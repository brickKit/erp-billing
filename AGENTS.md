# erp/billing

出账组件的代码。用法与边界在 `BRICKKIT.md`，为什么这样设计（术语、数据、状态、规则、未决问题）在 [docs/design.md](docs/design.md)；草拟、开具、作废与恢复每一步的顺序在 [docs/flows/billing.md](docs/flows/billing.md)。跨组件的规则在装配项目 be-assembly-standard 的协议 05（认领模式，本组件是“出账”用法的使用方）、协议 14（单号、金额）、协议 10（打印）、安全 07（法人维度）。

## 代码地图

| 位置 | 内容 |
| --- | --- |
| `billing/definition.go` | 组件定义：身份、依赖（销售与打印可选）、流与事件类型、资源与操作、法人维度、参数 |
| `billing/module.go` | 路由与系统面的注册、本组件的错误原因、后台工作的声明、`check`、`decimal`、审计与事件的写法 |
| `billing/upstream.go` | 事务外的调用：法人、币种小数位、销售的订单读取与出账来源、打印渲染 |
| `billing/invoices.go` | 草拟（`draftClaim`：认领身份与认领的行随幂等键记下）、修改草稿（只减不增）、作废与释放认领、读取、`InvoiceService.Read` |
| `billing/issue.go` | 开具：确认认领（`confirmClaim`）、编号事务（`number`）、正式化（`formalize`）；正式化与继续开具的工作项 |
| `billing/print.go` | 打印快照（第一次渲染记下模板版本与视图模型）、视图模型的格式化（千分位、百分比、金额大写） |
| `billing/lists.go` | 账单列表 |
| `billing/records.go` | 记录的类型、读取的 SQL、“已出账”事实的载荷 |
| `billing/migrations/` | 表 |
| `cmd/seed/` | 种子：角色与授予、从订单草拟、作废、开具的账单，每个法人前几张的打印（`make seed`） |
| `contract/` | 本组件发布的契约，单独的模块：`contract/proto/`、`contract/events/`、`contract/openapi.yaml` |
| `docs/` | 设计与出账的顺序 |

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 账单的状态与开具的顺序 | `docs/design.md` 的状态表、`docs/flows/billing.md` | `issue.go`；集成测试的 `draftAndIssue`、`voidAndExpiry`、`recovery` |
| 账单号的格式或序列 | `docs/design.md` 的规则要点 | `issue.go` 的 `number` |
| 打印的数据（视图模型） | `docs/design.md` 的“打印” | `print.go` 的 `viewModel`；打印组件的发票模板要同步 |
| 加字段、加接口、改事件 | 装配项目 be-assembly-standard 规范里的“改动配方” | `docs/design.md` 的数据与命令 |

## 构建与测试

- `go test ./...`（`make test`）：清单的 `configSchema` 与 `events`、OpenAPI 与路由、事件载荷、合并安全、视图模型的格式化。
- `go test -tags integration ./billing/`（`make test-integration`）：真实的 PostgreSQL 与消息系统，例如 `BE_SDK_TEST_PG_ADMIN=postgres://postgres:<口令>@localhost:5432/brickkit_test_db`（口令是装配项目 `.env` 的 `POSTGRES_PASSWORD`）与 `BE_SDK_TEST_NATS_URL=nats://localhost:4222`；be-sdk-go 的 `sectest` 是测试部署；`mdm/org`、`mdm/currency`、`erp/sales`（订单读取与出账来源，按台账裁决认领）、`infra/print`（确定的渲染）是按契约写的替身（`standins_integration_test.go`），销售的替身可以丢一次应答、让认领到期、不可达。
- 集成测试的前提：装配项目里 `make up` 起了 `be-postgres` 与 `be-nats`；`be-postgres` 里有库 `brickkit_test_db`；测试每次开始时删掉再建自己的 schema `erp_billing_it`，跑完留着；端口 28089、9089 与替身的 19108–19111 要空着，被占时测试直接失败。
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
