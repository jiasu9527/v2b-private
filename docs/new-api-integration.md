# New API 独立站与本站余额接入

本模块连接独立部署的 New API，不把其模型网关、计费表合并进 V2B。方向固定为 **本站普通余额 → 本人 AI 服务额度**，不支持反向提现，不直接划转佣金余额。

Forest 分离前端现在另有 `/ai` 工作区：真实 AI 余额、站内划转、独立密码、流式聊天、V2 角色卡、API Key 和使用记录。需要同时更新 Forest 前端与本仓库 Go 后端；下面的 `/new-api/` 桥接门户仍保留用于 OIDC／Epay 回调与独立确认页。新工作区的上游版本依据、角色兼容范围和验收边界见 [AI 工作区后端说明](ai-workspace-backend-review.md)。

## 设计与资金口径

1. 用户通过本站 OIDC 登录 New API，稳定账号标识为 `forest:<本站用户 ID>`。
   新建账号的 `preferred_username` 使用客户完整邮箱；New API 内部用户名受 20 字符及唯一性约束，超长／冲突时由上游保留内部备用名，不截断邮箱。New API 支持邮箱登录，Forest 工作区以远端账号的实际邮箱作为 `login_account` 展示及复制。已有用户名不批量重命名，稳定 `sub`、用户 ID、余额和历史绑定不变；本站邮箱后来修改时，不冒充远端已同步的邮箱。
2. 用户在 New API 发起充值，选择“本站余额”；New API 先生成自己的充值订单，再以 Epay 签名请求跳转本站确认页。
3. 本站从 New API 只读接口核对原单、金额、用户状态和 `oidc_id`。金额用人民币整数分，不使用前端提交的额度计算扣款。
4. 用户明确确认后，本地事务锁定配置与用户，**扣除 `v2_user.balance` 和写入待到账记录在同一事务完成**。
5. 后台持久任务通知同一张 New API 原单，再回查其状态，确认成功后标记已到账。

不创建套餐订单、不改变套餐、不返佣、不把内部划转再次计作外部充值收入。额度兑换率及充值优惠由 New API 的原充值订单配置决定；上线前需核对其人民币价格与额度显示，不能默认“1 元就是 1 美元额度”。

## 一、V2B 后台配置

更新 Go 后端及随仓库发布的管理端产物后，在 **设置 → New API 接入** 配置：

同页的 **用户聊天** 选项卡供管理员只读查看 Forest 用户的普通/角色会话，支持筛选和消息分页。用户已删除的会话不会保留正文副本，员工不可访问。详见 [管理员聊天记录说明](new-api-admin-conversations.md)。

| 字段 | 说明 |
| --- | --- |
| AI 站地址 | 如 `https://ai.example.com`，HTTPS 根地址 |
| 本站地址 | 一个固定、可由浏览器及 New API 后端访问的 V2B HTTPS 根地址；不得填 AI 站地址 |
| OIDC Client ID | 默认 `forest` |
| OIDC Client Secret | 单独生成的至少 32 字节强随机密钥 |
| 商户号 | 默认 `forest-wallet` |
| 余额支付密钥 | 另一份至少 32 字节强随机密钥，与 AI 站 Epay 配置一致 |
| New API 只读 Token / 用户 ID | New API PAT 及其所属管理员 ID，至少 `profile:read,user:read,billing:read` |
| 单笔最低、最高、滚动 24 小时限额 | 后台以元填写，接口与数据库以分存储；24 小时包含 pending，不按零点重置 |

密钥只写不回显，留空保存保留原值；配置及交易关联凭据使用本站 `APP_KEY` 派生密钥加密。**备份数据库时也必须安全保留原 `APP_KEY`**，不可随意重置。

先关闭接入开关保存、检查只读连接，再配置 New API 两端一致的 OIDC/Epay，最后启用。不需要把 V2B 用户密码、订阅 Token 或 UUID 上传到 AI 站。

