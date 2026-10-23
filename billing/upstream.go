package billing

import (
	"context"
	"encoding/json"

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

func (m *Module) conn(id string) (*grpc.ClientConn, error) {
	c, ok := m.rt.GRPC(id)
	if !ok {
		return nil, errs.New(errs.ReasonDependencyUnavailable, id+" is not reachable")
	}
	return c, nil
}

// legalEntity is what billing needs of a legal entity.
type legalEntity struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Timezone string `json:"timezone"`
}

// legalEntityOf reads a legal entity; usable says it must be active.
func (m *Module) legalEntityOf(ctx context.Context, id string, usable bool) (legalEntity, error) {
	c, err := m.conn(Org)
	if err != nil {
		return legalEntity{}, err
	}
	res, err := orgv3.NewSourceServiceClient(c).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_LEGAL_ENTITY, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return legalEntity{}, err
	}
	var le legalEntity
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return le, errLegalEntity.New("no legal entity " + id)
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &le); err != nil {
		return le, err
	}
	if usable && le.State != "active" {
		return le, errLegalEntity.New("the legal entity is " + le.State)
	}
	return le, nil
}

// currencyScale is a currency's minor unit.
func (m *Module) currencyScale(ctx context.Context, code string) (int32, error) {
	c, err := m.conn(Currency)
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

// order is what billing takes of a sales order (erp.sales_order.changed.v1).
type order struct {
	ID            string   `json:"id"`
	Number        string   `json:"number"`
	LegalEntityID string   `json:"legal_entity_id"`
	Customer      Customer `json:"customer"`
	Currency      string   `json:"currency"`
	State         string   `json:"state"`
	ExchangeRate  *Rate    `json:"exchange_rate"`
	Lines         []struct {
		ID        string  `json:"id"`
		Product   Product `json:"product"`
		Unit      string  `json:"unit"`
		UnitPrice string  `json:"unit_price"`
		TaxRate   string  `json:"tax_rate"`
	} `json:"lines"`
}

// orderOf reads a sales order through erp/sales's system read; NOT_FOUND when it has none.
func (m *Module) orderOf(ctx context.Context, id string) (order, error) {
	c, err := m.conn(Sales)
	if err != nil {
		return order{}, err
	}
	res, err := salesv3.NewOrderServiceClient(c).Read(ctx, &salesv3.ReadRequest{Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return order{}, err
	}
	var o order
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != salesv3.ReadState_READ_STATE_EXISTS {
		return o, notFound("order")
	}
	return o, json.Unmarshal(res.GetResults()[0].GetData(), &o)
}

// source is erp/sales's billing source (protocol 05); every command is idempotent by the claim ID.
func (m *Module) source() (salesv3.BillingSourceServiceClient, error) {
	c, err := m.conn(Sales)
	if err != nil {
		return nil, err
	}
	return salesv3.NewBillingSourceServiceClient(c), nil
}

// render asks infra/print for a PDF of the invoice template at version (0: the current one).
func (m *Module) render(ctx context.Context, version int64, data []byte, copy bool) (*printv3.RenderResponse, error) {
	c, err := m.conn(Print)
	if err != nil {
		return nil, err
	}
	return printv3.NewRenderServiceClient(c).Render(ctx, &printv3.RenderRequest{Template: Template, Version: version,
		Data: data, Format: printv3.Format_FORMAT_PDF, Copy: copy}, calls.Idempotent())
}
