# core：全组件共用的名字与写法（共用）

每个模块都引用、自己什么业务也不做的东西。改这里会影响所有模块。

## 负责与不负责

- 负责：组件 ID、依赖的 ID、流与事件类型、资源与操作、法人维度、上限与时限、模板名、作业名、资源定义 `Definitions()`（`names.go`）；本组件的错误原因、格式、授权检查、校验、十进制、审计、发件表（`Emit`）、`ErrorsIsNotFound`（`rules.go`）；记录的类型、读取（`GetInvoice`、`ReadInvoice`）、账单事件与“已开具”事实（`records.go`）；`Base`（`base.go`）。
- 不负责：任何业务规则（在各模块）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `base.go` | `Base`：组件的运行时 |
| `names.go` | 常量；`fields`、`Definitions()` |
| `rules.go` | 错误原因、格式、`Check`、`Invalid`、`NotFound`、`Decimal`、`change`、`Emit`、`ErrorsIsNotFound` |
| `records.go` | `Invoice`、`Line`、`Totals` 等记录与 `InvoiceCols`、`ScanInvoice`、`GetInvoice`、`ReadInvoice`；`EmitInvoice`、`IssuedFact` |

## 依赖与被依赖

- 依赖：只有 SDK 与契约。
- 被依赖：全部模块与入口。

## 规则在哪

`docs/design.md`；装配项目协议 05、14，安全 07。

## 测试

由 `itest/` 的流程测试间接覆盖。
