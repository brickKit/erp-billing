package billing

import (
	"context"

	"github.com/brickKit/be-sdk-go/v3/recovery"
	core "github.com/brickKit/erp-billing/v3/internal/core"
	recoverycheck "github.com/brickKit/erp-billing/v3/internal/recoverycheck"
)

// RecoveryChecks registers what a full recovery asks this member (be-protocol chapter 14): whether the objects this
// member audits are still here, at which version, and the checks it reports.
func (m *Module) RecoveryChecks(c *recovery.Checks) error {
	c.Presence(core.Invoices, recovery.TablePresence("invoice", "id", "version"))
	c.Void(recoverycheck.VoidNumbers)
	if err := c.Add(recovery.Check{Name: recoverycheck.CheckClaims, Basis: "装配项目协议 05（出账与来源，本组件是认领方，替两边报告）；协议 15 第 8 步",
		Run: func(ctx context.Context, read recovery.Read) (recovery.Result, error) {
			return recoverycheck.ClaimsCheck(m.b, ctx, read)
		}}); err != nil {
		return err
	}
	return nil
}
