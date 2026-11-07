package core

import (
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
)

// ID is the component's ID.
const ID = "erp/billing"

// Work items (protocol 01): formalizing at the source and releasing a void invoice's claim are retried until the
// source answers; an issue that stopped after confirming its claim is numbered by the continue item.
const (
	JobFormalize = "formalize-invoice"
	JobRelease   = "release-claim"
	JobContinue  = "continue-issue"
)

// Dependencies (the IDs runtime.GRPC takes). Sales and print are optional.
const (
	Org      = "mdm/org"
	Partner  = "mdm/partner"
	Currency = "mdm/currency"
	Sales    = "erp/sales"
	Print    = "infra/print"
)

// The stream and its event types (contract/events).
const (
	Stream         = "erp.billing"
	InvoiceChanged = "erp.invoice.changed.v1"
	InvoiceIssued  = "erp.invoice.issued.v1"
)

// Resources and operations (contract/openapi.yaml names each route's operation).
const (
	Invoices = "erp.billing.invoice"
	OpRead   = "erp.billing.invoice.read"
	OpDraft  = "erp.billing.invoice.draft"
	OpIssue  = "erp.billing.invoice.issue"
	OpPrint  = "erp.billing.invoice.print"
	System   = "erp.billing.system"
	// OpResend is the redelivery of this component's stream (be-protocol P6.38: every component with streams serves
	// it; any component of the deployment may call it).
	OpResend = "erp.billing.system.resend"
	// OpRecover is a full recovery's checks (be-protocol chapter 14: the recovery tool only).
	OpRecover = "erp.billing.system.recover"
	OpSysRead = "erp.billing.system.read"
)

// DimLegalEntity is the dimension of invoices (security 07).
const DimLegalEntity = "legal_entity"

// Parameters.
const (
	MaxLines   = 500
	MaxReadIDs = 500
	// ContinueAfter is how long an issue that confirmed its claim may stay unnumbered before the work item numbers it.
	ContinueAfter = time.Minute
	// StuckAfter is how long a confirmed, unnumbered invoice waits before it is logged as an alert.
	StuckAfter = 24 * time.Hour
	// Template is the print template of invoices (infra/print).
	Template = "invoice"
)

func fields(names ...string) map[string]sdkauthz.Field {
	out := map[string]sdkauthz.Field{}
	for _, n := range names {
		out[n] = sdkauthz.Field{Class: sdkauthz.Ordinary}
	}
	return out
}

// Definitions are the component's resources and operations; invoices carry the legal entity.
func Definitions() *sdkauthz.Definitions {
	op := func(key, purpose string, audit bool) sdkauthz.Operation {
		o := sdkauthz.Operation{Key: key, ResourceType: Invoices, Purpose: purpose, RiskClass: "low", Audit: audit}
		if purpose != "read" {
			o.SideEffects = []string{"business_write"}
		}
		return o
	}
	return &sdkauthz.Definitions{Component: ID,
		Resources: []sdkauthz.Resource{
			{Type: Invoices, ViewOperation: OpRead,
				Dimensions: map[string]sdkauthz.Dimension{DimLegalEntity: {Kind: sdkauthz.ValueSet, Column: "legal_entity_id"}},
				Fields: fields("id", "number", "legal_entity_id", "customer", "currency", "exchange_rate", "source", "state",
					"formalization", "invoice_date", "lines", "totals", "print", "version", "created_at", "updated_at")},
			{Type: System, ViewOperation: OpSysRead, Dimensions: map[string]sdkauthz.Dimension{},
				Fields: map[string]sdkauthz.Field{}},
		},
		Operations: []sdkauthz.Operation{
			op(OpRead, "read", false),
			op(OpDraft, "manage", true),
			op(OpIssue, "action", true),
			op(OpPrint, "action", true),
			{Key: OpSysRead, ResourceType: System, Purpose: "read", RiskClass: "low"},
			{Key: OpResend, ResourceType: System, Purpose: "read", RiskClass: "low"},
			{Key: OpRecover, ResourceType: System, Purpose: "read", RiskClass: "low"},
		}}
}
