//go:build integration

package billing

// The stand-ins of the integration test, each speaking its published contract: mdm/org, mdm/currency, erp/sales (the
// order read and the billing source of protocol 05, deciding claims on its ledger as the real source does) and
// infra/print (a deterministic renderer). The source can be told to lose an answer once, to let a claim expire or to
// be down, so the test can drive every row of the invoice's tables.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"

	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	printv3 "github.com/brickKit/infra-print/contract/v3/gen/go/be/infra/print/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var down = status.Error(codes.Unavailable, "the stand-in is down")

// --- mdm/org and mdm/currency ---

type orgStandIn struct {
	orgv3.UnimplementedSourceServiceServer
	mu  sync.Mutex
	les map[string]map[string]any
}

func (o *orgStandIn) set(id, state string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.les[id]["state"] = state
}

func (o *orgStandIn) Read(_ context.Context, in *orgv3.ReadRequest) (*orgv3.ReadResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := &orgv3.ReadResponse{Stamp: &orgv3.Stamp{}}
	for _, id := range in.GetIds() {
		r := &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_PURGED}
		if v, ok := o.les[id]; ok {
			data, _ := json.Marshal(v)
			r = &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

type currencyStandIn struct {
	currencyv3.UnimplementedSourceServiceServer
}

func (currencyStandIn) Read(_ context.Context, in *currencyv3.ReadRequest) (*currencyv3.ReadResponse, error) {
	out := &currencyv3.ReadResponse{Stamp: &currencyv3.Stamp{}}
	for _, code := range in.GetIds() {
		data, _ := json.Marshal(map[string]any{"code": code, "minor_unit": 2, "active": true})
		out.Results = append(out.Results, &currencyv3.ReadResult{Id: code, State: currencyv3.ReadState_READ_STATE_EXISTS,
			Version: 1, Data: data})
	}
	return out, nil
}

// --- erp/sales: orders and their billable ledger ---

type ledgerLine struct{ billable, claimed, billed [2]*big.Rat } // quantity, amount

type claimIT struct {
	order, state string
	lines        []*salesv3.ClaimLine
	billed       []*salesv3.ClaimLine
}

type salesStandIn struct {
	salesv3.UnimplementedOrderServiceServer
	salesv3.UnimplementedBillingSourceServiceServer
	mu      sync.Mutex
	orders  map[string]map[string]any
	ledger  map[string]*ledgerLine // order line → ledger
	lineOf  map[string]string      // order line → order
	claims  map[string]*claimIT
	mode    map[string]string // command → "lost" (once), "down"
	expired map[string]bool   // claims past their period
	calls   map[string]int    // command → calls that took effect
}

func rat(s string) *big.Rat { r, _ := new(big.Rat).SetString(s); return r }

func text(r *big.Rat) string {
	s := r.FloatString(6)
	for len(s) > 1 && (s[len(s)-1] == '0' || s[len(s)-1] == '.') {
		if s[len(s)-1] == '.' {
			return s[:len(s)-1]
		}
		s = s[:len(s)-1]
	}
	return s
}

// addOrder adds a confirmed order; each line is ID, product, unit price, tax rate, billable quantity and amount.
func (s *salesStandIn) addOrder(id, le, customer string, lines [][6]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ls []map[string]any
	for _, l := range lines {
		q, a := l[4], l[5]
		ls = append(ls, map[string]any{"id": l[0], "product": map[string]any{"id": l[1], "code": "P-" + l[1][:4], "name": "产品"},
			"unit": "EA", "unit_price": l[2], "tax_rate": l[3]})
		s.ledger[l[0]] = &ledgerLine{billable: [2]*big.Rat{rat(q), rat(a)}, claimed: [2]*big.Rat{rat("0"), rat("0")},
			billed: [2]*big.Rat{rat("0"), rat("0")}}
		s.lineOf[l[0]] = id
	}
	s.orders[id] = map[string]any{"id": id, "number": "SO-SH-2026-" + id[:6], "legal_entity_id": le,
		"customer": map[string]any{"partner_id": customer, "name": "上海客户", "tax_id": "91310000MA1"}, "currency": "CNY",
		"state": "confirmed", "exchange_rate": nil, "lines": ls}
}

func (s *salesStandIn) set(cmd, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode[cmd] = mode
}

func (s *salesStandIn) expire(claim string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expired[claim] = true
}

func (s *salesStandIn) count(cmd string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[cmd]
}

func (s *salesStandIn) claim(id string) *claimIT {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims[id]
}

// gate applies a mode: "down" answers nothing; "lost" lets the command take effect and loses the answer once.
func (s *salesStandIn) gate(cmd string) (lost bool, err error) {
	switch s.mode[cmd] {
	case "down":
		return false, down
	case "lost":
		delete(s.mode, cmd)
		return true, nil
	}
	return false, nil
}

func (s *salesStandIn) Read(_ context.Context, in *salesv3.ReadRequest) (*salesv3.ReadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &salesv3.ReadResponse{}
	for _, id := range in.GetIds() {
		r := &salesv3.ReadResult{Id: id, State: salesv3.ReadState_READ_STATE_NOT_FOUND}
		if o, ok := s.orders[id]; ok {
			data, _ := json.Marshal(o)
			r = &salesv3.ReadResult{Id: id, State: salesv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

func (s *salesStandIn) Ledger(_ context.Context, in *salesv3.LedgerRequest) (*salesv3.LedgerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &salesv3.LedgerResponse{OrderId: in.GetOrderId(), OrderState: "confirmed"}
	for _, l := range s.orders[in.GetOrderId()]["lines"].([]map[string]any) {
		g := s.ledger[l["id"].(string)]
		m := func(v [2]*big.Rat) *salesv3.Measure {
			return &salesv3.Measure{Quantity: text(v[0]), Amount: text(v[1])}
		}
		out.Lines = append(out.Lines, &salesv3.LedgerLine{OrderLineId: l["id"].(string), Billable: m(g.billable),
			Claimed: m(g.claimed), Billed: m(g.billed)})
	}
	return out, nil
}

func (c *claimIT) proto(id string) *salesv3.Claim {
	return &salesv3.Claim{ClaimId: id, OrderId: c.order, Lines: c.lines, Billed: c.billed,
		State: salesv3.ClaimState(salesv3.ClaimState_value["CLAIM_STATE_"+map[string]string{"pending": "PENDING",
			"confirmed": "CONFIRMED", "formalized": "FORMALIZED", "expired": "EXPIRED", "released": "RELEASED"}[c.state]])}
}

func (s *salesStandIn) giveBack(c *claimIT) {
	for _, l := range c.lines {
		g := s.ledger[l.GetOrderLineId()]
		g.claimed[0].Sub(g.claimed[0], rat(l.GetMeasure().GetQuantity()))
		g.claimed[1].Sub(g.claimed[1], rat(l.GetMeasure().GetAmount()))
	}
}

// Claim takes all lines or none (BL01); the same ID and content answer the first claim (BL02).
func (s *salesStandIn) Claim(_ context.Context, in *salesv3.ClaimRequest) (*salesv3.ClaimResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lost, err := s.gate("claim")
	if err != nil {
		return nil, err
	}
	if c, ok := s.claims[in.GetClaimId()]; ok {
		return &salesv3.ClaimResponse{Claimed: true, Claim: c.proto(in.GetClaimId())}, nil
	}
	var problems []*salesv3.ClaimProblem
	for _, l := range in.GetLines() {
		g, ok := s.ledger[l.GetOrderLineId()]
		if !ok || s.lineOf[l.GetOrderLineId()] != in.GetOrderId() {
			problems = append(problems, &salesv3.ClaimProblem{OrderLineId: l.GetOrderLineId(), Reason: "line_not_found"})
			continue
		}
		for i, v := range []string{l.GetMeasure().GetQuantity(), l.GetMeasure().GetAmount()} {
			sum := new(big.Rat).Add(g.claimed[i], g.billed[i])
			if sum.Add(sum, rat(v)).Cmp(g.billable[i]) > 0 {
				problems = append(problems, &salesv3.ClaimProblem{OrderLineId: l.GetOrderLineId(), Reason: "exceeds_billable"})
				break
			}
		}
	}
	if len(problems) > 0 {
		return &salesv3.ClaimResponse{Problems: problems}, nil
	}
	for _, l := range in.GetLines() {
		g := s.ledger[l.GetOrderLineId()]
		g.claimed[0].Add(g.claimed[0], rat(l.GetMeasure().GetQuantity()))
		g.claimed[1].Add(g.claimed[1], rat(l.GetMeasure().GetAmount()))
	}
	c := &claimIT{order: in.GetOrderId(), state: "pending", lines: in.GetLines()}
	s.claims[in.GetClaimId()] = c
	s.calls["claim"]++
	if lost {
		return nil, down
	}
	return &salesv3.ClaimResponse{Claimed: true, Claim: c.proto(in.GetClaimId())}, nil
}

// Confirm: an expired claim is refused and given back (BL05, BL26); again answers the same (BL24).
func (s *salesStandIn) Confirm(_ context.Context, in *salesv3.ConfirmRequest) (*salesv3.ConfirmResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lost, err := s.gate("confirm")
	if err != nil {
		return nil, err
	}
	c, ok := s.claims[in.GetClaimId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no claim")
	}
	if c.state == "pending" && s.expired[in.GetClaimId()] {
		c.state = "expired"
		s.giveBack(c)
	} else if c.state == "pending" {
		c.state = "confirmed"
		s.calls["confirm"]++
	}
	if lost {
		return nil, down
	}
	return &salesv3.ConfirmResponse{Confirmed: c.state == "confirmed" || c.state == "formalized", Claim: c.proto(in.GetClaimId())}, nil
}

func (s *salesStandIn) Formalize(_ context.Context, in *salesv3.FormalizeRequest) (*salesv3.FormalizeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.gate("formalize"); err != nil {
		return nil, err
	}
	c, ok := s.claims[in.GetClaimId()]
	if !ok || (c.state != "confirmed" && c.state != "formalized") {
		return nil, status.Error(codes.FailedPrecondition, "CLAIM_STATE")
	}
	if c.state == "confirmed" {
		s.giveBack(c)
		for _, b := range in.GetBilled() {
			g := s.ledger[b.GetOrderLineId()]
			g.billed[0].Add(g.billed[0], rat(b.GetMeasure().GetQuantity()))
			g.billed[1].Add(g.billed[1], rat(b.GetMeasure().GetAmount()))
		}
		c.state, c.billed = "formalized", in.GetBilled()
		s.calls["formalize"]++
	}
	return &salesv3.FormalizeResponse{Claim: c.proto(in.GetClaimId())}, nil
}

func (s *salesStandIn) Release(_ context.Context, in *salesv3.ReleaseRequest) (*salesv3.ReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.gate("release"); err != nil {
		return nil, err
	}
	c, ok := s.claims[in.GetClaimId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no claim")
	}
	if c.state == "pending" || c.state == "confirmed" {
		s.giveBack(c)
		c.state = "released"
		s.calls["release"]++
	}
	return &salesv3.ReleaseResponse{Claim: c.proto(in.GetClaimId())}, nil
}

// --- infra/print: the same version and data give the same bytes ---

type printStandIn struct {
	printv3.UnimplementedRenderServiceServer
	mu      sync.Mutex
	current int64
	down    bool
	data    map[int64][]byte // the data each render got, by call
	n       int64
}

func (p *printStandIn) Render(_ context.Context, in *printv3.RenderRequest) (*printv3.RenderResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		return nil, down
	}
	v := in.GetVersion()
	if v == 0 {
		v = p.current
	}
	p.n++
	p.data[p.n] = in.GetData()
	sum := sha256.Sum256(in.GetData())
	content := fmt.Sprintf("%%PDF %s v%d copy=%t %s", in.GetTemplate(), v, in.GetCopy(), hex.EncodeToString(sum[:8]))
	return &printv3.RenderResponse{Content: []byte(content), MediaType: "application/pdf", Template: in.GetTemplate(),
		Version: v, VersionDigest: fmt.Sprintf("digest-%d", v)}, nil
}
