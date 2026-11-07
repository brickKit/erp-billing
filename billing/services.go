package billing

import (
	"context"

	billingv3 "github.com/brickKit/erp-billing/contract/v3/gen/go/be/erp/billing/v3"
	invoices "github.com/brickKit/erp-billing/v3/internal/invoices"
)

// Read is InvoiceService.Read, answered by internal/invoices.
func (m *Module) Read(ctx context.Context, in *billingv3.ReadRequest) (*billingv3.ReadResponse, error) {
	return invoices.Read(m.b, ctx, in)
}
