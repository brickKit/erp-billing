# print：打印

账单的打印：第一次渲染记下模板版本与视图模型，之后的照它们渲染并标为副本，即使模板已经变了。渲染由打印组件做（`masterdata.Render`）。

## 负责与不负责

- 负责：路由 `/api/billing/invoices/:id/print`（`PrintInvoice`）；视图模型（`viewModel`：金额的位数、分组、税率、大写金额）。
- 不负责：版式（打印组件随带的模板）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `print.go` | `PrintInvoice`、`rendered`、`firstPrint`、`viewModel`、`places`、`grouped`、`percent`、`inWords` |
| `print_test.go` | 单元测试：视图模型的格式 |

## 依赖与被依赖

- 依赖：`core`、`invoices`、`masterdata`。
- 被依赖：入口。

## 规则在哪

`docs/design.md` 的“打印”；打印组件的 `AGENTS.md`（视图模型的 JSON Schema）。

## 测试

`print_test.go`；`itest/print_integration_test.go`（`printing`）。
