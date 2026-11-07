# internal/：业务模块

出账的业务，每个模块一个包。只给本仓库引用（Go 的 `internal`）：别的组件编译期就引用不了，跨组件只走契约（决定 0101）。先按下表找到模块，再读那个目录的 `AGENTS.md`。

## 模块

| 包 | 一句话 | 依赖（本组件内） |
| --- | --- | --- |
| `invoices` | 账单草稿：起草、修改、作废、读取、列表 | `core`、`masterdata` |
| `issue` | 开具：确认认领、编号、正式化 | `core`、`invoices`、`masterdata` |
| `print` | 打印 | `core`、`invoices`、`masterdata` |
| `recoverycheck` | 整套恢复后的认领核对、作废的号 | `core`、`invoices`、`masterdata` |
| `masterdata`（共用） | 事务外向主数据、销售的出账来源、打印组件取证与调用 | `core` |
| `core`（共用） | 名字与资源定义、校验、审计与事件、记录、`Base` | — |

分层（每个包只引用比它低的层）：`issue`、`print`、`recoverycheck` → `invoices` → `masterdata` → `core`。

## 共同的写法

- 每个模块的函数先收 `m *core.Base`（组件的运行时），入口包的 `Module` 持有它、登记时适配。
- 本组件是认领方（装配项目协议 05）：认领、确认、正式化、释放都是调销售的出账来源，在事务之外；本地的状态在事务里写，结果未知的由工作项按同一身份重发。
- 写入、审计与发件表（`core.Emit`）同一个事务。

## 测试

模块的业务规则由 `itest/` 的流程测试覆盖；`print` 有单元测试。