后台地址会给出授权、Token、用户信息、Epay 及 OIDC 回调地址。固定本站地址没填时不会生成可复制的相对地址。

## 二、New API 配置

已核对官方 `v1.0.0-rc.49`。后续升级应重跑本文验收，不应假设其他 fork 的支付及 OIDC 行为一致。

### 内置 OIDC（不是通用自定义 OAuth）

必须使用 **内置 OIDC**，使用户记录的 `oidc_id` 为本站下发的稳定 `sub`。通用自定义 OAuth 的其他绑定字段不能替代这个字段。

设本站地址 `https://panel.example.com`，AI 站地址 `https://ai.example.com`：

```text
ServerAddress = https://ai.example.com
RegisterEnabled = true
PasswordRegisterEnabled = false

oidc.enabled = true
oidc.display_name = Forest 本站账号
oidc.client_id = forest
oidc.client_secret = 与本站一致的 OIDC 密钥
oidc.well_known = https://panel.example.com/new-api/.well-known/openid-configuration
oidc.authorization_endpoint = https://panel.example.com/new-api/authorize
oidc.token_endpoint = https://panel.example.com/api/v1/new-api/oauth/token
oidc.user_info_endpoint = https://panel.example.com/api/v1/new-api/oauth/userinfo
```

回调是 `https://ai.example.com/oauth/oidc`。本站只允许配置的这一个回调，不接受任意跳转地址。

`RegisterEnabled` 是包括首次 OIDC 建号在内的总开关，因此不能与密码注册开关一起关闭。关闭其他公开 OAuth、密码注册及不需要的新用户/邀请赠送，保留管理员登录。不要把既有第三方账号仅按相同邮箱强行认领成本站用户。

### Epay 余额支付

```text
支付网关地址 = https://panel.example.com/new-api
商户号 = forest-wallet
商户密钥 = 与本站一致的余额支付密钥
```

New API 会向网关的 `/submit.php` 发出签名表单。支付方式展示为“本站余额”，实际 `type` 应使用此版本 New API 支持的 Epay 支付方式值（如 `alipay`），两端核对原单记录，不另造未经支持的支付 provider。此处不接支付宝、不跳外部收银台，只复用 Epay 原单与幂等回调协议。

在 New API 中配置充值最低金额、人民币/额度换算和可选充值金额，确保它生成的人民币金额在本站限额内。本站不会用管理员写接口直接增加远端 quota。

### 人民币 1:1 划转（Forest CNY 补丁）

官方 `v1.0.0-rc.49` 的旧 Epay `amount` 是整数额度单位，不是人民币元；当 `Price=7.3` 时，`amount=1` 会生成 ¥7.30 的订单。不能只把 `Price` 改为 1，否则会改变现有额度购买力。

`v1.0.0-rc.49-forest-cny.1` 增加人民币分协议，保留模型价格和原配额单位：

- 两站用户均输入人民币，最低 **¥1.00**，可填 **¥1.23、¥10.00** 等两位小数；本站扣除多少人民币，AI 站显示增加多少人民币。管理员配置更高最低额时仍遵守限额。
- `GET /api/user/topup/info` 返回 `supports_cny_amount_fen=true`、`min_amount_fen`；仅 CNY 显示、充值率与显示率相等、无额外充值倍率/折扣时启用。未支持此协议的远端继续走旧报价，不伪造 ¥1 支持。
- `POST /api/user/amount` 传 `{amount_fen}`，返回金额、到账配额、配额单位、币种、换算参数和 `quote_hash`。建单 `/api/user/pay` 传 `{amount_fen,expected_quote_hash,payment_method}`，不得同时传旧 `amount`。
- 新单保存上述快照，回调和补单按原快照到账；新单报价参数变化拒绝支付并要求重新报价，历史旧单保持旧语义。订单残缺快照不能回退旧公式。
- 配额按最近整数取整，当前配额精度下最大误差不超过半个内部 quota；强制校验到账显示四舍五入到分后等于付款人民币金额。不会把 ¥1 显示成 $1，也不会修改模型原价。
- Forest 工作区也须部署支持新协议的后端；New API 独立充值页可直接使用。支付启用和用户确认流程保持不变，测试不代替管理员完成线上支付启用配置。

