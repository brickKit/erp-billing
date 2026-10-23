package billing

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/brickKit/be-sdk-go/v3/audit"
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/identity"
	"github.com/brickKit/be-sdk-go/v3/money"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"
	billingv3 "github.com/brickKit/erp-billing/contract/v3/gen/go/be/erp/billing/v3"
)

// The component's own reasons (the SDK's catalog has the shared ones).
var (
	errVersion = errs.Define(ID, "VERSION_CONFLICT", errs.Aborted, 409, errs.RetryNo, "The record changed since that version")
	errState   = errs.Define(ID, "STATE_CONFLICT", errs.FailedPrecondition, 409, errs.RetryNo,
		"The invoice's state does not allow this")
	errNotBillable = errs.Define(ID, "NOT_BILLABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The source has nothing (or not that much) to bill")
	errClaimExpired = errs.Define(ID, "CLAIM_EXPIRED", errs.FailedPrecondition, 409, errs.RetryNo,
		"The claim expired before the invoice was issued: the draft is void, draft again")
	errLegalEntity = errs.Define(ID, "LEGAL_ENTITY_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The legal entity does not exist or is not active")
	errQuantity = errs.Define(ID, "ABOVE_CLAIM", errs.FailedPrecondition, 409, errs.RetryNo,
		"A draft's quantities and amounts only go down from what was claimed")
)

var (
	uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	dateForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Work items (protocol 01): formalizing at the source and releasing a void invoice's claim are retried until the
// source answers; an issue that stopped after confirming its claim is numbered by the continue item.
const (
	jobFormalize = "formalize-invoice"
	jobRelease   = "release-claim"
	jobContinue  = "continue-issue"
)

// Module is the billing component.
type Module struct {
	billingv3.UnimplementedInvoiceServiceServer
	rt *runtime.Runtime
}

// New builds the module from its runtime.
func New(rt *runtime.Runtime) (*Module, error) { return &Module{rt: rt}, nil }

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler, orchestration bool) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h, Orchestration: orchestration})
	}
	add("GET", "/api/billing/invoices", OpRead, m.listInvoices, false)
	add("POST", "/api/billing/invoices", OpDraft, m.draftInvoice, true)
	add("GET", "/api/billing/invoices/:id", OpRead, m.getInvoice, false)
	add("PATCH", "/api/billing/invoices/:id", OpDraft, m.updateInvoice, false)
	add("POST", "/api/billing/invoices/:id/issue", OpIssue, m.issueInvoice, true)
	add("POST", "/api/billing/invoices/:id/void", OpDraft, m.voidInvoice, true)
	add("POST", "/api/billing/invoices/:id/print", OpPrint, m.printInvoice, true)
	// in the contract, not in this version (D75)
	add("POST", "/api/billing/invoices/:id/corrections", OpIssue, func(c *httpx.Context) error {
		inv, err := m.invoice(c.Ctx(), c.Param("id"))
		if err != nil {
			return err
		}
		if _, _, err := check(c, c.Operation(), inv.LegalEntityID); err != nil {
			return err
		}
		return errs.New(errs.ReasonCapabilityUnsupported, "corrections are not in this version of erp/billing")
	}, false)
	return nil
}

// Services registers the system surface: the invoice read for any component.
func (m *Module) Services(s *grpcx.Services) error {
	s.Register(&billingv3.InvoiceService_ServiceDesc, m, map[string]grpcx.Method{
		"Read": {Operation: OpSysRead, Callers: []string{grpcx.AnyComponent}}})
	return nil
}

// Jobs declares the background work (protocol 01).
func (m *Module) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: jobFormalize, Kind: work.Queue, Effect: work.IdempotentExternal, MaxAttempts: work.Unlimited,
		Handle: m.formalizeJob})
	j.Add(work.Job{Name: jobRelease, Kind: work.Queue, Effect: work.IdempotentExternal, MaxAttempts: work.Unlimited,
		Handle: m.releaseJob})
	j.Add(work.Job{Name: jobContinue, Kind: work.Queue, Effect: work.IdempotentExternal, MaxAttempts: work.Unlimited,
		Handle: m.continueJob})
	j.Add(work.Job{Name: "purge-draft-keys", Kind: work.Singleton, Effect: work.Local, Interval: time.Hour,
		MaxAttempts: work.Unlimited, Handle: m.purgeDraftKeys})
	return nil
}

// check decides op on an invoice of the legal entity and returns the context to work in and the acting principal.
func check(c *httpx.Context, op, legalEntity string) (context.Context, string, error) {
	s, _ := sdkauthz.From(c.Ctx())
	ctx, _, err := s.Check(c.Ctx(), op, sdkauthz.Facts{DimLegalEntity: sdkauthz.Value(legalEntity)})
	if err != nil {
		return ctx, "", err
	}
	u, _ := identity.UserFrom(ctx)
	return ctx, u.Subject, nil
}

func invalid(field, why string) error {
	return errs.New(errs.ReasonRequestInvalid, why).WithViolations(errs.Violation{Field: field, Reason: "INVALID", Description: why})
}

func notFound(what string) error { return errs.New(errs.ReasonNotFound, what+" not found") }

// decimal checks a non-negative decimal string of at most scale places and returns its canonical form.
func decimal(field, v string, scale int32) (string, error) {
	d, err := money.Parse(v)
	if err != nil || d.Sign() < 0 || -d.Exponent > scale {
		return "", invalid(field, field+" is a non-negative decimal string with at most "+strconv.Itoa(int(scale))+" places")
	}
	d.Reduce(d)
	return d.Text('f'), nil
}

// change records the business change with the record's new state (protocol 03), in the transaction that made it.
func (m *Module) change(ctx context.Context, tx db.Tx, actor, op, id string, after any) error {
	_, err := m.rt.Audit.Write(ctx, tx, audit.Record{Category: audit.BusinessChange, Operation: op, Outcome: audit.Succeeded,
		Actor: actor, Target: map[string]any{"type": Invoices, "id": id}, Changes: map[string]any{"after": after}})
	return err
}

// emit writes an event into the outbox, in the transaction that changed the record (protocol 02, S2).
func (m *Module) emit(ctx context.Context, tx db.Tx, typ, id string, version int64, legalEntity string, data any) error {
	_, err := m.rt.Events.Emit(ctx, tx, Stream, events.Event{Type: typ, RecordID: id, Version: version,
		Projection: events.Full, LegalEntity: legalEntity, Data: data})
	return err
}

// purgeDraftKeys forgets the invoice IDs of draft requests old enough that no retry comes any more.
func (m *Module) purgeDraftKeys(ctx context.Context, _ *work.Run) error {
	return m.rt.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM draft_key WHERE created_at < now() - interval '7 days'`)
		return err
	})
}
