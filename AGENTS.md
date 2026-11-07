# erp/billing

出账组件的代码。用法与边界在 `BRICKKIT.md`，为什么这样设计（术语、数据、状态、规则、未决问题）在 [docs/design.md](docs/design.md)；草拟、开具、作废与恢复每一步的顺序在 [docs/flows/billing.md](docs/flows/billing.md)。跨组件的规则在装配项目 be-assembly-standard 的协议 05（认领模式，本组件是“出账”用法的使用方）、协议 14（单号、金额）、协议 10（打印）、安全 07（法人维度）。

## 代码地图

代码按业务模块分层。**先读这一节，再进目录**：每个代码目录都有自己的 `AGENTS.md`（同目录的 `CLAUDE.md` 让 Claude Code 读这个目录的文件时自动带上它），写这一层负责什么、不负责什么、每个文件、依赖谁、测在哪。

| 目录 | 是什么 |
| --- | --- |
| [`billing/`](billing/AGENTS.md) | 入口：组件定义与组装（路由、系统面、后台工作、恢复核对都在这里登记，转给业务模块）；不写业务。`main.go` 与外壳只引用它 |
| [`internal/`](internal/AGENTS.md) | 业务：下面的模块各一个包；模块之间的依赖表在它的 `AGENTS.md` |
| [`itest/`](itest/AGENTS.md) | 集成测试，按业务流程分文件；它的 `AGENTS.md` 是测试索引 |
| `billing/migrations/` | 表：账单、账单行、草稿的请求键、作废的号 |
| `cmd/seed/` | 种子（`make seed`） |
| `contract/` | 本组件发布的契约，单独的模块：`contract/proto/`、`contract/events/`、`contract/openapi.yaml` |
| `docs/` | 设计与细则（`docs/flows/billing.md`） |

业务模块（`internal/` 下）：

| 模块 | 负责 | 拥有的表 |
| --- | --- | --- |
| [`invoices`](internal/invoices/AGENTS.md) | 账单草稿：按订单认领并起草、修改、作废（释放认领）、读取、列表；系统面的读取 | `invoice`、`invoice_line`、`draft_key` |
| [`issue`](internal/issue/AGENTS.md) | 开具：确认认领、编号、正式化；中断后由工作项接着做 | — |
| [`print`](internal/print/AGENTS.md) | 打印：视图模型、第一次的版本与之后的副本 | — |
| [`recoverycheck`](internal/recoverycheck/AGENTS.md) | 整套恢复后协议 05 的认领核对、跨过的号登记作废 | `voided_number` |

共用的：[`masterdata`](internal/masterdata/AGENTS.md)（事务外向 `mdm/org`、`mdm/currency`、销售的出账来源、打印组件取证与调用）、[`core`](internal/core/AGENTS.md)（名字与资源定义、校验、审计与事件的写法、记录的类型与读取）。

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 账单的状态与开具的顺序 | `docs/design.md` 的状态表、`docs/flows/billing.md` | `internal/issue`、`internal/invoices`；`itest/invoices_integration_test.go`、`itest/issue_integration_test.go` |
| 账单号的格式或序列 | `docs/design.md` 的规则要点 | `internal/issue` 的 `number` |
| 打印的数据（视图模型） | `docs/design.md` 的“打印” | `internal/print` 的 `viewModel`；打印组件随带的发票模板同步改：视图模型的 JSON Schema（不允许多出的字段）与 Typst 版式，见它的 `AGENTS.md`；两个组件各升版本，本组件依赖打印的新版本 |
| 加字段、加接口、改事件 | 装配项目 be-assembly-standard 规范里的“改动配方” | `docs/design.md` 的数据与命令 |

## 构建与测试

- `go test ./...`（`make test`）：清单的 `configSchema` 与 `events`、OpenAPI 与路由、事件载荷、合并安全；打印的视图模型（`internal/print`）。
- `make test-integration`（`go test -tags integration ./itest/`）：真实的 PostgreSQL 与消息系统；每个文件测哪条流程、要什么前提，在 [itest/AGENTS.md](itest/AGENTS.md)。
- `brickkit lint`

## 设计取舍

- **额度只由来源裁决**：本组件只认领，不自己算余额；认领身份就是账单身份，所有来源命令按它幂等，出站调用都标为幂等（`calls.Idempotent()`），结果未知时 SDK 用同一身份重试。
- **草拟的重试认同一份**：幂等键第一次草拟时记下账单身份与认领的行（`draft_key`），重试拿回同一份再认领，来源返回原认领；不重新取剩余（否则自己的认领会让剩余变成零）。
- **先确认认领，再编号**：编号事务只接受 `confirmed` 的账单；作废先在本地提交再释放认领，所以已释放的认领不会被编号（BL25）。
- **编号连续**：`db.NextNumber` 在编号事务里取号，回滚还号；草稿与作废不占号。
- **打印快照存原样的字节**：视图模型存成文本（不是 `jsonb`），重打交给打印组件的是第一次的同一份字节。

## 易错点

- 不要在事务里调销售、法人或打印：先取（`upstream.go`），再开事务。
- 列表能筛选的字段必须在资源定义的字段里声明（`definition.go`），否则 SDK 答 `FIELD_NOT_QUERYABLE`。
- 不要用浮点算金额：乘法经 `times`（`apd`），比较与加减在 SQL 里做。

## 改代码前自查

- 改了契约（`contract/`）：proto 改后 `make -C contract gen`，发布 `contract/v…`，再改这里的依赖；OpenAPI 与路由由测试核对。
- 改了版本号吗？每次改动都升版本。

<!-- brickkit:managed:begin lang=zh -->
## BrickKit

这是一个 BrickKit 组件：平台只读 `component.yaml`。它依赖的规则：

- `configSchema` 的键就是代码读的环境变量名。不能用保留名：`COMPONENT_ID`、`COMPONENT_VERSION`、`PORT`、`BRICKKIT_SERVED_MEMBERS`、`BRICKKIT_SERVED_MEMBERS_CONFIG`，以及任何 `*_ENDPOINT`。
- 依赖写精确版本。依赖的地址以 `<ID>_ENDPOINT` 注入；缺席的可选依赖根本没有这个变量，读的时候要带兜底。
- `/healthz` 只查本进程，不查依赖。迁移命令用同一个镜像跑，遇到不认识的参数必须直接失败；`local.migrationCommand` 从源码跑同一条迁移，给 `mode: local` 用。
- `BRICKKIT.md` 会随版本进入每个使用它的项目，在那里是脱离仓库单独读的：跟代码一起改，不放相对链接。
- 发版：改 `metadata.version`，提交、推送，`brickkit release`。`brickkit lint` 会检查清单和这些文档——在项目里、在这个目录下跑，只查这个组件（`--all` 查整个项目）。
- 完整规则在 `brickkit-component` 技能里（装了技能的项目或仓库根目录下的 `.claude/skills/brickkit-component/SKILL.md`；`brickkit skills update` 会装上）；参数问 `brickkit <命令> --help`；BrickKit 自己的文档用 `brickkit docs` 看。
<!-- brickkit:managed:end -->
