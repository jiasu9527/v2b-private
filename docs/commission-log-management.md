# 返佣记录管理

后台入口：**订单管理 → 返佣记录**。可按邀请人 ID、用户 ID、订单号、时间、佣金状态筛选，单条或批量隐藏／恢复。列表合计单位为分，不是用户可提现余额。

## 资金语义

- `delete` 是**软隐藏明细**，不是财务冲正或退款；记录从用户邀请明细中隐藏，管理员仍可查询、恢复。
- 保留原流水 ID、邀请人、金额、创建时间及操作审计；`commission_balance`、账户余额、订单佣金状态一律不变。
- 用户邀请页累计佣金仍按所有原流水统计，不因隐藏减少。累计收入与当前余额本来就可能因提现、划转、退款等不同，不能用前者反推后者。
- 恢复只恢复明细，不再发放佣金。隐藏也不会阻止其他尚未结算订单正常发佣。
- 原有订单退款是独立资金业务，不能用本功能替代；退款可能按原流程删除对应佣金明细，已经生成的清理审计快照仍保留。

订单的 `commission_status` 含义：

| 值 | 含义 | 可人工设置 |
| --- | --- | --- |
| 0 | 待确认，不是“无佣金” | 未结算订单可设置 |
| 1 | 发放中／等待结算任务处理 | 未结算订单可设置 |
| 2 | 已发放 | 由结算流程维护，不可人工设置 |
| 3 | 已驳回 | 未结算订单可设置 |

已结算订单（状态 2、已有实际佣金，或存在任意历史佣金流水，包括隐藏流水）不能重置为待发放，防止重复返佣。未结算订单重复设置同一状态为无操作。**本次没有新增订单佣金状态的批量修改接口**；批量能力是流水隐藏／恢复。

## API 与 AI 操作目录

后台 URL 前缀 `/api/v1/{secure_path}`，全部 JSON POST；直接访问需管理员登录。AI 按既有管理员／员工运维授权调用目录，写操作继续服从当前会话审批方式，支付配置没有开放。

| AI 操作 | HTTP 路径 | 作用 |
| --- | --- | --- |
| `commission_log.list` | `/commission-log/list` | 独立分页、条数、金额合计、`applied_filters` |
| `commission_log.preview` | `/commission-log/preview` | 冻结本次名单，尚未修改流水 |
| `commission_log.delete` | `/commission-log/delete` | 按 ID 隐藏一条；预览必须恰好是该 ID |
| `commission_log.delete_many` | `/commission-log/delete-many` | 隐藏预览冻结的记录 |
| `commission_log.restore` | `/commission-log/restore` | 恢复预览冻结的已隐藏记录 |

### 筛选

`filter` 为对象，所有已提供条件严格 AND。支持 `id` 或 `ids`、`invite_user_id`、`user_id`、精确 `trade_no`、`created_from`（Unix 秒，含）、`created_to`（不含）、`commission_status`（当前关联订单状态）、`visibility`（`active` / `deleted` / `all`）。默认查询正常记录；恢复预览默认已隐藏记录。

未知字段、错误类型、显式 null、空 ID 数组、空订单号、非法范围都会返回 400；不会静默退回全表。`id` 与 `ids` 不能混用。列表 `current` 默认 1，`page_size` 默认 20，最多 100。`total`、`total_amount` 和本页使用相同数据库快照。

```json
{"filter":{"invite_user_id":61624,"created_to":1790812800,"visibility":"active"},"current":1,"page_size":50}
```

### 先预览，再执行

```json
{"filter":{"invite_user_id":61624},"action":"delete","reason":"按管理员要求清理邀请明细展示"}
```

预览返回 `data.preview_hash`、`count`、`total_amount`、`expires_at`、`all_records`、实际筛选及最多 20 条样本。有效期 **10 分钟**，单批最多 **10000 条**；超出时拒绝并要求缩小筛选，不会只处理前 10000 条。

执行时不再接受新筛选，只提交冻结预览：

```json
{"preview_hash":"<真实预览回执>","idempotency_key":"<本次操作唯一且重试不变的键>","confirm":true}
```

单条 `/delete` 还需 `id`。只给 `visibility` 或空筛选视为全范围，**预览与执行都必须分别提供 `confirm_all:true`**。

- `Idempotency-Key` 请求头可替代 JSON 的 `idempotency_key`；同时提供时必须相同，最长 128 字节。
- 同一操作人、同键、同参数重试返回原回执（`replayed:true`），不会重复处理；换参数复用键返回 409。
- 预览绑定操作人且仅可使用一次。新增匹配记录不会扩大已冻结批次；原记录、结算状态或隐藏版本变化则整批拒绝，不部分执行。
- 成功回执包含 `operation_id`、`action`、`count`、`total_amount`、`committed:true`、`balance_changed:false`。
- 网络异常无法确定是否成功时，**先用原键和原参数重试**，不要立刻创建新预览。过期但已经成功的同键请求仍可读取原回执。
- 审计保存操作人、时间、原因和原流水快照；过期未使用预览在下一次预览时清理，已执行审计保留，避免丢失依据。

## 同步修复

- `orders.list`、`users.list` 的 `filter` 数组支持 JSON 和现有表单格式，未知 key／比较条件返回 400，多条件严格 AND，并回显真正使用的 `applied_filters`。时间用 Unix 秒；订单支持 `created_at/updated_at/paid_at`，用户支持 `created_at/updated_at`。
- 错误筛选同样阻止用户批量封禁、删除、导出、群发邮件，不会放大到全站。
- AI 响应中的 `checkout_result`、checkout claim／签名信息整字段隐藏；筛选回显中的 Token／UUID 值也脱敏。用户正常支付接口不变。

## 升级

Go 服务启动时自动执行可重复的增量建表及加列，无需手工清理旧流水。新安装 SQL 包含展示状态列。发布后需实际重启到新 Go 二进制并更新后台静态资源；代码推送不代表线上部署完成。
