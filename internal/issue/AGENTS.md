# issue：开具

开具一张草稿：确认认领（认领过期的，草稿作废，BL05、BL26）、编号（连续，按法人与期间）、正式化（按调低后的数量）。确认之后中断的（例如法人被暂停），由工作项在恢复后接着编号；正式化没被来源接受的，工作项按同一身份重发（BL04）。

## 负责与不负责

- 负责：路由 `/api/billing/invoices/:id/issue`（`IssueInvoice`）；`confirmClaim`、`number`、`formalize`；工作项 `FormalizeJob`、`ContinueJob`；事件 `erp.invoice.issued.v1`。
- 不负责：草稿与作废（`invoices`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `issue.go` | `IssueInvoice`、`confirmClaim`、`number`、`formalize`、`FormalizeJob`、`ContinueJob` |

## 依赖与被依赖

- 依赖：`core`、`invoices`、`masterdata`。
- 被依赖：入口。

## 规则在哪

`docs/design.md` 的状态表与规则要点；`docs/flows/billing.md`；装配项目协议 05。

## 测试

`itest/invoices_integration_test.go`（`draftAndIssue`、`voidAndExpiry`）、`itest/issue_integration_test.go`（`recovery`）。
