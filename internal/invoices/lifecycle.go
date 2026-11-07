package invoices

import (
	"context"
	"fmt"
	"strings"

	"github.com/brickKit/be-sdk-go/v3/db"
	core "github.com/brickKit/erp-billing/v3/internal/core"
)

// The invoice's lifecycle in one table (docs/design.md, "账单的状态"; rules IN01-IN09). Every command on an invoice
// asks Decide what it may do in the invoice's phase, and every change of the invoice's state or formalization goes
// through Transition. A new state or command is a new phase or command here: TestTheLifecycleDecidesEveryCell fails
// until each of its cells is decided, and TestTheDesignDocumentIsTheLifecycle until docs/design.md says the same.

// Phase is where an invoice is: its stored state, with an issued invoice whose formalization is done told apart.
type Phase string

const (
	Draft      Phase = "draft"
	Confirmed  Phase = "confirmed"  // the claim confirmed at the source, not numbered yet
	Issued     Phase = "issued"     // numbered; the source not yet told what was billed (formalization pending)
	Formalized Phase = "formalized" // stored as issued, the formalization done
	Void       Phase = "void"
)

// Phases are every phase, in the order of docs/design.md.
var Phases = []Phase{Draft, Confirmed, Issued, Formalized, Void}

// Command is what happens to an invoice: a user's command, an answer of the source, or a step of the issue.
type Command string

const (
	Change         Command = "change"          // change a draft's quantities (PATCH)
	VoidIt         Command = "void"            // void it (the user)
	Issue          Command = "issue"           // the user's issue request
	ClaimConfirmed Command = "claim_confirmed" // the source confirmed the claim
	ClaimExpired   Command = "claim_expired"   // the source refused to confirm: the claim expired
	Number         Command = "number"          // the numbering transaction
	Formalize      Command = "formalize"       // the source took what was billed
	Print          Command = "print"           // print (the first rendering or a copy)
	Continue       Command = "continue"        // the work item that numbers an issue stopped after its confirmation
)

// Commands are every command, in the order of docs/design.md.
var Commands = []Command{Change, VoidIt, Issue, ClaimConfirmed, ClaimExpired, Number, Formalize, Print, Continue}

// Effect is what a command does in a phase.
type Effect string

const (
	Refuse  Effect = "refuse"  // refused with Decision.Refusal
	Ignore  Effect = "ignore"  // nothing happens; the command answers as done (a repeat, a step that no longer applies)
	Proceed Effect = "proceed" // the command does its work; the invoice's phase stays
	Move    Effect = "move"    // the command does its work and the invoice moves to Decision.To (Transition)
	Process Effect = "process" // the command first goes through the claim's confirmation at the source
)

// Decision is one cell of the table.
type Decision struct {
	Rule    string // the rule in docs/design.md
	Effect  Effect
	To      Phase  // Move: the phase after
	Op      string // Move: the operation the change's audit names (every change of an invoice is audited)
	Refusal string // Refuse: the words; {state} is the invoice's stored state
}

const (
	notDraft    = "only a draft changes; this invoice is {state}"
	notVoidable = "an issued invoice is corrected, never voided"
	notIssued   = "only an issued invoice prints; this one is {state}"
)

func refuse(rule, words string) Decision { return Decision{Rule: rule, Effect: Refuse, Refusal: words} }
func ignore(rule string) Decision        { return Decision{Rule: rule, Effect: Ignore} }
func proceed(rule string) Decision       { return Decision{Rule: rule, Effect: Proceed} }
func move(rule string, to Phase, op string) Decision {
	return Decision{Rule: rule, Effect: Move, To: to, Op: op}
}

