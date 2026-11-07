# itest/：集成测试与测试索引

出账组件对着真实的 PostgreSQL 与消息系统、经它真实的接口的测试（L2）。主数据组件、销售的出账来源与 `infra/print` 是按已发布的契约写的替身，走 mTLS（`standins_integration_test.go`）。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`），约 10 秒。
- 环境变量：`BE_SDK_TEST_PG_ADMIN=postgres://postgres:<口令>@localhost:5432/brickkit_test_db`（口令是装配项目 `.env` 的 `POSTGRES_PASSWORD`）、`BE_SDK_TEST_NATS_URL=nats://localhost:4222`。
- 前提：装配项目里 `make up` 起了 `be-postgres` 与 `be-nats`；库 `brickkit_test_db` 存在；测试自己的 schema 每次重建；端口要空着，被占时测试直接失败。
- **只有一个测试** `TestBillingEndToEnd`：按下表的顺序跑各步（普通的方法调用），后面的步用前面开的账单，所以一步不能单独跑。

## 文件

按运行顺序：

| 文件 | 步 | 测的流程 | 被测的模块 |
| --- | --- | --- | --- |
| `invoices_integration_test.go` | `draftAndIssue`、`concurrentDrafts`、`voidAndExpiry` | 剩下的全部按账单身份认领（重试 BL02）、调低的草稿、开具时确认编号并按调低后的数量正式化；两人同时起草同一订单只一张（BL01）；作废的草稿释放认领、不占号（BL25），认领过期时开具作废草稿（BL05、BL26），下一个号接着不缺号 | `invoices`、`issue` |
| `issue_integration_test.go` | `recovery` | 来源没接受正式化时工作项重发（BL04）；确认后中断（法人暂停）的开具由工作项在法人恢复后编号 | `issue` |
| `print_integration_test.go` | `printing` | 第一次渲染记下模板版本与视图模型，之后的照它们并标为副本，模板变了也一样 | `print` |
| `lists_integration_test.go` | `listsAndScope` | 关键字按号找、日期筛选、系统面的读取、别的法人的读者看不见 | `invoices` |
| `outbox_integration_test.go` | `outbox` | 每个事件合乎 schema；每张开具的账单正好一条“已开具” | 全部发事件的模块 |
| `recoverycheck_integration_test.go` | `recoveryChecks` | 整套恢复的核对：账单按版本答“还在不在”；协议 05 的认领核对 | 入口 `billing/checks.go`、`recoverycheck` |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestBillingEndToEnd`（各步的顺序）；`fixture`；`start`；`order`、`draft`、`issue`、`get`、`raw`；`client.do`、`send`、`serve`、`at`、`portFree` |
| `standins_integration_test.go` | 替身：主数据组件、销售的出账来源（可以拒绝正式化）、`infra/print` |

## 加测试

新流程加一个文件与一步，在 `TestBillingEndToEnd` 里按依赖排进顺序，在上表加一行；测试不为了通过而改。