**两端公网 HTTPS 和回源 HTTPS 都要验证。** 浏览器显示 HTTPS 不代表 CDN 到源站也使用 HTTPS。若公网 301 重复跳回相同 URL，而源站 443 正常，应检查 CDN 回源模式、源站规则、端口及缓存；不要关闭源站 TLS 校验来修复钱包通信。

## 三、用户使用入口

### Forest 站内工作区

更新分离前端后从「AI 服务」进入 `/ai`。接入关闭时不展示正常可用余额；接入开启、尚未连接时显示「连接 AI 账号」。连接使用本站当前登录身份完成 OIDC，只能绑定 `forest:<本人ID>`，不存在让浏览器指定远端账号 ID 的入口。

概览卡主余额来自 New API 的 `quota` 和实际显示单位，旁边的 V2B 余额为人民币分。转入先获得服务端报价，用户确认后才创建本人充值原单并进入既有钱包事务。`pending` 表示已扣本站余额但 AI 端到账待核对，不等于失败，不应再创建相同付款。

账号连接会将本人 New API 的全局计费偏好设为 `wallet_only`；网页聊天通过本人会话调用 New API Playground，外部应用使用本人 API Key，都不使用管理 PAT 推理。模型渠道需在 New API 单独配置，没有可用模型时页面明确提示，不生成演示回复。

独立密码不会复制本站密码，也无法读取外站已经存在的原密码。本站可以生成并加密保存新的 AI 独立密码；显示／修改前需验证当前本站密码，并遵守 New API 的密码、2FA 或 OIDC proof 要求。已有密码可能需要原 AI 密码；Passkey-only 验证首版不能在本站完成。密码查看响应 `no-store`，页面默认隐藏并定时清除。

以下资源统一在 `/api/v1/user/new-api/`，仅接受 Authorization 中的有效本站用户登录。桥接门户的 `nap_` 窄权限会话不能访问工作区：

| 方法／路径 | 用途与参数 |
| --- | --- |
| `GET account` | 本人远端余额、用户名、显示单位、托管密码是否可用；不回传会话凭据。 |
| `POST account/connect` | `{}`；连接本人账号，可返回登录验证挑战。 |
| `POST account/verify` | `{challenge_id,code}`；完成上游 2FA 挑战。 |
| `GET models` | 本人当前可用模型。 |
| `GET keys`、`GET usage` | `current/page_size` 分页；密钥列表不回完整 Key。 |
| `POST keys` | `name/quota/unlimited_quota/expired_time/models/request_id`；同请求不重复创建。 |
| `POST keys/reveal`、`POST keys/delete` | `{id}`；上游按本人身份再校验归属。 |
| `POST transfer/quote` | `{amount_fen,request_id}`；真实费率报价，不扣款。 |
| `POST transfer/confirm` | `{quote_id}`；确认服务器保存的原报价，不能另传金额或用户。 |
| `GET transfer/review` | `quote_id`；只读核对报价状态、已关联划转及未知原单候选。 |
| `POST transfer/resolve` | `{quote_id,remote_trade_no}`；用户确认选定原单后校验并支付，不创建第二单。 |
| `POST password/prepare` | `{panel_password,reset}`；取得本人安全验证方式。 |
| `POST password/confirm` | `{panel_password,challenge_id,method,code?,original_password?}`；生成并保存独立密码。 |
| `POST password/reveal` | `{panel_password}`；只展示本站生成且仍可确认有效的托管密码。 |

聊天、角色、历史及 SSE 字段见单独的工作区说明。写入请求为 JSON 对象，未知字段、跨用户筛选、重复查询参数均拒绝。工作区按用户限流；密码验证另有数据库原子限流。

### 独立桥接门户

