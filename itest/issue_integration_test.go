//go:build integration

package itest

import (
	"time"

	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// recovery: BL04, the source does not take the formalize and the work item sends it again; an issue that stopped
// after confirming (the legal entity was suspended) is numbered by its work item once the legal entity is back.
func (fx *fixture) recovery() {
	t := fx.t
	o, _, _ := fx.order()
	_, v := fx.draft(o)
	fx.sales.set("formalize", "down")
	code, issued := fx.issue(v["id"].(string))
	require.Equal(t, 200, code, issued)
	assert.Equal(t, "issued", issued["state"])
	assert.Equal(t, "pending", issued["formalization"])
	fx.sales.set("formalize", "")
	require.Eventually(t, func() bool { return fx.get(v["id"].(string))["formalization"] == "done" }, 60*time.Second,
		300*time.Millisecond)

	o2, _, _ := fx.order()
	_, v2 := fx.draft(o2)
	fx.org.set(fx.le, "suspended")
	code, stopped := fx.issue(v2["id"].(string))
	assert.Equal(t, 409, code)
	assert.Equal(t, "LEGAL_ENTITY_NOT_USABLE", stopped["reason"])
	assert.Equal(t, "confirmed", fx.get(v2["id"].(string))["state"], "BL21: confirmed, never expires, not numbered")
	fx.org.set(fx.le, "active")
	_, err := fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".invoice SET updated_at = now() - interval '2 minutes' WHERE id = $1",
		v2["id"])
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		v := fx.get(v2["id"].(string))
		return v["state"] == "issued" && v["formalization"] == "done"
	}, 60*time.Second, 300*time.Millisecond, "the continue item numbers it")
}
