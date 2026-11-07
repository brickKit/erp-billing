# recoverycheck：整套恢复后的认领核对

协议 05 的检查 `erp.billing.claims`：本组件是认领方，替两边报告（正式化中的账单列为差异；未结清或最近 30 天动过的账单所属的订单逐行对来源的台账，在事务之外问；问不到答“无法证明”）。连续单号被恢复提升时，跨过的号逐个登记作废。

## 负责与不负责

- 负责：表 `voided_number`；`CheckClaims`、`ClaimsCheck`、`VoidNumbers`。
- 不负责：认领本身（`invoices`、`issue`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `recoverycheck.go` | `CheckClaims`、`settledWindow`、`VoidNumbers`、`measure`、`expected`、`cmpDec`、`within`、`ClaimsCheck`、`ledgerOf` |

## 依赖与被依赖

- 依赖：`core`、`invoices`、`masterdata`。
- 被依赖：入口 `billing/checks.go`。

## 规则在哪

装配项目协议 05、协议 15 第 8 步；线协议第 14 章；`docs/design.md` 实现状态的 3.0.27。

## 测试

`itest/recoverycheck_integration_test.go`（`recoveryChecks`）。
