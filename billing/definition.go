// Package billing is the billing component: customer invoices drafted from the billable ledger of erp/sales (protocol
// 05's claim pattern, the billing use), numbered continuously per legal entity and year, the "invoiced" fact for
// finance and credit, and the print snapshot that keeps every reprint the same.
package billing

import (
	"embed"
	"io/fs"

	core "github.com/brickKit/erp-billing/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity.
const (
	Version  = "3.0.47"
	GRPCPort = 9089
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: core.ID, Version: Version, Migrations: migrations(), GRPCPort: GRPCPort,
		Streams: []events.Stream{{Name: core.Stream, Partitions: 4}}, Authz: core.Definitions(), Resend: core.OpResend, Recovery: core.OpRecover,
		New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
