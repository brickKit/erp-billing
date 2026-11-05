//go:build integration

package billing

import (
	"context"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/recoverypb"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// checksStory is a full recovery's checks against this member (be-protocol chapter 14): the recovery tool only; the
// audit of a business change names the object's version after it (P14.3), and the owner answers whether the object
// is still there at that version.
func checksStory(t *testing.T, w *sectest.World, admin *pgx.Conn, typ, id string) {
	ctx := context.Background()
	dial := func(as string) recoverypb.CheckServiceClient {
		conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(w.TLS(t, as).ClientConfig(ID))))
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return recoverypb.NewCheckServiceClient(conn)
	}
	_, err := dial("mdm/partner").Check(ctx, &recoverypb.CheckRequest{Recovery: "r"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "the recovery tool only: %v", err)
	cs := dial("ops/recover")
	checked, err := cs.Check(ctx, &recoverypb.CheckRequest{Recovery: "r"})
	require.NoError(t, err)
	assert.NotEmpty(t, checked.GetResults())

	var version string
	require.NoError(t, admin.QueryRow(ctx, "SELECT target->>'version' FROM "+itSchema+".be_audit_log WHERE target->>'type' = $1 "+
		"AND target->>'id' = $2 ORDER BY ord DESC LIMIT 1", typ, id).Scan(&version), "the audit names the version")
	v, err := strconv.ParseInt(version, 10, 64)
	require.NoError(t, err)
	present, err := cs.Present(ctx, &recoverypb.PresentRequest{Recovery: "r", Objects: []*recoverypb.ObjectRef{
		{Type: typ, Id: id, Version: version}, {Type: typ, Id: id, Version: strconv.FormatInt(v+1, 10)},
		{Type: typ, Id: ids.New().String(), Version: "1"}}})
	require.NoError(t, err)
	var answers []string
	for _, a := range present.GetAnswers() {
		answers = append(answers, a.GetAnswer())
	}
	assert.Equal(t, []string{"present", "older", "absent"}, answers)
}

// checkService is the recovery tool's client of this member's CheckService.
func (fx *fixture) checkService() recoverypb.CheckServiceClient {
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(fx.t, "ops/recover").ClientConfig(ID))))
	require.NoError(fx.t, err)
	fx.t.Cleanup(func() { conn.Close() })
	return recoverypb.NewCheckServiceClient(conn)
}

// claims runs the recovery's checks and answers erp.billing.claims.
func (fx *fixture) claims() *recoverypb.CheckResult {
	fx.t.Helper()
	res, err := fx.checkService().Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	require.NoError(fx.t, err)
	for _, r := range res.GetResults() {
		if r.GetCheck() == CheckClaims {
			return r
		}
	}
	fx.t.Fatal("no " + CheckClaims)
	return nil
}

func details(r *recoverypb.CheckResult) map[string]string {
	out := map[string]string{}
	for _, d := range r.GetDifferences() {
		out[d.GetObjectId()] = d.GetDetail()
	}
	return out
}

