package billing

import (
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/list"
)

// The invoice list (protocol 14: cursor pages, newest first; the filters and keyword declared here). Every field is
// ordinary, so a row is listed whole or not at all; the legal entity dimension compiles into the WHERE.
var invoiceList = list.Spec{Operation: OpRead, Alias: "i", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "customer", Column: "customer_id", Kind: list.UUID, Filterable: true},
		{Field: "source", Column: "source_id", Kind: list.UUID, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true},
		{Field: "invoice_date", Column: "invoice_date", Kind: list.Date, Filterable: true}},
	Search: []list.SearchColumn{{Name: "number_customer", Column: "search", Fields: []string{"number", "customer"}}}}

func (m *Module) listInvoices(c *httpx.Context) error {
	var f []list.Filter
	for param, field := range map[string]string{"legal_entity_id": "legal_entity_id", "customer_id": "customer",
		"order_id": "source", "state": "state"} {
		v := c.Query(param)
		if v == "" {
			continue
		}
		if param != "state" && !uuidForm.MatchString(v) {
			return invalid(param, param+" is an ID")
		}
		f = append(f, list.Filter{Field: field, Op: "eq", Values: []string{v}})
	}
	for param, op := range map[string]string{"from": "gte", "to": "lte"} {
		if v := c.Query(param); v != "" {
			if !dateForm.MatchString(v) {
				return invalid(param, param+" is YYYY-MM-DD")
			}
			f = append(f, list.Filter{Field: "invoice_date", Op: op, Values: []string{v}})
		}
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, invoiceList, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "created_at", Desc: true}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.rt.DB, q, "invoice", invoiceCols, scanInvoice,
		func(v Invoice) ([]string, string) { return []string{v.CreatedAt}, v.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"invoices": out, "next_cursor": next})
}
