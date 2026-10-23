package billing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/jackc/pgx/v5"
)

// Customer is the customer as the source order had it (protocol 07: a historical reference).
type Customer struct {
	PartnerID string  `json:"partner_id"`
	Name      string  `json:"name"`
	TaxID     *string `json:"tax_id"`
}

// Rate is the order's exchange rate snapshot.
type Rate struct {
	Rate string `json:"rate"`
	Date string `json:"date"`
	Type string `json:"type"`
}

// Source names what the invoice bills.
type Source struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Product is a line's product snapshot.
type Product struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// Line is an invoice line.
type Line struct {
	ID           string  `json:"id"`
	LineNo       int     `json:"line_no"`
	SourceLineID string  `json:"source_line_id"`
	Product      Product `json:"product"`
	Quantity     string  `json:"quantity"`
	Unit         string  `json:"unit"`
	UnitPrice    string  `json:"unit_price"`
	Net          string  `json:"net"`
	TaxRate      string  `json:"tax_rate"`
	Tax          string  `json:"tax"`

	ClaimedQty string `json:"-"`
	ClaimedAmt string `json:"-"`
}

// Totals are an invoice's sums.
type Totals struct {
	Net   string `json:"net"`
	Tax   string `json:"tax"`
	Gross string `json:"gross"`
}

// PrintState is what an invoice says about its print snapshot.
type PrintState struct {
	Template        *string `json:"template"`
	TemplateVersion *int64  `json:"template_version"`
	Count           int     `json:"count"`
}

// Invoice is an invoice's complete state (erp.invoice.changed.v1; also InvoiceService.Read's data).
type Invoice struct {
	ID            string     `json:"id"`
	Number        *string    `json:"number"`
	LegalEntityID string     `json:"legal_entity_id"`
	Customer      Customer   `json:"customer"`
	Currency      string     `json:"currency"`
	ExchangeRate  *Rate      `json:"exchange_rate"`
	Source        Source     `json:"source"`
	State         string     `json:"state"`
	Formalization string     `json:"formalization"`
	InvoiceDate   *string    `json:"invoice_date"`
	Lines         []Line     `json:"lines"`
	Totals        Totals     `json:"totals"`
	Print         PrintState `json:"print"`
	Version       int64      `json:"version"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`

	PrintData   []byte     `json:"-"`
	PrintDigest *string    `json:"-"`
	IssuedAt    *time.Time `json:"-"`
}

const invoiceCols = `i.id::text, i.number, i.legal_entity_id::text, i.customer_id::text, i.customer_name, i.customer_tax_id,
	i.currency, trim_scale(i.rate)::text, i.rate_date::text, i.rate_type, i.source_kind, i.source_id::text, i.state,
	i.formalization, i.invoice_date::text, trim_scale(i.net)::text, trim_scale(i.tax)::text, trim_scale(i.net + i.tax)::text,
	i.print_template, i.print_version, i.print_count, i.print_data, i.print_digest, i.issued_at, i.version, i.created_at,
	i.updated_at,
	COALESCE((SELECT jsonb_agg(jsonb_build_object('id', l.id::text, 'line_no', l.line_no, 'source_line_id', l.source_line_id::text,
		'product', jsonb_build_object('id', l.product_id::text, 'code', l.product_code, 'name', l.product_name),
		'quantity', trim_scale(l.quantity)::text, 'unit', l.unit, 'unit_price', trim_scale(l.unit_price)::text,
		'net', trim_scale(l.net)::text, 'tax_rate', trim_scale(l.tax_rate)::text, 'tax', trim_scale(l.tax)::text,
		'claimed_qty', trim_scale(l.claimed_qty)::text, 'claimed_amt', trim_scale(l.claimed_amt)::text) ORDER BY l.line_no)
		FROM invoice_line l WHERE l.invoice_id = i.id), '[]'::jsonb)`

func scanInvoice(row pgx.Row) (Invoice, error) {
	var v Invoice
	var rate, rateDate, rateType *string
	var created, updated time.Time
	var lines []byte
	err := row.Scan(&v.ID, &v.Number, &v.LegalEntityID, &v.Customer.PartnerID, &v.Customer.Name, &v.Customer.TaxID,
		&v.Currency, &rate, &rateDate, &rateType, &v.Source.Kind, &v.Source.ID, &v.State, &v.Formalization, &v.InvoiceDate,
		&v.Totals.Net, &v.Totals.Tax, &v.Totals.Gross, &v.Print.Template, &v.Print.TemplateVersion, &v.Print.Count,
		&v.PrintData, &v.PrintDigest, &v.IssuedAt, &v.Version, &created, &updated, &lines)
	if err != nil {
		return v, err
	}
	if rate != nil {
		v.ExchangeRate = &Rate{Rate: *rate, Date: *rateDate, Type: *rateType}
	}
	v.CreatedAt, v.UpdatedAt = canon.Time(created), canon.Time(updated)
	var raw []struct {
		Line
		ClaimedQty string `json:"claimed_qty"`
		ClaimedAmt string `json:"claimed_amt"`
	}
	if err := json.Unmarshal(lines, &raw); err != nil {
		return v, err
	}
	v.Lines = make([]Line, len(raw))
	for i, r := range raw {
		v.Lines[i] = r.Line
		v.Lines[i].ClaimedQty, v.Lines[i].ClaimedAmt = r.ClaimedQty, r.ClaimedAmt
	}
	return v, nil
}

// errNone is a lookup that found no row.
var errNone = errors.New("no such record")

// getInvoice reads an invoice; lock takes the invoice row's lock, the one every change of the invoice takes.
func getInvoice(ctx context.Context, tx db.Tx, id string, lock bool) (Invoice, error) {
	if lock {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM invoice WHERE id = $1::uuid FOR UPDATE`, id); err != nil {
			return Invoice{}, err
		}
	}
	v, err := scanInvoice(tx.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoice i WHERE i.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNone
	}
	return v, err
}

func (m *Module) invoice(ctx context.Context, id string) (Invoice, error) {
	var v Invoice
	if !uuidForm.MatchString(id) {
		return v, notFound("invoice")
	}
	err := m.rt.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		v, err = getInvoice(ctx, tx, id, false)
		return err
	})
	if errors.Is(err, errNone) {
		return v, notFound("invoice")
	}
	return v, err
}

// emitInvoice publishes an invoice's state after its rows changed and records the change.
func (m *Module) emitInvoice(ctx context.Context, tx db.Tx, id, actor, op string) (Invoice, error) {
	v, err := getInvoice(ctx, tx, id, false)
	if err != nil {
		return v, err
	}
	if err := m.emit(ctx, tx, InvoiceChanged, v.ID, v.Version, v.LegalEntityID, v); err != nil {
		return v, err
	}
	return v, m.change(ctx, tx, actor, op, v.ID, v)
}

// issuedFact is erp.invoice.issued.v1: the fact finance and credit must not miss.
func issuedFact(v Invoice) map[string]any {
	return map[string]any{"fact_id": v.ID, "number": *v.Number, "legal_entity_id": v.LegalEntityID,
		"customer_id": v.Customer.PartnerID, "currency": v.Currency, "exchange_rate": v.ExchangeRate, "source": v.Source,
		"order_id": v.Source.ID, "invoice_date": *v.InvoiceDate, "lines": v.Lines, "totals": v.Totals,
		"issued_at": canon.Time(*v.IssuedAt)}
}
