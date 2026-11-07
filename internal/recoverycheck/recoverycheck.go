package recoverycheck

import (
	"context"
	"fmt"
	"time"

	core "github.com/brickKit/erp-billing/v3/internal/core"
	invoices "github.com/brickKit/erp-billing/v3/internal/invoices"
	masterdata "github.com/brickKit/erp-billing/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/recovery"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	"github.com/cockroachdb/apd/v3"
	"github.com/jackc/pgx/v5"
)

// CheckClaims is protocol 05's check between the invoices and their source after a full recovery (protocol 15, step
// 8). This component is the claimant, so it reports for both sides (P14.2): its invoices against the source's ledger.
const CheckClaims = "erp.billing.claims"

// settledWindow is how far back an order whose invoices are all settled is compared with the source.
const settledWindow = 30 * 24 * time.Hour

// VoidNumbers registers the INV numbers a full recovery raised the series over (P14.5).
func VoidNumbers(ctx context.Context, tx db.Tx, s db.Series, from, to int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO voided_number (legal_entity_id, series, period, number, reason)
		SELECT $1::uuid, $2, $3, n, 'a full recovery lost the invoice issued under this number' FROM generate_series($4::bigint, $5::bigint) n
		ON CONFLICT DO NOTHING`, s.LegalEntity, s.Code, s.Period, from, to)
	return err
}

// measure is a quantity and an amount.
type measure struct{ qty, amt apd.Decimal }

func (m *measure) add(q, a string) {
	x, _, _ := apd.NewFromString(q)
	y, _, _ := apd.NewFromString(a)
	_, _ = invoices.Arith.Add(&m.qty, &m.qty, x)
	_, _ = invoices.Arith.Add(&m.amt, &m.amt, y)
}

func (m *measure) String() string { return m.qty.Text('f') + " / " + m.amt.Text('f') }

// expected is what the source should say about one order line: billed exactly, claimed between the confirmed claims
// (min) and those plus the drafts' (max: a draft's claim may have expired).
type expected struct{ billed, claimedMin, claimedMax measure }

func cmpDec(a, b *apd.Decimal) int { return a.Cmp(b) }

// within reports whether got lies in [lo, hi], quantity and amount each.
func within(gotQ, gotA string, lo, hi *measure) bool {
	q, _, _ := apd.NewFromString(gotQ)
	a, _, _ := apd.NewFromString(gotA)
	return cmpDec(q, &lo.qty) >= 0 && cmpDec(q, &hi.qty) <= 0 && cmpDec(a, &lo.amt) >= 0 && cmpDec(a, &hi.amt) <= 0
}

// ClaimsCheck reads this database's invoices (the orders with an invoice not settled, or touched lately), then asks
// the source for each order's ledger outside any transaction and compares line by line.
func ClaimsCheck(m *core.Base, ctx context.Context, read recovery.Read) (recovery.Result, error) {
	var diffs []recovery.Difference
	orders := map[string]map[string]*expected{}
	var order []string
	err := read(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT 'invoice', id::text, 'issued as ' || number || ', formalization pending (order ' || source_id || ')'
			FROM invoice WHERE state = 'issued' AND formalization = 'pending' ORDER BY id`)
		if err != nil {
			return err
		}
		if diffs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (recovery.Difference, error) {
			var d recovery.Difference
			return d, r.Scan(&d.ObjectType, &d.ObjectID, &d.Detail)
		}); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT i.source_id::text, l.source_line_id::text, i.state, i.formalization,
				l.quantity::text, l.net::text, l.claimed_qty::text, l.claimed_amt::text
			FROM invoice i JOIN invoice_line l ON l.invoice_id = i.id
			WHERE i.source_id IN (SELECT source_id FROM invoice WHERE state IN ('draft', 'confirmed')
				OR state = 'issued' AND formalization <> 'done' OR updated_at > now() - make_interval(secs => $1))
			ORDER BY i.source_id, l.source_line_id`, settledWindow.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var src, line, state, formal, q, a, cq, ca string
			if err := rows.Scan(&src, &line, &state, &formal, &q, &a, &cq, &ca); err != nil {
				return err
			}
			if orders[src] == nil {
				orders[src] = map[string]*expected{}
				order = append(order, src)
			}
			e := orders[src][line]
			if e == nil {
				e = &expected{}
				orders[src][line] = e
			}
			switch {
			case state == "issued" && formal == "done": // the source recorded what was billed
				e.billed.add(q, a)
			case state == "confirmed" || state == "issued": // a confirmed claim at the source, never expiring
				e.claimedMin.add(cq, ca)
				e.claimedMax.add(cq, ca)
			case state == "draft": // a pending claim, or one that expired
				e.claimedMax.add(cq, ca)
			} // void: the claim was released
		}
		return rows.Err()
	})
	if err != nil {
		return recovery.Result{}, err
	}
	mismatch, unknown := len(diffs) > 0, false
	for _, src := range order {
		ledger, err := ledgerOf(m, ctx, src)
		if err != nil {
			unknown = true
			diffs = append(diffs, recovery.Difference{ObjectType: "sales_order", ObjectID: src,
				Detail: "the source's ledger could not be read: " + err.Error()})
			continue
		}
		seen := map[string]bool{}
		for _, l := range ledger.GetLines() {
			seen[l.GetOrderLineId()] = true
			e := orders[src][l.GetOrderLineId()]
			if e == nil {
				e = &expected{}
			}
			billed, claimed := l.GetBilled(), l.GetClaimed()
			if !within(billed.GetQuantity(), billed.GetAmount(), &e.billed, &e.billed) {
				mismatch = true
				diffs = append(diffs, recovery.Difference{ObjectType: "order_line", ObjectID: l.GetOrderLineId(),
					Detail: fmt.Sprintf("order %s: billed %s / %s at the source, %s in the formalized invoices", src,
						billed.GetQuantity(), billed.GetAmount(), e.billed.String())})
			}
			if !within(claimed.GetQuantity(), claimed.GetAmount(), &e.claimedMin, &e.claimedMax) {
				mismatch = true
				diffs = append(diffs, recovery.Difference{ObjectType: "order_line", ObjectID: l.GetOrderLineId(),
					Detail: fmt.Sprintf("order %s: claimed %s / %s at the source, between %s and %s in the invoices", src,
						claimed.GetQuantity(), claimed.GetAmount(), e.claimedMin.String(), e.claimedMax.String())})
			}
		}
		for line := range orders[src] {
			if !seen[line] {
				mismatch = true
				diffs = append(diffs, recovery.Difference{ObjectType: "order_line", ObjectID: line,
					Detail: "order " + src + ": invoiced here, not a line of the order at the source"})
			}
		}
	}
	res := recovery.Result{Outcome: recovery.Pass, Differences: diffs}
	switch {
	case mismatch:
		res.Outcome = recovery.Diff
		res.Disposal = "正式化中的发票：开放后由正式化作业推进，不另行处理；出账与来源不一致的行：按协议 05 人工核对" +
			"（来源多出的认领或出账：发票随恢复丢了，按证据与审计补录或在来源释放；发票多出的：来源丢了，重新确认或正式化）"
	case unknown:
		res.Outcome = recovery.Unprovable
		res.Disposal = "来源问不到：等它在部署里就绪后重做检查"
	}
	return res, nil
}

// ledgerOf asks the source for an order's ledger.
func ledgerOf(m *core.Base, ctx context.Context, order string) (*salesv3.LedgerResponse, error) {
	src, err := masterdata.Source(m)
	if err != nil {
		return nil, err
	}
	return src.Ledger(ctx, &salesv3.LedgerRequest{OrderId: order}, calls.Idempotent())
}
