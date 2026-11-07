# invoices：账单草稿与生命周期

按订单起草账单：以账单的身份向销售认领剩余可出账的行（BL02：重试同一身份答同一认领），可以调低数量；修改草稿；作废草稿释放认领、不占号（BL25）。**账单的生命周期也在这里**：一张表定每个阶段能做什么、状态怎样变，状态与正式化唯一的写入（`Transition`）。

`lifecycle.go` 是一张表：5 个阶段（`draft`、`confirmed`、`issued`＝已编号而正式化未完成、`formalized`＝已编号且正式化完成、`void`）× 9 个命令（修改、作废、开具请求、认领确认、认领到期、编号、正式化、打印、继续作业），每格写：拒绝（原文）、忽略、允许、变成某阶段（审计的操作）、经认领的确认，规则编号 IN01–IN09（`docs/design.md` 的“账单的状态”）。`issue`、`print`、`recoverycheck` 也从这里问：`PhaseOf` → `Decide` → 按格行事，变状态只经 `Transition`。

- **加一个状态或命令**：在表里加一列或一行并定下每一格（`TestTheLifecycleDecidesEveryCell` 不定完不过）；同步改 `docs/design.md` 的同一张表（`TestTheDesignDocumentIsTheLifecycle` 逐格比较）；规则目录加编号、集成测试的注释写上编号（`TestEveryRuleIsTested`）；再看 `billing/structure_test.go` 的 `invoiceStateReaders`：登记在那里的每一处（现在是恢复核对的 `ClaimsCheck`）都要决定新状态怎样处理；契约里的状态枚举（OpenAPI、事件 schema、`InvoiceService.Read` 的数据）一并改。
- **状态变化时要做的事**：`Transition` 写状态、发 `erp.invoice.changed.v1`、审计；作废后释放认领的工作项（`SetVoid`）、确认后的继续作业、编号后的“已开具”事实与正式化工作项由调用方在同一事务里接着做。

## 负责与不负责

- 负责：账单的生命周期（`lifecycle.go`）；表 `invoice`、`invoice_line`、`draft_key`；路由 `/api/billing/invoices` 的列表、起草、读取、修改、作废，以及更正（本版不做，`Corrections`）；`InvoiceService.Read`；释放认领的工作项（`ReleaseJob`）；清理过期的草稿请求键（`PurgeDraftKeys`）。
- 不负责：开具（`issue`）；打印（`print`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `invoices.go` | `Arith`、`times`、`left`；`lineIn`、`checkLines`、`draftClaim`、`unmarshalLines`、`DraftInvoice`、`insertDraft`、`totals`、`IDResult`、`resultID`；`GetInvoice`、`UpdateInvoice`、`VoidInvoice`、`SetVoid`、`Fresh`；`InvoiceArgs`、`release`、`ReleaseJob`；`Read`；`PurgeDraftKeys`；`Corrections` |
| `lists.go` | 列表 `invoiceList`、`ListInvoices` |
| `lifecycle.go` | 阶段、命令、效果、`Decision`；表 `lifecycle`；`Decide`、`PhaseOf`、`PhaseOfStored`（SQL 读出的列）、`Refused`；`Numbered`、`Transition` |
| `lifecycle_test.go` | 单元测试：每一格都定了且完整；已开具与作废的不回头；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |

## 依赖与被依赖

- 依赖：`core`、`masterdata`。
- 被依赖：`issue`、`print`、`recoverycheck`；入口。

## 规则在哪

`docs/design.md` 的“账单的状态”（一张表与规则目录 IN01–IN09）；`docs/flows/billing.md`；装配项目协议 05（BL 编号）。

## 测试

单元：`lifecycle_test.go`（上表）；`billing/structure_test.go`（别处不读、不写账单状态）。集成：`itest/invoices_integration_test.go`（`draftAndIssue`、`concurrentDrafts`、`voidAndExpiry`）、`itest/lifecycle_integration_test.go`（业务流程走不到的格）、`itest/lists_integration_test.go`。
