# erp-billing

组件 `erp/billing`：客户账单（商业发票）。从销售订单认领可出账额度草拟，按法人、按年连续编号开具，发布“已出账”事实，打印快照保证重打一致。使用说明见 `BRICKKIT.md`，修改说明见 `AGENTS.md`，设计见 `docs/design.md`。

新组件，按终局设计从零开始（D59），版本从 `3.0.0` 起（A110）。

## 在项目里使用

在装配项目里 `brickkit add erp/billing@<版本>`，再按 `BRICKKIT.md` 的“部署前准备”配置。种子数据：`make seed`（见 `cmd/seed`）。

## 文档

| 问题 | 看哪里 |
| --- | --- |
| 它做什么、怎样部署与配置 | `BRICKKIT.md` |
| 为什么这样设计：术语、数据、规则、未决问题 | `docs/design.md`；草拟、开具、作废与恢复的顺序 `docs/flows/billing.md` |
| 怎样改它 | `AGENTS.md` |
| 契约 | `contract/README.md` |

## 开发

见 `AGENTS.md` 的“构建与测试”。
