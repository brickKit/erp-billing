package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/money"
	"github.com/brickKit/be-sdk-go/v3/search"
	"github.com/brickKit/be-sdk-go/v3/work"
	billingv3 "github.com/brickKit/erp-billing/contract/v3/gen/go/be/erp/billing/v3"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	"github.com/cockroachdb/apd/v3"
	"github.com/jackc/pgx/v5"
)

// Drafting an invoice from a sales order, changing and voiding drafts, reading invoices (docs/flows/billing.md).

var arith = apd.BaseContext.WithPrecision(80)

// times is a × b rounded half away from zero to scale places.
func times(a, b string, scale int32) string {
	x, _, _ := apd.NewFromString(a)
	y, _, _ := apd.NewFromString(b)
	var r apd.Decimal
	_, _ = arith.Mul(&r, x, y)
	return money.Format(&r, scale)
}

// left is a − b − c, the remaining of a ledger dimension.
func left(a, b, c string) *apd.Decimal {
	x, _, _ := apd.NewFromString(a)
	y, _, _ := apd.NewFromString(b)
	z, _, _ := apd.NewFromString(c)
	var r apd.Decimal
	_, _ = arith.Sub(&r, x, y)
	_, _ = arith.Sub(&r, &r, z)
	return &r
}

type lineIn struct {
	OrderLineID string `json:"order_line_id"`
	Quantity    string `json:"quantity"`
	Amount      string `json:"amount"`
}

func checkLines(in []lineIn) error {
	if len(in) == 0 || len(in) > MaxLines {
		return invalid("lines", fmt.Sprintf("1-%d lines", MaxLines))
	}
	seen := map[string]bool{}
	for i := range in {
		l := &in[i]
		f := fmt.Sprintf("lines[%d]", i)
		if !uuidForm.MatchString(l.OrderLineID) || seen[l.OrderLineID] {
			return invalid(f+".order_line_id", "an order line's ID, once")
		}
		seen[l.OrderLineID] = true
		var err error
		if l.Quantity, err = decimal(f+".quantity", l.Quantity, 6); err != nil {
			return err
		}
		if l.Amount, err = decimal(f+".amount", l.Amount, 4); err != nil {
			return err
		}
	}
	return nil
}

// draftClaim is the invoice ID and the claim of a draft request: the first try picks the ID and the lines (all the
// remaining when none are given), a retry with the same key gets the same two, so the source answers the first claim
// (docs/flows/billing.md, "草拟").
func (m *Module) draftClaim(ctx context.Context, req idem.Request, o order, given []lineIn) (string, []*salesv3.ClaimLine, error) {
	var id string
	var stored []byte
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT invoice_id::text, lines FROM draft_key WHERE principal = $1 AND key = $2`,
			req.Principal, req.Key).Scan(&id, &stored)
	})
	switch {
	case err == nil:
		var lines []*salesv3.ClaimLine
		for _, l := range unmarshalLines(stored) {
			lines = append(lines, &salesv3.ClaimLine{OrderLineId: l.OrderLineID,
				Measure: &salesv3.Measure{Quantity: l.Quantity, Amount: l.Amount}})
		}
		return id, lines, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", nil, err
	}
	lines := given
	if lines == nil { // every line's remaining
		src, err := m.source()
		if err != nil {
			return "", nil, err
		}
		ledger, err := src.Ledger(ctx, &salesv3.LedgerRequest{OrderId: o.ID}, calls.Idempotent())
		if err != nil {
			return "", nil, err
		}
		for _, l := range ledger.GetLines() {
			q := left(l.GetBillable().GetQuantity(), l.GetClaimed().GetQuantity(), l.GetBilled().GetQuantity())
			a := left(l.GetBillable().GetAmount(), l.GetClaimed().GetAmount(), l.GetBilled().GetAmount())
			if q.Sign() > 0 || a.Sign() > 0 {
				lines = append(lines, lineIn{OrderLineID: l.GetOrderLineId(), Quantity: q.Text('f'), Amount: a.Text('f')})
			}
		}
		if len(lines) == 0 {
			return "", nil, errNotBillable.New("order " + o.Number + " has nothing left to bill")
		}
	}
	raw, _ := json.Marshal(lines)
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO draft_key (principal, key, invoice_id, lines) VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT DO NOTHING`, req.Principal, req.Key, ids.New().String(), string(raw))
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return m.draftClaim(ctx, req, o, given) // the stored row, ours or a concurrent first try's
}

func unmarshalLines(raw []byte) []lineIn {
	var out []lineIn
	_ = json.Unmarshal(raw, &out)
	return out
}