// recoveryChecks: the presence of what this member audits; protocol 05's check of the invoices against the source's
// ledger; the continuous INV series raised over numbers a recovery lost, each registered void.
func (fx *fixture) recoveryChecks() {
	t := fx.t
	inv := fx.first["id"].(string)
	t.Run("recovery checks", func(t *testing.T) { checksStory(t, fx.w, fx.admin, Invoices, inv) })

	base := fx.claims()
	require.Equal(t, "pass", base.GetOutcome(), "after the flows the invoices and the source agree: %v", base.GetDifferences())

	// the source billed more on a line than the formalized invoices say
	var line string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT source_line_id::text FROM "+itSchema+".invoice_line WHERE invoice_id = $1 LIMIT 1",
		inv).Scan(&line))
	fx.sales.mu.Lock()
	g := fx.sales.ledger[line]
	was := g.billed[0]
	g.billed[0] = new(big.Rat).Add(was, big.NewRat(1, 1))
	fx.sales.mu.Unlock()
	got := fx.claims()
	assert.Equal(t, "diff", got.GetOutcome())
	assert.Contains(t, details(got)[line], "billed")
	fx.sales.mu.Lock()
	g.billed[0] = was
	fx.sales.mu.Unlock()

	// a voided invoice (its claim released at the source) taken as a draft fits: a draft's claim may have expired; taken
	// as confirmed it does not: a confirmed claim never expires
	var voided string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT i.id::text FROM "+itSchema+".invoice i WHERE i.state = 'void' AND EXISTS "+
		"(SELECT 1 FROM "+itSchema+".invoice_line l WHERE l.invoice_id = i.id) LIMIT 1").Scan(&voided))
	set := func(state string) {
		_, err := fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice SET state = $2 WHERE id = $1", voided, state)
		require.NoError(t, err)
	}
	set("draft")
	assert.Equal(t, "pass", fx.claims().GetOutcome(), "a draft whose claim is gone")
	set("confirmed")
	got = fx.claims()
	assert.Equal(t, "diff", got.GetOutcome(), "a confirmed invoice whose claim the source does not hold")
	set("void")

	// a line invoiced here that the order at the source does not have
	var lineID string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT id::text FROM "+itSchema+".invoice_line WHERE invoice_id = $1 LIMIT 1", inv).Scan(&lineID))
	stranger := ids.New().String()
	_, err := fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice_line SET source_line_id = $2 WHERE id = $1", lineID, stranger)
	require.NoError(t, err)
	assert.Contains(t, details(fx.claims())[stranger], "not a line of the order")
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice_line SET source_line_id = $2 WHERE id = $1", lineID, line)
	require.NoError(t, err)

	// an invoice in its formalization at the recovery point
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice SET formalization = 'pending' WHERE id = $1", inv)
	require.NoError(t, err)
	got = fx.claims()
	assert.Equal(t, "diff", got.GetOutcome())
	assert.Contains(t, details(got)[inv], "formalization pending")
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice SET formalization = 'done' WHERE id = $1", inv)
	require.NoError(t, err)

	// the source cannot be asked
	fx.sales.mu.Lock()
	fx.sales.mode["ledger"] = "down"
	fx.sales.mu.Unlock()
	assert.Equal(t, "unprovable", fx.claims().GetOutcome())
	fx.sales.mu.Lock()
	delete(fx.sales.mode, "ledger")
	fx.sales.mu.Unlock()
	// the calls' circuit to the source opened on the failures: pass again once it lets a call through
	require.Eventually(t, func() bool { return fx.claims().GetOutcome() == "pass" }, 60*time.Second, time.Second)

	// the INV series: listed with its used maximum, raised over two lost numbers, both registered void
	cs := fx.checkService()
	nums, err := cs.Numbers(fx.ctx, &recoverypb.NumbersRequest{Recovery: "r"})
	require.NoError(t, err)
	var inv1 *recoverypb.Series
	for _, s := range nums.GetSeries() {
		if s.GetCode() == "INV" && s.GetLegalEntity() == fx.le {
			inv1 = s
		}
	}
	require.NotNil(t, inv1, "%v", nums.GetSeries())
	raised, err := cs.AdvanceNumbers(fx.ctx, &recoverypb.AdvanceNumbersRequest{Recovery: "r", Series: []*recoverypb.Series{
		{LegalEntity: inv1.GetLegalEntity(), Code: "INV", Period: inv1.GetPeriod(), MaxUsed: inv1.GetMaxUsed() + 2}}})
	require.NoError(t, err)
	assert.Equal(t, inv1.GetMaxUsed()+2, raised.GetSeries()[0].GetMaxUsed())
	rows, err := fx.admin.Query(fx.ctx, "SELECT number FROM "+itSchema+".voided_number WHERE legal_entity_id = $1 AND series = 'INV' ORDER BY number", fx.le)
	require.NoError(t, err)
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err)
	assert.Equal(t, []int64{inv1.GetMaxUsed() + 1, inv1.GetMaxUsed() + 2}, numbers)
}
