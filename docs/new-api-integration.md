# New API 独立站与本站余额接入

本模块连接独立部署的 New API，不把其模型网关、计费表合并进 V2B。方向固定为 **本站普通余额 → 本人 AI 服务额度**，不支持反向提现，不直接划转佣金余额。

## 设计与资金口径

1. 用户通过本站 OIDC 登录 New API，稳定账号标识为 `forest:<本站用户 ID>`。
2. 用户在 New API 发起充值，选择“本站余额”；New API 先生成自己的充值订单，再以 Epay 签名请求跳转本站确认页。
3. 本站从 New API 只读接口核对原单、金额、用户状态和 `oidc_id`。金额用人民币整数分，不使用前端提交的额度计算扣款。
4. 用户明确确认后，本地事务锁定配置与用户，**扣除 `v2_user.balance` 和写入待到账记录在同一事务完成**。
5. 后台持久任务通知同一张 New API 原单，再回查其状态，确认成功后标记已到账。

不创建套餐订单、不改变套餐、不返佣、不把内部划转再次计作外部充值收入。额度兑换率及充值优惠由 New API 的原充值订单配置决定；上线前需核对其人民币价格与额度显示，不能默认“1 元就是 1 美元额度”。

## 一、V2B 后台配置

更新 Go 后端及随仓库发布的管理端产物后，在 **设置 → New API 接入** 配置：

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

**两端公网 HTTPS 和回源 HTTPS 都要验证。** 浏览器显示 HTTPS 不代表 CDN 到源站也使用 HTTPS。若公网 301 重复跳回相同 URL，而源站 443 正常，应检查 CDN 回源模式、源站规则、端口及缓存；不要关闭源站 TLS 校验来修复钱包通信。

## 三、用户使用入口

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
