# erp/billing

## 组件定位

出账：向客户开具账单（商业发票）。
- **草拟**：从一张销售订单向 `erp/sales` 认领可出账的数量与金额（缺省取每行全部剩余，也可以指定行与数量），认领身份就是账单身份；认领被拒就不建账单，回答原因。同一 `Idempotency-Key` 的重试认领同一份，不重复占用。
- **草稿**：数量与金额只减不增；作废先在本地提交，再向来源释放认领。
- **开具**：先请来源确认认领（已到期则草稿作废、不编号），再取法人的证据，然后在编号事务里取下一个号（`INV-<法人代码>-<年>-<六位序号>`，按法人、按年连续，草稿与作废不占号），开票日期是法人时区的今天，“已出账”事实与账单同事务进发件表；之后向来源正式化（按实际数量），来源不应答时由工作项重发。确认之后中途停下的开具，由工作项在一分钟后接着编号。
- **金额**：行金额等于认领的金额，行税额按订单行的税率按行舍入，合计不另舍入；外币账单沿用订单的汇率快照。
- **打印**：只有已开具的账单能取 PDF（经 `infra/print`）。第一次渲染记下模板版本与视图模型（日期、千分位金额、百分比税率、金额大写），之后每次用同一份并标为副本，模板或格式化代码改了也不改变旧账单。PDF 本版不保存。
- **数据范围**：账单带法人维度。

更正与补差、红字、预收抵扣、电子发票、按序号补取（关账）、项目与合同来源本版答“能力不支持”（`CAPABILITY_UNSUPPORTED`，D75）。

## 部署前准备

- 数据库：PostgreSQL 16 及以上；schema `erp_billing`，属主角色 `erp_billing_owner`（只供迁移）与运行角色 `erp_billing_rw`（只有读写数据的权限），名字来自装配项目的注册表。三者由部署方在第一次启动前建好；表由本组件的迁移建，迁移里不建角色、不授权。
- 消息系统（`BUS_URL`）与实现实例（`IMPLEMENTATION_INSTANCE`）：账单以完整状态事件、“已出账”以增量事实发布到流 `erp.billing`（4 个分区）。
- 部署的服务证书（`SERVICE_TLS_*`）；身份与授权（`IAM_*`、`AUTHZ_GRPC_URL`、`DEPLOYMENT_ID`）；审计（`AUDIT_GRPC_URL`）。
- 依赖 `mdm/org`（法人的代码、名称、状态与时区）、`mdm/currency`（小数位）、`mdm/partner`；`erp/sales` 与 `infra/print` 是可选的。
- 授权：`erp/sales` 的出账来源只对本组件的服务身份开放（销售那边授予）。

## 依赖说明

- 必需：数据库、消息系统、身份成员、授权成员、`mdm/org`、`mdm/currency`、`mdm/partner`。
- 可选：`erp/sales`（缺时不能从订单出账；装配准入要求至少有一个出账来源）、`infra/print`（缺时取 PDF 答“依赖不可用”）、审计组件。
- 销售在草拟或开具时不可达：答“依赖不可用”，什么也不建；已编号账单的正式化由工作项重发，直到来源确认。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 生成，测试核对）。

| 键 | 说明 |
| --- | --- |
| `DEPLOYMENT_ID`、`IAM_ISSUER`、`IAM_JWKS_URL`、`AUTHZ_GRPC_URL`、`SERVICE_TLS_CERT_FILE`、`SERVICE_TLS_KEY_FILE`、`SERVICE_TLS_CA_FILE`（必填） | 受保护的用户面与 mTLS 的系统面要用；部署身份、身份与授权的地址在外壳里是进程级的键 |
| `PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（必填） | 自己的数据库，密码是密钥；迁移另用 `PG_OWNER_USER`、`PG_OWNER_PASSWORD` |
| `BUS_URL`（必填，密钥）、`IMPLEMENTATION_INSTANCE`（必填） | 消息系统；实现实例是安装时生成的随机身份 |
| `AUDIT_GRPC_URL` | 审计组件的系统面；缺时审计记录留在本地，按协议 03 处理 |

依赖的地址由 BrickKit 按依赖注入（`MDM_ORG_GRPC_ENDPOINT`、`MDM_CURRENCY_GRPC_ENDPOINT`、`MDM_PARTNER_GRPC_ENDPOINT`，装了时还有 `ERP_SALES_GRPC_ENDPOINT`、`INFRA_PRINT_GRPC_ENDPOINT`），不是配置键。账单号格式与打印模板名（`invoice`）本版固定。

## 契约索引

- **系统面**（本仓库 `contract/proto`，模块 `github.com/brickKit/erp-billing/contract/v3`，标签 `contract/v3.…`）：`be.erp.billing.v3.InvoiceService/Read`：任一组件，操作 `erp.billing.system.read`；数据是账单事件的载荷。
- **用户面**：`contract/openapi.yaml`。操作 `erp.billing.invoice.read`、`erp.billing.invoice.draft`（草拟、修改、作废）、`erp.billing.invoice.issue`、`erp.billing.invoice.print`；账单带法人维度。草拟与开具要带 `Idempotency-Key`；取 PDF 答 `application/pdf`。
- **事件**（流 `erp.billing`，schema 在 `contract/events/`）：`erp.invoice.changed.v1`（完整状态），`erp.invoice.issued.v1`（“已出账”，不可遗漏的增量事实，事实身份是账单身份；财务形成应收、信用转换占用都订阅它）。

## 外壳声明

不是外壳。可以被 Go 的核心外壳（`be/go-core`）承载。
