package invoices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-billing/v3/internal/core"
	masterdata "github.com/brickKit/erp-billing/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
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

var Arith = apd.BaseContext.WithPrecision(80)

// times is a × b rounded half away from zero to scale places.
func times(a, b string, scale int32) string {
	x, _, _ := apd.NewFromString(a)
	y, _, _ := apd.NewFromString(b)
	var r apd.Decimal
	_, _ = Arith.Mul(&r, x, y)
	return money.Format(&r, scale)
}

// left is a − b − c, the remaining of a ledger dimension.
func left(a, b, c string) *apd.Decimal {
	x, _, _ := apd.NewFromString(a)
	y, _, _ := apd.NewFromString(b)
	z, _, _ := apd.NewFromString(c)
	var r apd.Decimal
	_, _ = Arith.Sub(&r, x, y)
	_, _ = Arith.Sub(&r, &r, z)
	return &r
}

type lineIn struct {
	OrderLineID string `json:"order_line_id"`
	Quantity    string `json:"quantity"`
	Amount      string `json:"amount"`
}

func checkLines(in []lineIn) error {
	if len(in) == 0 || len(in) > core.MaxLines {
		return core.Invalid("lines", fmt.Sprintf("1-%d lines", core.MaxLines))
	}
	seen := map[string]bool{}
	for i := range in {
		l := &in[i]
		f := fmt.Sprintf("lines[%d]", i)
		if !core.UUIDForm.MatchString(l.OrderLineID) || seen[l.OrderLineID] {
			return core.Invalid(f+".order_line_id", "an order line's ID, once")
		}
		seen[l.OrderLineID] = true
		var err error
		if l.Quantity, err = core.Decimal(f+".quantity", l.Quantity, 6); err != nil {
			return err
		}
		if l.Amount, err = core.Decimal(f+".amount", l.Amount, 4); err != nil {
			return err
		}
	}
	return nil
}

// draftClaim is the invoice ID and the claim of a draft request: the first try picks the ID and the lines (all the
// remaining when none are given), a retry with the same key gets the same two, so the source answers the first claim
// (docs/flows/billing.md, "草拟").
func draftClaim(m *core.Base, ctx context.Context, req idem.Request, o masterdata.Order, given []lineIn) (string, []*salesv3.ClaimLine, error) {
	var id string
	var stored []byte
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
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
		src, err := masterdata.Source(m)
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
			return "", nil, core.ErrNotBillable.New("order " + o.Number + " has nothing left to bill")
		}
	}
	raw, _ := json.Marshal(lines)
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO draft_key (principal, key, invoice_id, lines) VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT DO NOTHING`, req.Principal, req.Key, ids.New().String(), string(raw))
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return draftClaim(m, ctx, req, o, given) // the stored row, ours or a concurrent first try's
}

func unmarshalLines(raw []byte) []lineIn {
	var out []lineIn
	_ = json.Unmarshal(raw, &out)
	return out
}

// DraftInvoice claims the order's billable lines (all the remaining when no lines are given) under a new invoice ID
// and records the draft; a refused claim creates nothing (BL01).
func DraftInvoice(m *core.Base, c *httpx.Context) error {
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
	if !core.UUIDForm.MatchString(in.OrderID) {
		return core.Invalid("order_id", "order_id is a sales order's ID")
	}
	if in.Lines != nil {
		if err := checkLines(in.Lines); err != nil {
			return err
		}
	}
	o, err := masterdata.OrderOf(m, c.Ctx(), in.OrderID)
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpDraft, o.LegalEntityID)
	if err != nil {
		return err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, o.Currency)
	if err != nil {
		return err
	}
	id, claim, err := draftClaim(m, ctx, req, o, in.Lines)
	if err != nil {
		return err
	}
	src, err := masterdata.Source(m)
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
		return core.ErrNotBillable.New("the source refused the claim: " + strings.Join(why, "; "))
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		if _, err := core.GetInvoice(ctx, tx, id, false); err == nil {
			return IDResult(id), nil
		}
		return IDResult(id), insertDraft(m, ctx, tx, id, o, res.GetClaim().GetLines(), scale, actor)
	})
	if err != nil {
		return err
	}
	v, err := core.ReadInvoice(m, ctx, resultID(out))
	if err != nil {
		return err
	}
	return c.JSON(200, v)
}

// insertDraft records a draft from the claimed lines: each line's amount is the claim's amount, its tax the amount
// times the order line's rate rounded by line (as the order's), the totals the sums.
func insertDraft(m *core.Base, ctx context.Context, tx db.Tx, id string, o masterdata.Order, claimed []*salesv3.ClaimLine, scale int32,
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
	_, err := core.EmitInvoice(m, ctx, tx, id, actor, core.OpDraft)
	return err
}

// totals sets an invoice's sums from its lines (never rounded again).
func totals(ctx context.Context, tx db.Tx, id string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE invoice i SET net = coalesce((SELECT sum(net) FROM invoice_line WHERE invoice_id = i.id), 0),
		tax = coalesce((SELECT sum(tax) FROM invoice_line WHERE invoice_id = i.id), 0) WHERE i.id = $1`, id)
	return tag.RowsAffected() > 0, err
}

func IDResult(id string) idem.Outcome {
	b, _ := json.Marshal(map[string]string{"id": id})
	return idem.Outcome{Status: 200, Result: b}
}

func resultID(out idem.Outcome) string {
	var r struct{ ID string }
	_ = json.Unmarshal(out.Result, &r)
	return r.ID
}

func GetInvoice(m *core.Base, c *httpx.Context) error {
	v, err := core.ReadInvoice(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpRead, v.LegalEntityID); err != nil {
		return err
	}
	return c.JSON(200, v)
}

