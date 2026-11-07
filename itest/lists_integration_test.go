//go:build integration

package itest

import (
	"net/url"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	billingv3 "github.com/brickKit/erp-billing/contract/v3/gen/go/be/erp/billing/v3"
	core "github.com/brickKit/erp-billing/v3/internal/core"
	assert "github.com/stretchr/testify/assert"
	require "github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// listsAndScope: the keyword finds an invoice by number, the date filters, the system read, and a reader of
// another legal entity sees nothing.
func (fx *fixture) listsAndScope() {
	t := fx.t
	number := fx.first["number"].(string)
	code, page := fx.alice.do("GET", "/api/billing/invoices?q="+url.QueryEscape(number), nil)
	require.Equal(t, 200, code, page)
	require.Len(t, page["invoices"], 1)
	assert.Equal(t, fx.first["id"], at(page, "invoices", 0, "id"))
	_, page = fx.alice.do("GET", "/api/billing/invoices?state=issued&from="+fx.first["invoice_date"].(string), nil)
	assert.Len(t, page["invoices"], 4)

	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(t, core.Sales).ClientConfig(core.ID))))
	require.NoError(t, err)
	defer conn.Close()
	read, err := billingv3.NewInvoiceServiceClient(conn).Read(fx.ctx, &billingv3.ReadRequest{Ids: []string{fx.first["id"].(string)}})
	require.NoError(t, err)
	assert.Contains(t, string(read.GetResults()[0].GetData()), number)

	other := ids.New().String()
	fx.w.Grant(sectest.Bob, sdkauthz.Segment{ResourceType: core.Invoices, Operation: core.OpRead,
		ReadFields: []string{"id", "number", "legal_entity_id", "customer", "currency", "exchange_rate", "source", "state",
			"formalization", "invoice_date", "lines", "totals", "print", "version", "created_at", "updated_at"},
		Atoms: map[string]sdkauthz.Atom{core.DimLegalEntity: {Kind: "in", Values: []string{other}}},
		Valid: sdkauthz.Window{From: "2026-01-01T00:00:00Z"}})
	bob := client{t, fx.w.Bearer(sectest.Bob)}
	// without the grant the read is refused (403); with it, an invoice of another legal entity is not visible (404)
	require.Eventually(t, func() bool {
		code, _ := bob.do("GET", "/api/billing/invoices/"+fx.first["id"].(string), nil)
		return code == 404
	}, 30*time.Second, 200*time.Millisecond)
	_, page = bob.do("GET", "/api/billing/invoices", nil)
	assert.Empty(t, page["invoices"])
}
