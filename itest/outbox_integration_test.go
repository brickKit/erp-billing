//go:build integration

package itest

import (
	"bytes"

	core "github.com/brickKit/erp-billing/v3/internal/core"
	"github.com/santhosh-tekuri/jsonschema/v6"
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
)

// outbox: every event fits its schema; every issued invoice has exactly one "invoiced" fact.
func (fx *fixture) outbox() {
	t := fx.t
	rows, err := fx.admin.Query(fx.ctx, "SELECT type, payload FROM "+itSchema+".be_outbox ORDER BY ord")
	require.NoError(t, err)
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var typ string
		var payload []byte
		require.NoError(t, rows.Scan(&typ, &payload))
		counts[typ]++
		s, err := jsonschema.NewCompiler().Compile("../contract/events/" + typ + ".schema.json")
		require.NoError(t, err, typ)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		require.NoError(t, err)
		assert.NoError(t, s.Validate(doc), "%s: %s", typ, payload)
	}
	var issued int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".invoice WHERE state = 'issued'").Scan(&issued))
	assert.Equal(t, issued, counts[core.InvoiceIssued])
	assert.NotZero(t, counts[core.InvoiceChanged])
}
