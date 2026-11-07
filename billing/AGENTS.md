# billing/：入口与组装

组件对外的一个包：`main.go` 与外壳只引用它（`billing.Definition()`、`billing.New`）。这里只**组装**：把 `internal/` 下模块的函数登记成 SDK 要的路由、gRPC 方法、后台工作与恢复核对；业务一行也不写。

## 负责与不负责

- 负责：组件的版本、端口、迁移、组件定义；哪个路由、哪个 gRPC 方法、哪个后台工作交给哪个模块。
- 不负责：业务规则（去 [internal/AGENTS.md](../internal/AGENTS.md) 找模块）；资源、操作、维度、事件类型、作业的名字（`internal/core/names.go`）；集成测试（`itest/`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `definition.go` | `Version`、`GRPCPort`、迁移；`Definition()`：身份、依赖、流、资源与操作（`core.Definitions()`） |
| `module.go` | `Module` 与 `New`；`Routes`（起草、开具、作废、打印是编排的路由；更正本版不做 → `invoices.Corrections`）、`Services`（读取，任何组件可调）、`Jobs`（正式化、释放认领、接着开具、清理草稿的请求键）；`handler`、`job` 适配 |
| `services.go` | gRPC 方法 `Read` 转给 `internal/invoices` |
| `checks.go` | 整套恢复的核对：账单按版本答“还在不在”；跨过的号登记作废；协议 05 的认领核对 → `recoverycheck.ClaimsCheck` |
| `contract_test.go` | 单元测试：清单的 `configSchema` 与 `events`、OpenAPI 与路由一一对应、事件载荷合乎 schema、合并安全 |
| `structure_test.go` | 单元测试，按类型检查 `internal/`：`internal/invoices/lifecycle.go` 以外不写 `invoice.state` 与 `formalization`，读它们的（Go 或 SQL）都登记在 `invoiceStateReaders`（文件与函数、用途） |
| `migrations/` | 表 |

## 依赖与被依赖

- 依赖：`internal/core`、`invoices`、`issue`、`print`、`recoverycheck`。
- 被依赖：`main.go`、`cmd/seed`、外壳 `be/go-core`、`itest/`。

## 测试

`go test ./billing/`。