// draftInvoice claims the order's billable lines (all the remaining when no lines are given) under a new invoice ID
// and records the draft; a refused claim creates nothing (BL01).
func (m *Module) draftInvoice(c *httpx.Context) error {
	var in struct {
		OrderID string   `json:"order_id"`
		Lines   []lineIn `json:"lines"`
	}
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if !uuidForm.MatchString(in.OrderID) {
		return invalid("order_id", "order_id is a sales order's ID")
	}
	if in.Lines != nil {
		if err := checkLines(in.Lines); err != nil {
			return err
		}
	}
	o, err := m.orderOf(c.Ctx(), in.OrderID)
	if err != nil {
		return err
	}
	ctx, actor, err := check(c, OpDraft, o.LegalEntityID)
	if err != nil {
		return err
	}
	scale, err := m.currencyScale(ctx, o.Currency)
	if err != nil {
		return err
	}
	id, claim, err := m.draftClaim(ctx, req, o, in.Lines)
	if err != nil {
		return err
	}
	src, err := m.source()
	if err != nil {
		return err
	}
	res, err := src.Claim(ctx, &salesv3.ClaimRequest{ClaimId: id, OrderId: o.ID, Lines: claim}, calls.Idempotent())
	if err != nil {
		return err
	}
	if !res.GetClaimed() {
		var why []string
		for _, p := range res.GetProblems() {
			why = append(why, strings.TrimSpace(p.GetOrderLineId()+" "+p.GetReason()))
		}
		return errNotBillable.New("the source refused the claim: " + strings.Join(why, "; "))
	}
	out, _, err := idem.Do(ctx, m.rt.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		if _, err := getInvoice(ctx, tx, id, false); err == nil {
			return idResult(id), nil
		}
		return idResult(id), m.insertDraft(ctx, tx, id, o, res.GetClaim().GetLines(), scale, actor)
	})
	if err != nil {
		return err
	}
	v, err := m.invoice(ctx, resultID(out))
	if err != nil {
		return err
	}
	return c.JSON(200, v)
}

// insertDraft records a draft from the claimed lines: each line's amount is the claim's amount, its tax the amount
// times the order line's rate rounded by line (as the order's), the totals the sums.
func (m *Module) insertDraft(ctx context.Context, tx db.Tx, id string, o order, claimed []*salesv3.ClaimLine, scale int32,
	actor string) error {
	var rate, rateDate, rateType *string
	if o.ExchangeRate != nil {
		rate, rateDate, rateType = &o.ExchangeRate.Rate, &o.ExchangeRate.Date, &o.ExchangeRate.Type
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice (id, legal_entity_id, customer_id, customer_name, customer_tax_id,
		currency, rate, rate_date, rate_type, source_id, search, search_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::date, $9, $10, $11, $12)`, id, o.LegalEntityID, o.Customer.PartnerID,
		o.Customer.Name, o.Customer.TaxID, o.Currency, rate, rateDate, rateType, o.ID, search.Text(o.Customer.Name),
		search.Version); err != nil {
		return err
	}
	no := 0
	for _, ol := range o.Lines { // in the order's line order
		for _, c := range claimed {
			if c.GetOrderLineId() != ol.ID {
				continue
			}
			no++
			q, a := c.GetMeasure().GetQuantity(), c.GetMeasure().GetAmount()
			if _, err := tx.Exec(ctx, `INSERT INTO invoice_line (id, invoice_id, line_no, source_line_id, product_id,
				product_code, product_name, unit, unit_price, quantity, net, tax_rate, tax, claimed_qty, claimed_amt)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13::numeric,
				$10::numeric, $11::numeric)`, ids.New().String(), id, no, ol.ID, ol.Product.ID, ol.Product.Code,
				ol.Product.Name, ol.Unit, ol.UnitPrice, q, a, ol.TaxRate, times(a, ol.TaxRate, scale)); err != nil {
				return err
			}
		}
	}
	if _, err := totals(ctx, tx, id); err != nil {
		return err
	}
	_, err := m.emitInvoice(ctx, tx, id, actor, OpDraft)
	return err
}

// totals sets an invoice's sums from its lines (never rounded again).
func totals(ctx context.Context, tx db.Tx, id string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE invoice i SET net = coalesce((SELECT sum(net) FROM invoice_line WHERE invoice_id = i.id), 0),
		tax = coalesce((SELECT sum(tax) FROM invoice_line WHERE invoice_id = i.id), 0) WHERE i.id = $1`, id)
	return tag.RowsAffected() > 0, err
}

func idResult(id string) idem.Outcome {
	b, _ := json.Marshal(map[string]string{"id": id})
	return idem.Outcome{Status: 200, Result: b}
}

func resultID(out idem.Outcome) string {
	var r struct{ ID string }
	_ = json.Unmarshal(out.Result, &r)
	return r.ID
}

