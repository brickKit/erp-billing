package core

import (
	"context"
	"maps"
	"regexp"
	"strconv"

	"github.com/brickKit/be-sdk-go/v3/audit"
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/identity"
	"github.com/brickKit/be-sdk-go/v3/money"
)

// The component's own reasons (the SDK's catalog has the shared ones).
var (
	ErrVersion = errs.Define(ID, "VERSION_CONFLICT", errs.Aborted, 409, errs.RetryNo, "The record changed since that version")
	ErrState   = errs.Define(ID, "STATE_CONFLICT", errs.FailedPrecondition, 409, errs.RetryNo,
		"The invoice's state does not allow this")
	ErrNotBillable = errs.Define(ID, "NOT_BILLABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The source has nothing (or not that much) to bill")
	ErrClaimExpired = errs.Define(ID, "CLAIM_EXPIRED", errs.FailedPrecondition, 409, errs.RetryNo,
		"The claim expired before the invoice was issued: the draft is void, draft again")
	ErrLegalEntity = errs.Define(ID, "LEGAL_ENTITY_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The legal entity does not exist or is not active")
	ErrQuantity = errs.Define(ID, "ABOVE_CLAIM", errs.FailedPrecondition, 409, errs.RetryNo,
		"A draft's quantities and amounts only go down from what was claimed")
)

var (
	UUIDForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	DateForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Check decides op on an invoice of the legal entity and returns the context to work in and the acting principal.
func Check(c *httpx.Context, op, legalEntity string) (context.Context, string, error) {
	s, _ := sdkauthz.From(c.Ctx())
	ctx, _, err := s.Check(c.Ctx(), op, sdkauthz.Facts{DimLegalEntity: sdkauthz.Value(legalEntity)})
	if err != nil {
		return ctx, "", err
	}
	u, _ := identity.UserFrom(ctx)
	return ctx, u.Subject, nil
}

func Invalid(field, why string) error {
	return errs.New(errs.ReasonRequestInvalid, why).WithViolations(errs.Violation{Field: field, Reason: "INVALID", Description: why})
}

func NotFound(what string) error { return errs.New(errs.ReasonNotFound, what+" not found") }

// Decimal checks a non-negative decimal string of at most scale places and returns its canonical form.
func Decimal(field, v string, scale int32) (string, error) {
	d, err := money.Parse(v)
	if err != nil || d.Sign() < 0 || -d.Exponent > scale {
		return "", Invalid(field, field+" is a non-negative decimal string with at most "+strconv.Itoa(int(scale))+" places")
	}
	d.Reduce(d)
	return d.Text('f'), nil
}

// change records the business change with the record's new state (protocol 03), in the transaction that made it;
// more adds entries to the changes (the issued number, be-protocol P14.5).
func change(m *Base, ctx context.Context, tx db.Tx, actor, op, id string, after any, more ...map[string]any) error {
	changes := map[string]any{"after": after}
	for _, extra := range more {
		maps.Copy(changes, extra)
	}
	_, err := m.RT.Audit.Write(ctx, tx, audit.Record{Category: audit.BusinessChange, Operation: op, Outcome: audit.Succeeded,
		Actor: actor, Target: audit.TargetOf(Invoices, id, after), Changes: changes})
	return err
}

// Emit writes an event into the outbox, in the transaction that changed the record (protocol 02, S2).
func Emit(m *Base, ctx context.Context, tx db.Tx, typ, id string, version int64, legalEntity string, data any) error {
	_, err := m.RT.Events.Emit(ctx, tx, Stream, events.Event{Type: typ, RecordID: id, Version: version,
		Projection: events.Full, LegalEntity: legalEntity, Data: data})
	return err
}

func ErrorsIsNotFound(err error) bool { return errs.Is(err, errs.PlatformDomain, errs.ReasonNotFound) }
