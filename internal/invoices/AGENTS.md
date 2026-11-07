# invoices：账单草稿

按订单起草账单：以账单的身份向销售认领剩余可出账的行（BL02：重试同一身份答同一认领），可以调低数量；修改草稿；作废草稿释放认领、不占号（BL25）。

## 负责与不负责

- 负责：表 `invoice`、`invoice_line`、`draft_key`；路由 `/api/billing/invoices` 的列表、起草、读取、修改、作废，以及更正（本版不做，`Corrections`）；`InvoiceService.Read`；释放认领的工作项（`ReleaseJob`）；清理过期的草稿请求键（`PurgeDraftKeys`）。
- 不负责：开具（`issue`）；打印（`print`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `invoices.go` | `Arith`、`times`、`left`；`lineIn`、`checkLines`、`draftClaim`、`unmarshalLines`、`DraftInvoice`、`insertDraft`、`totals`、`IDResult`、`resultID`；`GetInvoice`、`UpdateInvoice`、`VoidInvoice`、`SetVoid`、`Fresh`；`InvoiceArgs`、`release`、`ReleaseJob`；`Read`；`PurgeDraftKeys`；`Corrections` |
| `lists.go` | 列表 `invoiceList`、`ListInvoices` |

## 依赖与被依赖

- 依赖：`core`、`masterdata`。
- 被依赖：`issue`、`print`、`recoverycheck`；入口。

## 规则在哪

`docs/design.md` 的状态表；`docs/flows/billing.md`；装配项目协议 05（BL 编号）。

## 测试

`itest/invoices_integration_test.go`（`draftAndIssue`、`concurrentDrafts`、`voidAndExpiry`）、`itest/lists_integration_test.go`。
