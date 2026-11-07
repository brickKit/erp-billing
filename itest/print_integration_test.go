//go:build integration

package itest

import (
	"encoding/json"
	"strings"

	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// printing: the first render records the template version and view model; a later one uses them, marked as a copy,
// even after the template changed.
func (fx *fixture) printing() {
	t := fx.t
	id := fx.first["id"].(string)
	o, _, _ := fx.order()
	_, draft := fx.draft(o)
	code, _ := fx.alice.do("POST", "/api/billing/invoices/"+draft["id"].(string)+"/print", nil)
	assert.Equal(t, 409, code, "a draft does not print")
	fx.print.mu.Lock()
	fx.print.down = true
	fx.print.mu.Unlock()
	code, _ = fx.alice.do("POST", "/api/billing/invoices/"+id+"/print", nil)
	assert.Equal(t, 503, code)
	assert.EqualValues(t, 0, at(fx.get(id), "print", "count"), "a failed first render records nothing")
	fx.print.mu.Lock()
	fx.print.down = false
	fx.print.mu.Unlock()

	first := fx.raw("POST", "/api/billing/invoices/"+id+"/print")
	assert.Contains(t, first, "invoice v3 copy=false")
	v := fx.get(id)
	assert.EqualValues(t, 3, at(v, "print", "template_version"))
	assert.EqualValues(t, 1, at(v, "print", "count"))
	var model map[string]any
	fx.print.mu.Lock()
	require.NoError(t, json.Unmarshal(fx.print.data[fx.print.n], &model))
	fx.print.current = 4 // the template changes
	fx.print.mu.Unlock()
	assert.Equal(t, "人民币贰佰玖拾肆元陆角整", at(model, "totals", "gross_in_words"), "120 + 15.6 + 150 + 9")
	assert.Equal(t, "13%", at(model, "lines", 0, "tax_rate"))
	second := fx.raw("POST", "/api/billing/invoices/"+id+"/print")
	assert.Contains(t, second, "invoice v3 copy=true", "the snapshot's version, marked as a copy")
	assert.Equal(t, first[strings.LastIndex(first, " "):], second[strings.LastIndex(second, " "):], "the same data")
	assert.EqualValues(t, 2, at(fx.get(id), "print", "count"))
}