// lifecycle is the table: every phase decides every command.
var lifecycle = map[Command]map[Phase]Decision{
	Change: {
		Draft:      proceed("IN01"),
		Confirmed:  refuse("IN01", notDraft),
		Issued:     refuse("IN01", notDraft),
		Formalized: refuse("IN01", notDraft),
		Void:       refuse("IN01", notDraft),
	},
	VoidIt: { // locally first, then the claim is released (BL25)
		Draft:      move("IN02", Void, core.OpDraft),
		Confirmed:  move("IN02", Void, core.OpDraft),
		Issued:     refuse("IN02", notVoidable),
		Formalized: refuse("IN02", notVoidable),
		Void:       ignore("IN02"),
	},
	Issue: {
		Draft:      {Rule: "IN03", Effect: Process},
		Confirmed:  proceed("IN03"),
		Issued:     ignore("IN03"),
		Formalized: ignore("IN03"),
		Void:       refuse("IN03", "the invoice is void"),
	},
	ClaimConfirmed: {
		Draft:      move("IN04", Confirmed, core.OpIssue),
		Confirmed:  ignore("IN04"),
		Issued:     ignore("IN04"),
		Formalized: ignore("IN04"),
		Void:       ignore("IN04"),
	},
	ClaimExpired: { // BL05, BL26
		Draft:      move("IN05", Void, core.OpDraft),
		Confirmed:  ignore("IN05"),
		Issued:     ignore("IN05"),
		Formalized: ignore("IN05"),
		Void:       ignore("IN05"),
	},
	Number: { // BL21, BL25
		Draft:      refuse("IN06", "the invoice's claim is not confirmed yet"),
		Confirmed:  move("IN06", Issued, core.OpIssue),
		Issued:     ignore("IN06"),
		Formalized: ignore("IN06"),
		Void:       refuse("IN06", "the invoice was voided: it takes no number"),
	},
	Formalize: { // BL04
		Draft:      ignore("IN07"),
		Confirmed:  ignore("IN07"),
		Issued:     move("IN07", Formalized, core.OpIssue),
		Formalized: ignore("IN07"),
		Void:       ignore("IN07"),
	},
	Print: {
		Draft:      refuse("IN08", notIssued),
		Confirmed:  refuse("IN08", notIssued),
		Issued:     proceed("IN08"),
		Formalized: proceed("IN08"),
		Void:       refuse("IN08", notIssued),
	},
	Continue: {
		Draft:      ignore("IN09"),
		Confirmed:  proceed("IN09"),
		Issued:     ignore("IN09"),
		Formalized: ignore("IN09"),
		Void:       ignore("IN09"),
	},
}

// Decide answers what command c does in phase p.
func Decide(p Phase, c Command) Decision {
	d, ok := lifecycle[c][p]
	if !ok { // the table test keeps this from happening
		return Decision{Effect: Refuse, Refusal: fmt.Sprintf("no rule for %s in %s", c, p)}
	}
	return d
}

// PhaseOf is the invoice's phase.
func PhaseOf(v core.Invoice) Phase {
	if v.State == string(Issued) && v.Formalization == "done" {
		return Formalized
	}
	return Phase(v.State)
}

// PhaseOfStored is the phase of a row's stored state and formalization, read in SQL.
func PhaseOfStored(state, formalization string) Phase {
	return PhaseOf(core.Invoice{State: state, Formalization: formalization})
}

// Refused is the refusal of d for v, as the STATE_CONFLICT error the user surface answers.
func Refused(d Decision, v core.Invoice) error {
	return core.ErrState.New(strings.ReplaceAll(d.Refusal, "{state}", v.State))
}

// Numbered is what the numbering transaction writes beside the state (Number only).
type Numbered struct {
	Number, InvoiceDate, Search string
	SearchVersion               int
}

// Transition is the one writer of the invoice's state and formalization: d is a Move decided for v, read under the
// invoice's lock in tx. It publishes the invoice and audits the change (more: the audit's further changes); n is
// what the numbering writes (Number only).
func Transition(m *core.Base, ctx context.Context, tx db.Tx, v core.Invoice, d Decision, n *Numbered, actor string,
	more ...map[string]any) (core.Invoice, error) {
	if d.Effect != Move {
		return v, fmt.Errorf("invoices: transition of %s on a %s decision", v.ID, d.Effect)
	}
	if (d.To == Issued) != (n != nil) {
		return v, fmt.Errorf("invoices: %s to %s: the numbering writes a number, nothing else does", v.ID, d.To)
	}
	var err error
	var tag interface{ RowsAffected() int64 }
	const from = ` WHERE id = $1 AND state = $2 AND formalization = $3`
	switch d.To {
	case Confirmed, Void:
		tag, err = tx.Exec(ctx, `UPDATE invoice SET state = $4, version = version + 1, updated_at = now()`+from,
			v.ID, v.State, v.Formalization, string(d.To))
	case Issued:
		tag, err = tx.Exec(ctx, `UPDATE invoice SET state = 'issued', number = $4, invoice_date = $5::date,
			formalization = 'pending', issued_at = now(), search = $6, search_version = $7, version = version + 1,
			updated_at = now()`+from, v.ID, v.State, v.Formalization, n.Number, n.InvoiceDate, n.Search, n.SearchVersion)
	case Formalized:
		tag, err = tx.Exec(ctx, `UPDATE invoice SET formalization = 'done', version = version + 1, updated_at = now()`+from,
			v.ID, v.State, v.Formalization)
	default:
		return v, fmt.Errorf("invoices: no transition writes %s", d.To)
	}
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() != 1 {
		return v, fmt.Errorf("invoices: %s is no longer %s", v.ID, PhaseOf(v))
	}
	return core.EmitInvoice(m, ctx, tx, v.ID, actor, d.Op, more...)
}
