# masterdata：向上游取证与调用（共用）

向 `mdm/org`（法人）、`mdm/currency`（小数位）、`erp/sales` 的出账来源（订单与它的台账、认领与正式化）、`infra/print`（渲染）。每个调用都在事务之外。

## 负责与不负责

- 负责：`LegalEntityOf`、`CurrencyScale`、`OrderOf`、`Source`（出账来源的客户端）、`Render`；按依赖取 gRPC 连接（`conn`）。
- 不负责：认领的顺序与本地状态（`invoices`、`issue`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `masterdata.go` | `conn`、`LegalEntity`、`LegalEntityOf`、`CurrencyScale`、`Order`、`OrderOf`、`Source`、`Render` |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`invoices`、`issue`、`print`、`recoverycheck`。

## 规则在哪

装配项目协议 05（认领模式）、协议 07（取证在事务外）。

## 测试

由流程测试间接覆盖；替身在 `itest/standins_integration_test.go`。
