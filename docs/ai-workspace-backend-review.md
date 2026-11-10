# AI 工作区：后端接口、兼容审阅与验收边界

审阅日期：2026-11-08。本文记录本地源码对照及隔离测试，**不表示已经在生产 New API 上完成登录、充值、密码修改或付费聊天验收**。

## 1. 上游版本证据

目标版本为 [QuantumNous/new-api v1.0.0-rc.49](https://github.com/QuantumNous/new-api/tree/v1.0.0-rc.49)。审阅时通过 GitHub tag ref 核实其 commit 为：

```text
48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0
```

先前调研快照使用 `8faf1d45e8c101974dc7483324d3d91c8a462ffe`。本次重新下载 rc.49 的下列 12 个文件，与原调研文件逐字节比较，结果全部相同：

```text
controller/user.go                 controller/auth_session.go
controller/oauth.go                controller/topup.go
controller/subscription.go         controller/secure_verification.go
controller/login_verification.go   controller/playground.go
service/auth_session.go            service/security_verification.go
middleware/auth.go                 router/api-router.go
```

这只能证明这些接口实现一致，不能据此断言两个提交的全部源码一致。固定版本依据：

- [OAuth state / callback](https://github.com/QuantumNous/new-api/blob/48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0/controller/oauth.go)
- [登录、当前用户和密码更新](https://github.com/QuantumNous/new-api/blob/48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0/controller/user.go)
- [refresh 会话协议](https://github.com/QuantumNous/new-api/blob/48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0/controller/auth_session.go)
- [充值报价、创建原单与回调](https://github.com/QuantumNous/new-api/blob/48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0/controller/topup.go)
- [Playground 走本人 Relay](https://github.com/QuantumNous/new-api/blob/48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0/controller/playground.go)

## 2. 已核对的真实接口契约

| 能力 | 真实上游契约与实现选择 |
| --- | --- |
| 本站登录连接 | `POST /api/oauth/state` 传 `provider=oidc,intent=login`；Forest 签发当前用户一次性 code，再完成 `GET /api/oauth/oidc`。不支持任意远端用户 ID 冒充。 |
| 登录／refresh DTO | `data.access_token`、`data.access_expires_at`、`data.session.sid`、`data.user`；refresh 来自 `new_api_refresh` cookie。 |
| 刷新会话 | `POST /api/user/auth/refresh`，携带 refresh cookie、`X-Auth-Session` 及合法 Origin；本地使用按账号锁串行刷新。 |
| 身份核对 | `/api/user/self` 的 `id`、`status`、`oidc_id=forest:<本站ID>` 必须匹配。余额为负不等于身份非法，欠费账号仍应可查看余额和充值。 |
| 登录二次验证 | 上游可返回 `require_verification/flow_token/methods`；站内支持 2FA 验证码。Passkey-only 账号需要另行接入浏览器仪式，不能绕过。 |
| 首次密码／改密码 | `/api/verify/methods` 获取要求；支持 OIDC proof、密码 proof、2FA，实际策略以上游为准。`PUT /api/user/self` 使用 `X-Security-Proof`；已有密码必须提供当前原密码。 |
| 密码结果 | 返回新的 access/session。不能查询原密码；站内只能展示自己生成并加密托管、且仍属于当前远端会话的密码。 |
| 余额 | `/api/user/self.quota` 为原始额度，不是人民币分；展示需要 `/api/status` 的 `quota_per_unit/quota_display_type/usd_exchange_rate`。 |
| 划转报价 | `POST /api/user/amount` 返回真实支付价格字符串；输入 `amount` 是上游单位，不可直接填人民币分。组别倍率和预设折扣影响价格。 |
| 划转创建 | 用户确认后 `POST /api/user/pay` 创建本人 Epay 原单，返回签名参数；Forest 通过既有 checkout、余额事务及补通知链路支付。 |
| 聊天 | `/pg/chat/completions` 接受本人 dashboard session，拒绝管理 PAT；进入上游原生 Relay 计费，不再次按消息扣 Forest 余额。 |
| 模型列表 | `/api/user/models` 返回 `[]string`，不提供可靠的模型上下文窗口大小；发送前核对模型属于本人可用列表。 |
| 模型 Key | `/api/token/` 本人列表／创建；`POST /api/token/:id/key` 返回本人完整 key。管理 PAT 不能用于代用户创建模型 Key。 |

## 3. 本地聊天及角色 HTTP 资源

路由前缀为 `/api/v1/user/new-api/`，须使用新鲜的本站用户登录；不提供任意上游路径代理。

| 路由 | 用途 |
| --- | --- |
| `GET conversations` | 会话分页，按更新时间倒序；UI 选择最近一项恢复。 |
| `POST conversations/create` | `title/mode/model/character_id/persona`，角色模式保存私有角色快照。 |
| `GET conversations/detail?id=...` | 会话、最新 100 条消息、继续向前分页游标及上下文限额。 |
| `GET conversations/messages?id=...&before_id=...&page_size=...` | 更早消息；返回项按时间正序，含 `has_more/next_before_id`。 |
| `POST conversations/delete` | 删除本人会话与消息；正在运行时拒绝。 |
| `POST chat/send` | `conversation_id/client_request_id/content/model/max_output_tokens/branch_from_message_id`。 |
| `GET chat/request?client_request_id=...` | 仅查询本人原请求的 `found/status/conversation_id/user_message_id/assistant_message_id/deleted`；找回丢失 meta 的实际分支，不调用上游。 |
| `GET characters`、`GET characters/detail?id=...` | 私有角色分页／详情。 |
| `POST characters/save` | 新建省略 ID／版本；更新须带 `id/expected_version`。 |
| `POST characters/delete` | 删除本人角色；已创建会话保留独立快照。 |
| `POST characters/import` | `{encoding:"json"或"base64",payload:"..."}`，解码后最多 2 MiB。 |
| `GET characters/export?id=...` | 导出 V2 JSON。 |

所有资源查询和写入均限制 `user_id`。SSE 事件格式：

```text
meta  {conversation_id,message_id,client_request_id,request_id,context,limits,replayed}
delta {text}
done  {message,status,finish_reason?,replayed?}
error {code,message}
```

编辑／重新生成使用 `branch_from_message_id` 创建新会话，不覆盖原分支。传用户消息表示编辑该轮；传 AI 消息表示重用前一条用户消息重新生成。空内容只在有效分支请求时有意义。

### 请求生命周期与计费边界

- `(user_id,client_request_id)` 数据库唯一；同请求不同参数返回冲突。
- 相同请求已经完成／中断时，仅重放已有结果，不再次调用付费模型。
- 同一会话只允许一个在途请求；超时／崩溃后标记 `unknown`，不会自动重新生成。
- 断线或停止保存已得到的 `partial` 内容；停止不代表上游没有产生费用。
- 删除会话后保留最小幂等墓碑（请求 ID、参数哈希、原消息 ID、状态和时间），不保留消息正文，防止丢响应后重试再次付费。
- 想主动重新生成，用户必须发起新的请求 ID；这是一笔新的上游推理请求。
- 请求回执查询只读且不返回参数哈希、正文或凭据；未找到明确返回 `found:false`，数据库异常仍返回错误，删除墓碑为 `found:true,deleted:true`。`found:false` 只是查询时的状态，不授权自动重发或更换请求 ID。

### 角色兼容范围

- 支持 Character Card V2 JSON、PNG `tEXt/chara`；校验导入长度、PNG CRC 和尺寸。
- V3（包括 PNG `ccv3`）、递归世界书明确拒绝，不静默降级。
- 未知 `extensions` 仅保留用于往返导出，不执行脚本、正则、工具或网络访问。
- 世界书支持关键词、常驻、启用、优先级、插入顺序和保守字节预算。
- PNG 首版只提取角色文本元数据，不保存头像图片；导出为 JSON。
- 本功能不接入 AI 运维工具、SSH、后台写操作或支付配置。

## 4. 审阅发现及尚存边界

本次审阅指出并已与实现者修正／核对：

1. **负余额被错误识别为非法账号**：上游后结算可能产生负 `quota`。现已允许读取并充值，不以负余额拒绝身份。
2. **显式重新连接未恢复钱包偏好**：现存有效会话的快速返回分支也需要执行用户已选择的连接设置。
3. **到账额度预览陈旧**：确认时需重新校验 `quota_per_unit/quota_display_type`，并核对实际创建原单额度。不能只确认人民币金额不变。
4. **小额报价舍入放大误差**：不能用一次两位小数报价反推线性倍率后只试相邻整数。实现者补充实时远端报价搜索与折扣档位核对。

以下不是本地事务能完全解决的边界，发布说明不可承诺绝对原子性：

- `wallet_only` 是 New API 的**账号全局计费偏好**，连接时设置会影响该用户在 New API 的其他调用。每轮聊天前读取确认，仍不能阻止外部在检查与实际结算之间修改该偏好。
- New API 充值额度换算配置是全局可变状态。报价、建单、最终回调不在同一跨站事务；有待支付订单期间不应修改 `QuotaPerUnit`。若要强保证，应在上游订单增加固定到账额度快照。
- 上游创建充值原单无外部幂等键。未知结果不能盲重建；必须核对本人原单再继续。本地只保证同一确认不重复扣 Forest 余额。
- 模型上下文预算为 24,000 UTF-8 字节保守估算、最多 100 条历史；不是模型 tokenizer 的精确 token 数，也不保证所有小窗口模型支持。默认输出 2,048、最大 8,192 tokens、单次生成 5 分钟，`meta` 明示裁剪数量。
- 复杂角色卡宏、V3、群聊、插件、推理文本回传及任意附件均不在首版范围。

## 5. 本地测试与真实端待验项

已在隔离 PostgreSQL schema 运行：

```sh
FOREST_NEWAPI_TEST_DSN='<disposable-postgresql-dsn>' go test ./internal/newapibridge -count=1
FOREST_NEWAPI_TEST_DSN='<disposable-postgresql-dsn>' go test -race ./internal/newapibridge \
  -run 'Test(Character|Lore|Prompt|WorkspaceSSE|ChatPostgres)' -count=1
```

聊天测试覆盖跨用户、重复 ID、并发、取消、断流、租约恢复、分支、分页、删除墓碑及角色导入预算。假 SSE 和 mock 上游用于验证本地流程，**不替代真实 New API 端到端测试**。

上线前还需使用独立低额度测试账号核验：

1. OIDC 新号创建及现有号连接；公开密码注册关闭时，OIDC 注册开关是否符合预期。
2. 新旧账号邮箱冲突、被冻结账号、2FA 挑战和 Passkey-only 的明确提示。
3. 反代后的安全 refresh：若 `SessionCookieSecure` 开启，应正确配置 New API 可信 HTTPS Origin；仅发 `X-Forwarded-Proto` 不能替代上游 Origin 白名单。
4. 首次密码、已有密码修改、旧密码拒绝、上游修改密码后的托管密码失效。
5. 一次小额报价／确认／到账核对，模拟超时后核对原单；确认负额度用户可以充值。
6. 真实可用模型的 SSE、停止、断线、重载恢复和本人的 quota 消费记录；不使用管理员余额。
7. 验证本人 API Key 的列表、创建、查看、删除，以及跨用户 ID 均不可读。

本审阅未使用生产密钥、未修改生产用户余额、未对线上发起付费聊天。
