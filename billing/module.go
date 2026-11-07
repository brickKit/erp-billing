package billing

import (
	"context"
	"time"

	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"
	billingv3 "github.com/brickKit/erp-billing/contract/v3/gen/go/be/erp/billing/v3"
	core "github.com/brickKit/erp-billing/v3/internal/core"
	invoices "github.com/brickKit/erp-billing/v3/internal/invoices"
	issue "github.com/brickKit/erp-billing/v3/internal/issue"
	print "github.com/brickKit/erp-billing/v3/internal/print"
)

// Module is the billing component as the launcher and the shells run it: it only assembles the business modules under
// internal/ (their map is internal/AGENTS.md); the gRPC services answer through them (services.go).
type Module struct {
	billingv3.UnimplementedInvoiceServiceServer
	b *core.Base
}

// New builds the module from its runtime.
func New(rt *runtime.Runtime) (*Module, error) { return &Module{b: &core.Base{RT: rt}}, nil }

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler, orchestration bool) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h, Orchestration: orchestration})
	}
	h := m.handler
	add("GET", "/api/billing/invoices", core.OpRead, h(invoices.ListInvoices), false)
	add("POST", "/api/billing/invoices", core.OpDraft, h(invoices.DraftInvoice), true)
	add("GET", "/api/billing/invoices/:id", core.OpRead, h(invoices.GetInvoice), false)
	add("PATCH", "/api/billing/invoices/:id", core.OpDraft, h(invoices.UpdateInvoice), false)
	add("POST", "/api/billing/invoices/:id/issue", core.OpIssue, h(issue.IssueInvoice), true)
	add("POST", "/api/billing/invoices/:id/void", core.OpDraft, h(invoices.VoidInvoice), true)
	add("POST", "/api/billing/invoices/:id/print", core.OpPrint, h(print.PrintInvoice), true)
	// in the contract, not in this version (D75)
	add("POST", "/api/billing/invoices/:id/corrections", core.OpIssue, h(invoices.Corrections), false)
	return nil
}

// Services registers the system surface: any component reads invoices.
func (m *Module) Services(s *grpcx.Services) error {
	s.Register(&billingv3.InvoiceService_ServiceDesc, m, map[string]grpcx.Method{
		"Read": {Operation: core.OpSysRead, Callers: []string{grpcx.AnyComponent}}})
	return nil
}

// Jobs declares the background work (protocol 01): formalizing an issued invoice's claim, releasing a voided draft's
// claim, continuing an issue that stopped half-way, and forgetting old draft keys.
func (m *Module) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: core.JobFormalize, Kind: work.Queue, Effect: work.IdempotentExternal, MaxAttempts: work.Unlimited,
		Handle: m.job(issue.FormalizeJob)})
	j.Add(work.Job{Name: core.JobRelease, Kind: work.Queue, Effect: work.IdempotentExternal, MaxAttempts: work.Unlimited,
		Handle: m.job(invoices.ReleaseJob)})
	j.Add(work.Job{Name: core.JobContinue, Kind: work.Queue, Effect: work.IdempotentExternal, MaxAttempts: work.Unlimited,
		Handle: m.job(issue.ContinueJob)})
	j.Add(work.Job{Name: "purge-draft-keys", Kind: work.Singleton, Effect: work.Local, Interval: time.Hour,
		MaxAttempts: work.Unlimited, Handle: m.job(invoices.PurgeDraftKeys)})
	return nil
}

// handler and job adapt a module's function, which takes the shared base first, to what the SDK registers.
func (m *Module) handler(f func(*core.Base, *httpx.Context) error) httpx.Handler {
	return func(c *httpx.Context) error { return f(m.b, c) }
}

func (m *Module) job(f func(*core.Base, context.Context, *work.Run) error) func(context.Context, *work.Run) error {
	return func(ctx context.Context, r *work.Run) error { return f(m.b, ctx, r) }
}
