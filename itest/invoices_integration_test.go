//go:build integration

package itest

import (
	"regexp"
	"sync"

	"github.com/brickKit/be-sdk-go/v3/ids"
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// draftAndIssue: all the remaining claimed under the invoice's ID (BL02 on retry), a lowered draft, the issue that
// confirms, numbers and formalizes with the lowered quantities. Rules IN01, IN03, IN04, IN06, IN07.
func (fx *fixture) draftAndIssue() {
	t := fx.t
	o, goods, _ := fx.order()
	key := ids.New().String()
	code, v := fx.draft(o, key)
	require.Equal(t, 200, code, v)
	assert.Equal(t, "draft", v["state"])
	assert.Nil(t, v["number"])
	assert.Equal(t, map[string]any{"net": "390", "tax": "40.2", "gross": "430.2"}, v["totals"])
	code, again := fx.draft(o, key)
	require.Equal(t, 200, code, again)
	assert.Equal(t, v["id"], again["id"], "BL02: the same key drafts the same invoice")
	assert.Equal(t, 1, fx.sales.count("claim"), "the claim is taken once")

	code, low := fx.alice.do("PATCH", "/api/billing/invoices/"+v["id"].(string), map[string]any{"lines": []any{
		map[string]any{"order_line_id": goods, "quantity": "1", "amount": "120"}}})
	require.Equal(t, 200, code, low)
	assert.Equal(t, "15.6", at(low, "lines", 0, "tax"))
	assert.Equal(t, "24.6", at(low, "totals", "tax"))
	code, high := fx.alice.do("PATCH", "/api/billing/invoices/"+v["id"].(string), map[string]any{"lines": []any{
		map[string]any{"order_line_id": goods, "quantity": "3", "amount": "360"}}})
	assert.Equal(t, 409, code)
	assert.Equal(t, "ABOVE_CLAIM", high["reason"])

	fx.sales.set("confirm", "lost") // BL24: the confirm's answer is lost once; the same ID asks again
	code, issued := fx.issue(v["id"].(string))
	require.Equal(t, 200, code, issued)
	assert.Equal(t, "issued", issued["state"])
	assert.Regexp(t, regexp.MustCompile(`^INV-SH-`+fx.year+`-000001$`), issued["number"])
	assert.NotNil(t, issued["invoice_date"])
	assert.Equal(t, "done", issued["formalization"])
	c := fx.sales.claim(v["id"].(string))
	assert.Equal(t, "formalized", c.state)
	assert.Equal(t, "1", c.billed[0].GetMeasure().GetQuantity(), "the source records what was billed, not the claim")
	code, repeat := fx.issue(v["id"].(string))
	require.Equal(t, 200, code)
	assert.Equal(t, issued["number"], repeat["number"])
	fx.first = issued
}

// concurrentDrafts: BL01, two people draft the same order at once; one claim wins, the other draft is never made.
func (fx *fixture) concurrentDrafts() {
	o, _, _ := fx.order()
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i], _ = fx.draft(o) }()
	}
	wg.Wait()
	assert.ElementsMatch(fx.t, []int{200, 409}, codes)
	code, nothing := fx.draft(o)
	assert.Equal(fx.t, 409, code)
	assert.Equal(fx.t, "NOT_BILLABLE", nothing["reason"])
}

// voidAndExpiry: a voided draft releases its claim and takes no number (BL25); an expired claim voids the draft
// at issue (BL05, BL26); the next number follows on without a gap. Rules IN02, IN03, IN05, IN06.
func (fx *fixture) voidAndExpiry() {
	t := fx.t
	o, _, _ := fx.order()
	_, v := fx.draft(o)
	code, void := fx.alice.do("POST", "/api/billing/invoices/"+v["id"].(string)+"/void", nil)
	require.Equal(t, 200, code, void)
	assert.Equal(t, "void", void["state"])
	assert.Equal(t, "released", fx.sales.claim(v["id"].(string)).state)
	code, refused := fx.issue(v["id"].(string))
	assert.Equal(t, 409, code)
	assert.Equal(t, "STATE_CONFLICT", refused["reason"])

	o2, _, _ := fx.order()
	_, v2 := fx.draft(o2)
	fx.sales.expire(v2["id"].(string))
	code, expired := fx.issue(v2["id"].(string))
	assert.Equal(t, 409, code)
	assert.Equal(t, "CLAIM_EXPIRED", expired["reason"])
	assert.Equal(t, "void", fx.get(v2["id"].(string))["state"])
	assert.Nil(t, fx.get(v2["id"].(string))["number"])

	o3, _, _ := fx.order()
	_, v3 := fx.draft(o3)
	code, issued := fx.issue(v3["id"].(string))
	require.Equal(t, 200, code, issued)
	assert.Regexp(t, regexp.MustCompile(`-000002$`), issued["number"], "void drafts take no number")
}
