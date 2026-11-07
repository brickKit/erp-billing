package issue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/brickKit/erp-billing/v3/internal/core"
	invoices "github.com/brickKit/erp-billing/v3/internal/invoices"
	masterdata "github.com/brickKit/erp-billing/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/audit"
	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/search"
	"github.com/brickKit/be-sdk-go/v3/work"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
)

// Issuing an invoice (docs/flows/billing.md, "开具"): the source confirms the claim, the legal entity is evidenced,
// the numbering transaction takes the next number of the continuous series and writes the "invoiced" fact, then the
// source formalizes. Every step after the confirmation has a work item that finishes it after a crash.

// IssueInvoice issues a draft (or a confirmed invoice whose issue stopped); issuing again answers the invoice.
func IssueInvoice(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	cur, err := core.ReadInvoice(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpIssue, cur.LegalEntityID)
	if err != nil {
		return err
	}
	switch d := invoices.Decide(invoices.PhaseOf(cur), invoices.Issue); d.Effect {
	case invoices.Ignore:
		return c.JSON(200, cur)
	case invoices.Refuse:
		return invoices.Refused(d, cur)
	case invoices.Process:
		if err := confirmClaim(m, ctx, cur.ID, actor); err != nil {
			return err
		}
	}
	le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID, true)
	if err != nil {
		return err // the invoice stays confirmed: void it, or issue again once the legal entity is back
	}
	_, _, err = idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		return invoices.IDResult(cur.ID), number(m, ctx, tx, cur.ID, le, actor)
	})
	if err != nil {
		return err
	}
	_ = formalize(m, ctx, cur.ID) // what is left is the work item's (BL04)
	return c.JSON(200, invoices.Fresh(m, ctx, cur))
}

// confirmClaim asks the source to confirm the claim before numbering (BL24: again answers the same). An expired claim
// voids the draft and answers CLAIM_EXPIRED (BL05, BL26); a confirmed one makes the draft confirmed and enqueues the
// continue item in the same transaction.
func confirmClaim(m *core.Base, ctx context.Context, id, actor string) error {
	src, err := masterdata.Source(m)
	if err != nil {
		return err
	}
	res, err := src.Confirm(ctx, &salesv3.ConfirmRequest{ClaimId: id}, calls.Idempotent())
	if err != nil {
		return err
	}
	expired := false
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := core.GetInvoice(ctx, tx, id, true)
		if err != nil {
			return err
		}
		answer := invoices.ClaimConfirmed
		if !res.GetConfirmed() {
			answer = invoices.ClaimExpired
		}
		d := invoices.Decide(invoices.PhaseOf(v), answer)
		switch {
		case d.Effect == invoices.Ignore:
			return nil
		case answer == invoices.ClaimExpired:
			expired = true
			_, err := invoices.SetVoid(m, ctx, tx, v, d, actor)
			return err
		}
		if _, err := invoices.Transition(m, ctx, tx, v, d, nil, actor); err != nil {
			return err
		}
		_, err = m.RT.Work.Enqueue(ctx, tx, core.JobContinue, invoices.InvoiceArgs{ID: id})
		return err
	})
	if err == nil && expired {
		return core.ErrClaimExpired.New("claim " + id + " expired at the source")
	}
	return err
}

// number is the numbering transaction: the invoice still confirmed (a void one never takes a number, BL25), the next
// number of INV per legal entity and year in this transaction (a rollback gives it back), the invoice issued with the
// legal entity's today as its date, the "invoiced" fact and the formalize item in the outbox and queue.
func number(m *core.Base, ctx context.Context, tx db.Tx, id string, le masterdata.LegalEntity, actor string) error {
	v, err := core.GetInvoice(ctx, tx, id, true)
	if err != nil {
		return err
	}
	d := invoices.Decide(invoices.PhaseOf(v), invoices.Number)
	switch d.Effect {
	case invoices.Ignore:
		return nil
	case invoices.Refuse:
		return invoices.Refused(d, v)
	}
	date, err := bizdate.Today(le.Timezone)
	if err != nil {
		return err
	}
	series := db.Series{LegalEntity: le.ID, Code: "INV", Period: date[:4]}
	n, err := db.NextNumber(ctx, tx, series)
	if err != nil {
		return err
	}
	number := fmt.Sprintf("INV-%s-%s-%06d", le.Code, date[:4], n)
	// the audit names the number issued: after a full recovery the tool raises the series past it (P14.5)
	if v, err = invoices.Transition(m, ctx, tx, v, d, &invoices.Numbered{Number: number, InvoiceDate: date,
		Search: search.Text(number, v.Customer.Name), SearchVersion: search.Version}, actor,
		map[string]any{"document_number": audit.DocumentNumber(series, n)}); err != nil {
		return err
	}
	if err := core.Emit(m, ctx, tx, core.InvoiceIssued, v.ID, 1, v.LegalEntityID, core.IssuedFact(v)); err != nil {
		return err
	}
	_, err = m.RT.Work.Enqueue(ctx, tx, core.JobFormalize, invoices.InvoiceArgs{ID: id})
	return err
}