func (m *Module) getInvoice(c *httpx.Context) error {
	v, err := m.invoice(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := check(c, OpRead, v.LegalEntityID); err != nil {
		return err
	}
	return c.JSON(200, v)
}

// updateInvoice lowers a draft's quantities and amounts; a line never goes above its claim.
func (m *Module) updateInvoice(c *httpx.Context) error {
	var in struct {
		Lines   []lineIn `json:"lines"`
		Version *int64   `json:"version"`
	}
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if err := checkLines(in.Lines); err != nil {
		return err
	}
	cur, err := m.invoice(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := check(c, OpDraft, cur.LegalEntityID)
	if err != nil {
		return err
	}
	scale, err := m.currencyScale(ctx, cur.Currency)
	if err != nil {
		return err
	}
	var out Invoice
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := getInvoice(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		switch {
		case v.State != "draft":
			return errState.New("only a draft changes; this invoice is " + v.State)
		case in.Version != nil && *in.Version != v.Version:
			return errVersion.New(fmt.Sprintf("the invoice is at version %d", v.Version))
		}
		for _, l := range in.Lines {
			var fits *bool
			if err := tx.QueryRow(ctx, `SELECT $3::numeric <= claimed_qty AND $4::numeric <= claimed_amt FROM invoice_line
				WHERE invoice_id = $1 AND source_line_id = $2`, v.ID, l.OrderLineID, l.Quantity, l.Amount).Scan(&fits); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return invalid("lines", "order line "+l.OrderLineID+" is not on the invoice")
				}
				return err
			}
			if !*fits {
				return errQuantity.New("order line " + l.OrderLineID + " goes above its claim")
			}
			if _, err := tx.Exec(ctx, `UPDATE invoice_line SET quantity = $3::numeric, net = $4::numeric,
				tax = round($4::numeric * tax_rate, $5) WHERE invoice_id = $1 AND source_line_id = $2`,
				v.ID, l.OrderLineID, l.Quantity, l.Amount, scale); err != nil {
				return err
			}
		}
		if _, err := totals(ctx, tx, v.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invoice SET version = version + 1, updated_at = now() WHERE id = $1`, v.ID); err != nil {
			return err
		}
		out, err = m.emitInvoice(ctx, tx, v.ID, actor, OpDraft)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

// voidInvoice voids a draft or confirmed invoice locally, then releases its claim (never the other way round, BL25);
// the release is the work item's when the source does not answer now.
func (m *Module) voidInvoice(c *httpx.Context) error {
	cur, err := m.invoice(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := check(c, OpDraft, cur.LegalEntityID)
	if err != nil {
		return err
	}
	var out Invoice
	err = m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := getInvoice(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		switch v.State {
		case "void":
			out = v
			return nil
		case "issued":
			return errState.New("an issued invoice is corrected, never voided")
		}
		if out, err = m.setVoid(ctx, tx, v.ID, actor); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = m.release(ctx, out.ID)
	return c.JSON(200, m.fresh(ctx, out))
}

// setVoid makes an invoice void and enqueues the release of its claim, in one transaction.
func (m *Module) setVoid(ctx context.Context, tx db.Tx, id, actor string) (Invoice, error) {
	if _, err := tx.Exec(ctx, `UPDATE invoice SET state = 'void', version = version + 1, updated_at = now() WHERE id = $1`,
		id); err != nil {
		return Invoice{}, err
	}
	if _, err := m.rt.Work.Enqueue(ctx, tx, jobRelease, invoiceArgs{ID: id}); err != nil {
		return Invoice{}, err
	}
	return m.emitInvoice(ctx, tx, id, actor, OpDraft)
}

// fresh reads the invoice again (a step after the transaction may have changed it), falling back to what we had.
func (m *Module) fresh(ctx context.Context, v Invoice) Invoice {
	if n, err := m.invoice(ctx, v.ID); err == nil {
		return n
	}
	return v
}

type invoiceArgs struct {
	ID string `json:"invoice_id"`
}

// release gives a void invoice's claim back at the source; a claim the source never had or already released is done.
func (m *Module) release(ctx context.Context, id string) error {
	src, err := m.source()
	if err != nil {
		return err
	}
	_, err = src.Release(ctx, &salesv3.ReleaseRequest{ClaimId: id}, calls.Idempotent())
	if err != nil && errorsIsNotFound(err) {
		return nil
	}
	return err
}

func (m *Module) releaseJob(ctx context.Context, r *work.Run) error {
	var a invoiceArgs
	if err := json.Unmarshal(r.Args, &a); err != nil {
		return work.Permanent(err)
	}
	return m.release(ctx, a.ID)
}

// Read answers invoices for other components (InvoiceService): the data of erp.invoice.changed.v1, as JSON.
func (m *Module) Read(ctx context.Context, in *billingv3.ReadRequest) (*billingv3.ReadResponse, error) {
	if len(in.GetIds()) > MaxReadIDs {
		return nil, invalid("ids", fmt.Sprintf("at most %d IDs a call", MaxReadIDs))
	}
	out := &billingv3.ReadResponse{}
	err := m.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		for _, id := range in.GetIds() {
			r := &billingv3.ReadResult{Id: id, State: billingv3.ReadState_READ_STATE_NOT_FOUND}
			if uuidForm.MatchString(id) {
				v, err := getInvoice(ctx, tx, id, false)
				switch {
				case err == nil:
					data, err := json.Marshal(v)
					if err != nil {
						return err
					}
					r.State, r.Version, r.Data = billingv3.ReadState_READ_STATE_EXISTS, v.Version, data
				case !errors.Is(err, errNone):
					return err
				}
			}
			out.Results = append(out.Results, r)
		}
		return nil
	})
	return out, err
}