// UpdateInvoice lowers a draft's quantities and amounts; a line never goes above its claim.
func UpdateInvoice(m *core.Base, c *httpx.Context) error {
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
	cur, err := core.ReadInvoice(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpDraft, cur.LegalEntityID)
	if err != nil {
		return err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, cur.Currency)
	if err != nil {
		return err
	}
	var out core.Invoice
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := core.GetInvoice(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		if d := Decide(PhaseOf(v), Change); d.Effect == Refuse {
			return Refused(d, v)
		}
		switch {
		case in.Version != nil && *in.Version != v.Version:
			return core.ErrVersion.New(fmt.Sprintf("the invoice is at version %d", v.Version))
		}
		for _, l := range in.Lines {
			var fits *bool
			if err := tx.QueryRow(ctx, `SELECT $3::numeric <= claimed_qty AND $4::numeric <= claimed_amt FROM invoice_line
				WHERE invoice_id = $1 AND source_line_id = $2`, v.ID, l.OrderLineID, l.Quantity, l.Amount).Scan(&fits); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return core.Invalid("lines", "order line "+l.OrderLineID+" is not on the invoice")
				}
				return err
			}
			if !*fits {
				return core.ErrQuantity.New("order line " + l.OrderLineID + " goes above its claim")
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
		out, err = core.EmitInvoice(m, ctx, tx, v.ID, actor, core.OpDraft)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

// VoidInvoice voids a draft or confirmed invoice locally, then releases its claim (never the other way round, BL25);
// the release is the work item's when the source does not answer now.
func VoidInvoice(m *core.Base, c *httpx.Context) error {
	cur, err := core.ReadInvoice(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpDraft, cur.LegalEntityID)
	if err != nil {
		return err
	}
	var out core.Invoice
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := core.GetInvoice(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		switch d := Decide(PhaseOf(v), VoidIt); d.Effect {
		case Ignore:
			out = v
			return nil
		case Refuse:
			return Refused(d, v)
		default:
			out, err = SetVoid(m, ctx, tx, v, d, actor)
			return err
		}
	})
	if err != nil {
		return err
	}
	_ = release(m, ctx, out.ID)
	return c.JSON(200, Fresh(m, ctx, out))
}

// SetVoid makes an invoice void (d: a move to void decided for v, the user's void or an expired claim) and enqueues
// the release of its claim, in one transaction.
func SetVoid(m *core.Base, ctx context.Context, tx db.Tx, v core.Invoice, d Decision, actor string) (core.Invoice, error) {
	out, err := Transition(m, ctx, tx, v, d, nil, actor)
	if err != nil {
		return out, err
	}
	_, err = m.RT.Work.Enqueue(ctx, tx, core.JobRelease, InvoiceArgs{ID: v.ID})
	return out, err
}

// Fresh reads the invoice again (a step after the transaction may have changed it), falling back to what we had.
func Fresh(m *core.Base, ctx context.Context, v core.Invoice) core.Invoice {
	if n, err := core.ReadInvoice(m, ctx, v.ID); err == nil {
		return n
	}
	return v
}

type InvoiceArgs struct {
	ID string `json:"invoice_id"`
}

// release gives a void invoice's claim back at the source; a claim the source never had or already released is done.
func release(m *core.Base, ctx context.Context, id string) error {
	src, err := masterdata.Source(m)
	if err != nil {
		return err
	}
	_, err = src.Release(ctx, &salesv3.ReleaseRequest{ClaimId: id}, calls.Idempotent())
	if err != nil && core.ErrorsIsNotFound(err) {
		return nil
	}
	return err
}

func ReleaseJob(m *core.Base, ctx context.Context, r *work.Run) error {
	var a InvoiceArgs
	if err := json.Unmarshal(r.Args, &a); err != nil {
		return work.Permanent(err)
	}
	return release(m, ctx, a.ID)
}

// Read answers invoices for other components (InvoiceService): the data of erp.invoice.changed.v1, as JSON.
func Read(m *core.Base, ctx context.Context, in *billingv3.ReadRequest) (*billingv3.ReadResponse, error) {
	if len(in.GetIds()) > core.MaxReadIDs {
		return nil, core.Invalid("ids", fmt.Sprintf("at most %d IDs a call", core.MaxReadIDs))
	}
	out := &billingv3.ReadResponse{}
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		for _, id := range in.GetIds() {
			r := &billingv3.ReadResult{Id: id, State: billingv3.ReadState_READ_STATE_NOT_FOUND}
			if core.UUIDForm.MatchString(id) {
				v, err := core.GetInvoice(ctx, tx, id, false)
				switch {
				case err == nil:
					data, err := json.Marshal(v)
					if err != nil {
						return err
					}
					r.State, r.Version, r.Data = billingv3.ReadState_READ_STATE_EXISTS, v.Version, data
				case !errors.Is(err, core.ErrNone):
					return err
				}
			}
			out.Results = append(out.Results, r)
		}
		return nil
	})
	return out, err
}

// PurgeDraftKeys forgets the invoice IDs of draft requests old enough that no retry comes any more.
func PurgeDraftKeys(m *core.Base, ctx context.Context, _ *work.Run) error {
	return m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM draft_key WHERE created_at < now() - interval '7 days'`)
		return err
	})
}

// Corrections answers the corrections route of the contract: not in this version (D75); the invoice is read and the
// caller's right on its legal entity checked first, so an invoice the caller may not see stays unseen.
func Corrections(m *core.Base, c *httpx.Context) error {
	inv, err := core.ReadInvoice(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, c.Operation(), inv.LegalEntityID); err != nil {
		return err
	}
	return errs.New(errs.ReasonCapabilityUnsupported, "corrections are not in this version of erp/billing")
}