// formalize tells the source what was billed (each line's quantity and amount, at most the claim) and records that
// it is done; formalizing again is harmless at the source.
func formalize(m *core.Base, ctx context.Context, id string) error {
	v, err := core.ReadInvoice(m, ctx, id)
	if err != nil || invoices.Decide(invoices.PhaseOf(v), invoices.Formalize).Effect == invoices.Ignore {
		return err
	}
	src, err := masterdata.Source(m)
	if err != nil {
		return err
	}
	billed := make([]*salesv3.ClaimLine, len(v.Lines))
	for i, l := range v.Lines {
		billed[i] = &salesv3.ClaimLine{OrderLineId: l.SourceLineID, Measure: &salesv3.Measure{Quantity: l.Quantity, Amount: l.Net}}
	}
	if _, err := src.Formalize(ctx, &salesv3.FormalizeRequest{ClaimId: id, Billed: billed}, calls.Idempotent()); err != nil {
		return err
	}
	return m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		v, err := core.GetInvoice(ctx, tx, id, true)
		if err != nil {
			return err
		}
		d := invoices.Decide(invoices.PhaseOf(v), invoices.Formalize)
		if d.Effect == invoices.Ignore { // formalized meanwhile
			return nil
		}
		_, err = invoices.Transition(m, ctx, tx, v, d, nil, core.ID)
		return err
	})
}

func FormalizeJob(m *core.Base, ctx context.Context, r *work.Run) error {
	var a invoices.InvoiceArgs
	if err := json.Unmarshal(r.Args, &a); err != nil {
		return work.Permanent(err)
	}
	return formalize(m, ctx, a.ID)
}

// ContinueJob numbers an invoice whose issue stopped after its claim was confirmed (the user meant to issue it,
// docs/design.md's open questions): it waits a minute for the request itself, then evidences the legal entity and
// numbers; one confirmed for a day without a number is logged as an alert.
func ContinueJob(m *core.Base, ctx context.Context, r *work.Run) error {
	var a invoices.InvoiceArgs
	if err := json.Unmarshal(r.Args, &a); err != nil {
		return work.Permanent(err)
	}
	var cur core.Invoice
	var age time.Duration
	err := m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		if cur, err = core.GetInvoice(ctx, tx, a.ID, false); err != nil {
			return err
		}
		var secs float64
		err = tx.QueryRow(ctx, `SELECT extract(epoch FROM now() - updated_at) FROM invoice WHERE id = $1`, a.ID).Scan(&secs)
		age = time.Duration(secs * float64(time.Second))
		return err
	})
	if err != nil || invoices.Decide(invoices.PhaseOf(cur), invoices.Continue).Effect == invoices.Ignore {
		return err
	}
	if age < core.ContinueAfter {
		return work.Wait(errors.New("the issue request may still be numbering it"))
	}
	if age > core.StuckAfter {
		m.RT.Logger.Error("an invoice has been confirmed without a number for more than a day", "invoice", a.ID)
	}
	v, err := core.ReadInvoice(m, ctx, a.ID)
	if err != nil {
		return err
	}
	le, err := masterdata.LegalEntityOf(m, ctx, v.LegalEntityID, true)
	if err != nil {
		return err
	}
	if err := m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return number(m, ctx, tx, a.ID, le, core.ID)
	}); err != nil {
		return err
	}
	return formalize(m, ctx, a.ID)
}