可将 `https://panel.example.com/new-api/` 放进用户中心自定义导航或公告。这是内置独立付款/划转记录页面，支持手机及深色显示；并未自动改写所有第三方用户主题的菜单。

流程：

1. 进入 AI 站，选择“Forest 本站账号”登录。
2. 在本站确认授权，返回 AI 站自动关联账号。
3. 在 AI 站发起充值，选择“本站余额”。
4. 本站确认页显示 AI 账号、原充值单号、人民币金额及本站余额。
5. 点击“确认扣除本站余额”，等待原单入账；可查看本站划转记录。

已登录本站的前端可调用：

```http
POST /api/v1/user/new-api/launch
Authorization: Bearer <本站登录凭据>
Content-Type: application/json

{}
```

响应 `data.url` 是带 **一分钟一次性 ticket 的 fragment 链接**。直接跳转该 URL 即可免重复登录本站付款门户，不要把本站 JWT、密码或订阅 Token 拼到 URL。

桥接门户只保存 `nap_` 窄权限会话（30 分钟）；每次访问重新检查原本站会话是否有效及用户状态，不能拿它访问管理员 API。本站密码仅向本站登录接口提交，不发送 New API。退出本门户不等于退出其他 V2B 页面或 New API 页面。

## 四、反向代理与权限

### 模型公开价格（Forest 工作区）

`GET /api/v1/user/new-api/pricing` 使用当前 Forest 登录身份，要求接入已启用；无需先关联 New API 账号。
拒绝请求参数、GET 正文和非 GET 方法，不允许客户端指定上游 URL。后端仅对已配置站点的
`/api/pricing`、`/api/status` 发起只读 GET，不转发管理员 Token、用户会话或 Cookie，不跟随重定向。

返回 `data.source_url`、`fetched_at`、`scope=public_catalog`、`currency=CNY`、
`usd_exchange_rate`、`quota_per_unit` 与 `catalog`。目录保留公开模型价格、公式、分组倍率、
厂商、端点及插件计费元数据；模型数以上游当前返回为准。缺失换算参数为 `null` 并返回警告，
不能补成 1 或免费。金额仅供展示，不写入计费配置；这是公开分组价，不是用户专享费率或调用权限证明。

Forest 前端在概览显示人民币价格及明细。实际扣费仍由 New API 结算；不将按次误称按张，
不能解析的表达式保留原文而不猜价格。新增功能当前只在本地预览验证，需前后端一起更新后才用于用户站点。

若 V2B 使用“只有 `/api/` 代理后端、其余为静态主题”的部署方式，需要额外将 `/new-api` 和 `/new-api/` 代理到同一个 Go 后端；尤其 `/new-api/submit.php` **不是 PHP 文件**，不得交给 PHP-FPM 或被静态 404 规则截获。例如在对应站点使用：

```nginx
location = /new-api {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Real-IP $remote_addr;
}
location ^~ /new-api/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Real-IP $remote_addr;
}
```

示例上游端口需按实际 Go 服务修改；已有统一 `location /` 代理仍要检查 PHP regex 是否抢占。不要通过公网客户端任意提供的转发头认定可信 IP。

管理员配置和全站流水仅当前有效的管理员可用，员工不可修改；用户只能访问自己的付款单和流水。凭据只从 Authorization 读取，不支持 query/body 的 `auth_data`。余额支付与密钥配置不注册进 AI 运维操作目录。

公共入口登录和票据兑换按 IP/端点限流；OIDC Token 交换来自单个 AI 后端，使用独立 300 次/分钟预算，避免误用用户登录的 30 次/分钟预算造成全站登录瓶颈。多实例应配合已有可信反向代理统一限流。

## 五、状态、补通知与密钥轮换

- `awaiting_confirmation`：待用户确认，未扣款。
- `expired`：未付款确认过期，未扣款；回 AI 站重新发起。
- `pending`：本站已扣款，远端成功尚未核实。
- `succeeded`：已核对远端原单成功。

