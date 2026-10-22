# erp-billing 的契约

`erp/billing` 发布的契约包：系统面的 protobuf、事件载荷的 JSON Schema、用户面的 OpenAPI。放在组件仓库的 `contract/` 子目录里单独发布（标签 `contract/v3.…`）。

依据：装配项目 [be-assembly-standard](https://github.com/brickKit/be-assembly-standard) 的协议 05（认领模式、出账组件本地的草稿、正式化的顺序，本组件是出账用法的使用方）、协议 10（打印）、协议 14（单号、金额、列表）；组件的 `docs/design.md` 与 `docs/flows/billing.md`。

## 内容

| 位置 | 是什么 |
| --- | --- |
| `proto/be/erp/billing/v3/billing.proto` | `InvoiceService.Read` |
| `gen/go/be/erp/billing/v3/` | Go 代码，包 `billingv3`；模块 `github.com/brickKit/erp-billing/contract/v3` |
| `python/` | Python 包 `contract-erp-billing`，模块 `be.erp.billing.v3` |
| `events/` | 流 `erp.billing`：账单（完整状态）、已出账（增量事实） |
| `openapi.yaml` | 用户面；每个路由的操作写在 `x-operation` |

## 要点

- **账单身份就是认领身份**：草拟时用它向销售认领，开具时用它确认与正式化。
- **账单号**只在开具（编号事务）时分配，按法人、按年连续；草稿与已确认的作废不占号；已开具的不能作废。
- **已出账**（`erp.invoice.issued.v1`）是不可遗漏的增量事实，事实身份是账单身份，在编号事务里进发件表。
- **打印**：第一次渲染记下模板版本与打印数据，之后照用，第二次起标副本。
- **能力不支持**（D75）：更正与补差、红字、预收抵扣、电子发票，答 `CAPABILITY_UNSUPPORTED`。
