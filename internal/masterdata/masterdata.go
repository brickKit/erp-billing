package masterdata

import (
	"context"
	"encoding/json"

	core "github.com/brickKit/erp-billing/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/errs"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	printv3 "github.com/brickKit/infra-print/contract/v3/gen/go/be/infra/print/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	"google.golang.org/grpc"
)

// Every call here runs outside a transaction (protocol P4.20). Unreachable is never "does not exist" (protocol 07):
// it answers DEPENDENCY_UNAVAILABLE.

func conn(m *core.Base, id string) (*grpc.ClientConn, error) {
	c, ok := m.RT.GRPC(id)
	if !ok {
		return nil, errs.New(errs.ReasonDependencyUnavailable, id+" is not reachable")
	}
	return c, nil
}

// LegalEntity is what billing needs of a legal entity.
type LegalEntity struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Timezone string `json:"timezone"`
}

// LegalEntityOf reads a legal entity; usable says it must be active.
func LegalEntityOf(m *core.Base, ctx context.Context, id string, usable bool) (LegalEntity, error) {
	c, err := conn(m, core.Org)
	if err != nil {
		return LegalEntity{}, err
	}
	res, err := orgv3.NewSourceServiceClient(c).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_LEGAL_ENTITY, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return LegalEntity{}, err
	}
	var le LegalEntity
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return le, core.ErrLegalEntity.New("no legal entity " + id)
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &le); err != nil {
		return le, err
	}
	if usable && le.State != "active" {
		return le, core.ErrLegalEntity.New("the legal entity is " + le.State)
	}
	return le, nil
}

// CurrencyScale is a currency's minor unit.
func CurrencyScale(m *core.Base, ctx context.Context, code string) (int32, error) {
	c, err := conn(m, core.Currency)
	if err != nil {
		return 0, err
	}
	res, err := currencyv3.NewSourceServiceClient(c).Read(ctx, &currencyv3.ReadRequest{
		Aggregate: currencyv3.Aggregate_AGGREGATE_CURRENCY, Ids: []string{code}}, calls.Idempotent())
	if err != nil {
		return 0, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != currencyv3.ReadState_READ_STATE_EXISTS {
		return 0, errs.New(errs.ReasonInternal, "no currency "+code)
	}
	var cur struct {
		MinorUnit int32 `json:"minor_unit"`
	}
	return cur.MinorUnit, json.Unmarshal(res.GetResults()[0].GetData(), &cur)
}

// Order is what billing takes of a sales order (erp.sales_order.changed.v1).
type Order struct {
	ID            string        `json:"id"`
	Number        string        `json:"number"`
	LegalEntityID string        `json:"legal_entity_id"`
	Customer      core.Customer `json:"customer"`
	Currency      string        `json:"currency"`
	State         string        `json:"state"`
	ExchangeRate  *core.Rate    `json:"exchange_rate"`
	Lines         []struct {
		ID        string       `json:"id"`
		Product   core.Product `json:"product"`
		Unit      string       `json:"unit"`
		UnitPrice string       `json:"unit_price"`
		TaxRate   string       `json:"tax_rate"`
	} `json:"lines"`
}

// OrderOf reads a sales order through erp/sales's system read; NOT_FOUND when it has none.
func OrderOf(m *core.Base, ctx context.Context, id string) (Order, error) {
	c, err := conn(m, core.Sales)
	if err != nil {
		return Order{}, err
	}
	res, err := salesv3.NewOrderServiceClient(c).Read(ctx, &salesv3.ReadRequest{Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return Order{}, err
	}
	var o Order
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != salesv3.ReadState_READ_STATE_EXISTS {
		return o, core.NotFound("order")
	}
	return o, json.Unmarshal(res.GetResults()[0].GetData(), &o)
}

// Source is erp/sales's billing source (protocol 05); every command is idempotent by the claim ID.
func Source(m *core.Base) (salesv3.BillingSourceServiceClient, error) {
	c, err := conn(m, core.Sales)
	if err != nil {
		return nil, err
	}
	return salesv3.NewBillingSourceServiceClient(c), nil
}

// Render asks infra/print for a PDF of the invoice template at version (0: the current one).
func Render(m *core.Base, ctx context.Context, version int64, data []byte, copy bool) (*printv3.RenderResponse, error) {
	c, err := conn(m, core.Print)
	if err != nil {
		return nil, err
	}
	return printv3.NewRenderServiceClient(c).Render(ctx, &printv3.RenderRequest{Template: core.Template, Version: version,
		Data: data, Format: printv3.Format_FORMAT_PDF, Copy: copy}, calls.Idempotent())
}
