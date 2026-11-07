//go:build integration

package itest

import (
	"time"

	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// lifecycle: the cells of the invoice's lifecycle the business flows do not reach, each answered as it is today
// (docs/design.md, "账单的状态"; rules IN01, IN02, IN03, IN08): a confirmed, an issued (formalization pending and done)
// and a void invoice refuse a change; a confirmed one is voided and releases its claim; an issued one is never voided;
// voiding again answers the void invoice; only an issued invoice prints. It runs last: the confirmed invoice it makes
// is voided, so it takes no number from the steps before.
func (fx *fixture) lifecycle() {
	t := fx.t
	path := func(v map[string]any, verb string) string {
		p := "/api/billing/invoices/" + v["id"].(string)
		if verb != "" {
			p += "/" + verb
		}
		return p
	}
	change := func(v map[string]any) (int, map[string]any) {
		line := at(v, "lines", 0).(map[string]any)
		return fx.alice.do("PATCH", path(v, ""), map[string]any{"lines": []any{map[string]any{
			"order_line_id": line["source_line_id"], "quantity": line["quantity"], "amount": line["net"]}}})
	}
	refused := func(code int, body map[string]any, detail string) {
		t.Helper()
		assert.Equal(t, 409, code, body)
		assert.Equal(t, "STATE_CONFLICT", body["reason"], body)
		assert.Equal(t, detail, body["detail"], body)
	}

	// confirmed: the legal entity stops the issue after the claim was confirmed (BL21)
	o, _, _ := fx.order()
	_, confirmed := fx.draft(o)
	fx.org.set(fx.le, "suspended")
	code, _ := fx.issue(confirmed["id"].(string))
	require.Equal(t, 409, code)
	fx.org.set(fx.le, "active")
	confirmed = fx.get(confirmed["id"].(string))
	require.Equal(t, "confirmed", confirmed["state"])
	code, got := change(confirmed)
	refused(code, got, "only a draft changes; this invoice is confirmed")
	code, got = fx.alice.do("POST", path(confirmed, "print"), nil)
	refused(code, got, "only an issued invoice prints; this one is confirmed")
	code, got = fx.alice.do("POST", path(confirmed, "void"), nil)
	require.Equal(t, 200, code, got)
	assert.Equal(t, "void", got["state"], "a confirmed invoice is voided (BL25: locally first)")
	require.Eventually(t, func() bool { return fx.sales.claim(confirmed["id"].(string)).state == "released" },
		30*time.Second, 200*time.Millisecond, "then its claim is released")

	// void: voiding again answers the void invoice; it changes and prints no more
	void := fx.get(confirmed["id"].(string))
	code, got = fx.alice.do("POST", path(void, "void"), nil)
	require.Equal(t, 200, code, got)
	assert.Equal(t, "void", got["state"])
	assert.Equal(t, void["version"], got["version"], "nothing changed")
	code, got = change(void)
	refused(code, got, "only a draft changes; this invoice is void")
	code, got = fx.alice.do("POST", path(void, "print"), nil)
	refused(code, got, "only an issued invoice prints; this one is void")

	// issued, formalization pending: the source does not take the formalization now
	o2, _, _ := fx.order()
	_, pending := fx.draft(o2)
	fx.sales.set("formalize", "down")
	code, pending = fx.issue(pending["id"].(string))
	require.Equal(t, 200, code, pending)
	require.Equal(t, "pending", pending["formalization"])
	code, got = change(pending)
	refused(code, got, "only a draft changes; this invoice is issued")
	code, got = fx.alice.do("POST", path(pending, "void"), nil)
	refused(code, got, "an issued invoice is corrected, never voided")
	fx.sales.set("formalize", "")
	require.Eventually(t, func() bool { return fx.get(pending["id"].(string))["formalization"] == "done" }, 60*time.Second,
		300*time.Millisecond)

	// issued, formalized
	done := fx.get(pending["id"].(string))
	code, got = change(done)
	refused(code, got, "only a draft changes; this invoice is issued")
	code, got = fx.alice.do("POST", path(done, "void"), nil)
	refused(code, got, "an issued invoice is corrected, never voided")
	code, got = fx.issue(done["id"].(string))
	require.Equal(t, 200, code, got)
	assert.Equal(t, done["number"], got["number"], "issuing again answers the issued invoice")
}