同一目标站和远端订单号唯一；重复点击、重试、并发提交都不会重复扣款。两个不同订单争用同一余额也不能透支。

后台 worker 使用数据库租约和指数退避（最高一小时）；回调超时后回查同一原单，不创建另一笔充值、不立即退款。**远端已入账但 HTTP 响应丢失**不能作为退款依据。禁用接入只停新扣款，既有 pending 仍继续投递。

管理员在划转流水点击“核对 / 补通知”只重排原单处理，记录操作人；没有直接“再次扣款”“删除财务记录”“无条件退款”按钮。问题单应检查远端原单和真实额度状态后单独审计处理。

存在 pending 时拒绝切换 AI 站地址或商户号。相同目标允许轮换商户密钥、只读 Token 和所属管理员 ID，worker 会用新凭据继续核对旧单。切换到另一个实例前，应清空 pending 并独立核对关联账号，不把旧实例订单当成新实例订单。

管理员“付款账号关联”只汇总已创建本站付款确认的用户；不是所有已 OIDC 登录用户的全量目录。

## 六、存储、维护与验收

首次启动自动创建 `v2_newapi_settings`、`v2_newapi_token`、`v2_newapi_checkout`、`v2_newapi_transfer`；短期票据自动清理，资金流水不自动删除。原套餐、佣金表不改动。数据库、配置与 `APP_KEY` 应成套备份；独立 AI 站的数据库也要单独备份。

工作区还创建 `v2_newapi_account`、`v2_newapi_workspace_action`、`v2_newapi_workspace_audit`、`v2_newapi_character`、`v2_newapi_chat`、`v2_newapi_chat_message`、`v2_newapi_chat_request`。远端登录／refresh／托管密码加密保存，角色和聊天文本按本人隔离持久化。用户删除会话会删除正文，保留最小请求幂等墓碑；普通对话不会自动过期。过期的已结束登录／密码挑战清理，资金未知结果及 Key 创建幂等记录不会随其删除。

分离部署时，Forest 的服务端 `API_URL` 仍指向 V2B 后端，不能填模型中转地址。工作区 Fetch SSE 经过前端代理，反代需允许长连接、关闭该流式路径响应缓冲；Go 端发送 15 秒心跳。账户 JSON 接口可用并不能证明流式代理配置正确。升级时优先后端、再前端；新增表采用兼容创建方式，回退程序不应手动删除账户／资金／幂等数据。

自动化测试使用临时 PostgreSQL 与模拟远端：

```sh
cd go-api
FOREST_NEWAPI_TEST_DSN='postgres://.../temporary_test_db?sslmode=disable' \
  go test -race ./internal/newapibridge ./internal/http
```

包括同单 20 路并发、不同订单竞争余额、跨用户、签名篡改、每日限额、配置 CAS、OAuth code 一次性/PKCE、权限隔离、回调丢失响应、凭据轮换、禁用后的补通知及多 worker 租约。未提供 DSN 时 PostgreSQL 测试会跳过；不能把跳过当成完成资金并发验证。

生产开启前用明确的测试账号和最小测试金额验证：

- 公网 HTTPS 无重定向循环；所有登录、回调和余额路径均正确路由。
- 内置 OIDC 首次建号与再次登录；用户 `oidc_id` 正确。
- `只读连接检测` 通过，但该检查本身不证明 OIDC 或支付全链路正常。
- 一笔真实测试原单：本站只扣一次，New API 原单只入账一次，金额/额度换算符合设置。
- 重复刷新、远端暂时不可用再恢复，不能双扣或双加额度。
- 登出/撤销原本站会话、封禁用户及跨账号请求都不可付款。
- 远端模型渠道和价格另行配置；钱包模块可用并不代表已配置可调用的模型。

New API 保留上游 AGPL 许可与署名；独立部署不豁免许可义务。若修改上游程序向网络用户提供服务，应按该版本许可证履行对应源码提供义务。
