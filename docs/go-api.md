# Go Runtime

The runtime entrypoint is now Go-only. Deployment, install, update, start, stop, and test all use `./scripts/appctl`.
Go runtime prefers `.env.go`; legacy `.env` is only used when it does not look like an old MySQL/Redis env file.

## Quick entrypoints

- Install: `./init.sh`
- Update: `./update.sh`
- Runtime ops: `./scripts/appctl <command>`

## Node smoke

Read-only node smoke test:

```bash
BASE_URL=http://127.0.0.1:8080 \
SERVER_TOKEN='your-server-token' \
NODE_ID=1 \
NODE_TYPE=vmess \
./scripts/smoke-node-api.sh
```

- `NODE_TYPE` can be `vmess`/`v2ray`, `shadowsocks`, `trojan`, `vless`, `tuic`, `hysteria`, `anytls`, or `v2node`
- `CHECK_LEGACY_COMPAT=0` skips old compatibility routes and only checks UniProxy
- full usage is documented in `docs/node-smoke.md`

## Queue monitor

`GET /monitor/api/stats` should return `status: running`.
`current_jobs: 0` is normal when the queue is idle; it does not mean the queue failed to start.

## Current Go endpoints

- `GET /healthz`
- `GET /readyz`
- `GET /api/_meta/runtime`
- `GET /api/v1/guest/comm/config`
- `GET /api/v1/guest/plan/fetch`
- `GET /api/v1/guest/invite/preview`
- `POST /api/v1/guest/telegram/webhook`
- `GET/POST /api/v1/guest/payment/notify/{method}/{uuid}`
- `POST /api/v1/passport/comm/pv`
- `POST /api/v1/passport/comm/sendEmailVerify`
- `POST /api/v1/passport/auth/register`
- `POST /api/v1/passport/auth/login`
- `GET /api/v1/passport/auth/token2Login`
- `POST /api/v1/passport/auth/forget`
- `POST /api/v1/passport/auth/getQuickLoginUrl`
- `GET /api/v1/client/app/getVersion`
- `GET /api/v1/client/app/getConfig`
- `GET /api/v1/client/subscribe`
- `GET /api/v2/server/config`
- `GET /api/v1/server/UniProxy/user`
- `GET /api/v1/server/UniProxy/config`
- `GET /api/v1/server/UniProxy/alivelist`
- `POST /api/v1/server/UniProxy/alive`
- `POST /api/v1/server/UniProxy/push`
- `GET /api/v1/server/Deepbwork/user`
- `GET /api/v1/server/Deepbwork/config`
- `POST /api/v1/server/Deepbwork/submit`
- `GET /api/v1/server/ShadowsocksTidalab/user`
- `POST /api/v1/server/ShadowsocksTidalab/submit`
- `GET /api/v1/server/TrojanTidalab/user`
- `GET /api/v1/server/TrojanTidalab/config`
- `POST /api/v1/server/TrojanTidalab/submit`
- `GET /api/v1/user/checkLogin`
- `GET /api/v1/user/info`
- `GET /api/v1/user/getSubscribe`
- `GET /api/v1/user/server/fetch`
- `GET /api/v1/user/telegram/getBotInfo`
- `GET /api/v1/user/plan/fetch`
- `GET /api/v1/user/notice/fetch`
- `GET /api/v1/user/invite/save`
- `GET /api/v1/user/invite/fetch`
- `GET /api/v1/user/invite/details`
- `GET /api/v1/user/getActiveSession`
- `POST /api/v1/user/removeActiveSession`
- `GET /api/v1/user/getStat`
- `GET /api/v1/user/ticket/fetch`
- `POST /api/v1/user/ticket/save`
- `POST /api/v1/user/ticket/reply`
- `POST /api/v1/user/ticket/close`
- `GET /api/v1/user/order/fetch`
- `GET /api/v1/user/order/detail`
- `GET /api/v1/user/order/check`
- `GET /api/v1/user/order/getPaymentMethod`
- `POST /api/v1/user/order/save`
- `POST /api/v1/user/order/checkout`
- `POST /api/v1/user/order/cancel`
- `GET /api/v1/apple-id/products`
- `POST /api/v1/apple-id/orders`
- `GET /api/v1/apple-id/orders`
- `GET /api/v1/apple-id/orders/{trade_no}`
- `POST /api/v1/apple-id/orders/{trade_no}/payment`
- `GET /api/v1/apple-id/orders/{trade_no}/delivery`
- `POST /api/v1/apple-id/orders/{trade_no}/cancel`
- `GET /api/v1/staff/plan/fetch`
- `GET /api/v1/staff/notice/fetch`
- `POST /api/v1/staff/notice/save`
- `POST /api/v1/staff/notice/update`
- `POST /api/v1/staff/notice/drop`
- `GET /api/v1/staff/ticket/fetch`
- `POST /api/v1/staff/ticket/reply`
- `POST /api/v1/staff/ticket/close`
- `GET /api/v1/staff/user/getUserInfoById`
- `POST /api/v1/staff/user/update`
- `POST /api/v1/staff/user/sendMail`
- `POST /api/v1/staff/user/ban`
- `POST /api/v1/staff/user/unban`
- `GET /api/v1/<admin_path>/system/getSystemStatus`
- `GET /api/v1/<admin_path>/config/fetch`
- `POST /api/v1/<admin_path>/config/save`
- `GET /api/v1/<admin_path>/config/getEmailTemplate`
- `POST /api/v1/<admin_path>/config/setTelegramWebhook`
- `POST /api/v1/<admin_path>/config/testSendMail`
- `GET /api/v1/<admin_path>/plan/fetch`
- `POST /api/v1/<admin_path>/plan/save`
- `POST /api/v1/<admin_path>/plan/drop`
- `POST /api/v1/<admin_path>/plan/update`
- `POST /api/v1/<admin_path>/plan/sort`
- `GET /api/v1/<admin_path>/server/group/fetch`
- `POST /api/v1/<admin_path>/server/group/save`
- `POST /api/v1/<admin_path>/server/group/drop`
- `GET /api/v1/<admin_path>/server/route/fetch`
- `POST /api/v1/<admin_path>/server/route/save`
- `POST /api/v1/<admin_path>/server/route/drop`
- `GET /api/v1/<admin_path>/server/manage/getNodes`
- `POST /api/v1/<admin_path>/server/manage/sort`
- `POST /api/v1/<admin_path>/server/manage/updateHost`
- `POST /api/v1/<admin_path>/server/vmess/save`
- `POST /api/v1/<admin_path>/server/vmess/drop`
- `POST /api/v1/<admin_path>/server/vmess/update`
- `POST /api/v1/<admin_path>/server/vmess/copy`
- `POST /api/v1/<admin_path>/server/trojan/save`
- `POST /api/v1/<admin_path>/server/trojan/drop`
- `POST /api/v1/<admin_path>/server/trojan/update`
- `POST /api/v1/<admin_path>/server/trojan/copy`
- `POST /api/v1/<admin_path>/server/shadowsocks/save`
- `POST /api/v1/<admin_path>/server/shadowsocks/drop`
- `POST /api/v1/<admin_path>/server/shadowsocks/update`
- `POST /api/v1/<admin_path>/server/shadowsocks/copy`
- `POST /api/v1/<admin_path>/server/tuic/save`
- `POST /api/v1/<admin_path>/server/tuic/drop`
- `POST /api/v1/<admin_path>/server/tuic/update`
- `POST /api/v1/<admin_path>/server/tuic/copy`
- `POST /api/v1/<admin_path>/server/hysteria/save`
- `POST /api/v1/<admin_path>/server/hysteria/drop`
- `POST /api/v1/<admin_path>/server/hysteria/update`
- `POST /api/v1/<admin_path>/server/hysteria/copy`
- `POST /api/v1/<admin_path>/server/vless/save`
- `POST /api/v1/<admin_path>/server/vless/drop`
- `POST /api/v1/<admin_path>/server/vless/update`
- `POST /api/v1/<admin_path>/server/vless/copy`
- `POST /api/v1/<admin_path>/server/anytls/save`
- `POST /api/v1/<admin_path>/server/anytls/drop`
- `POST /api/v1/<admin_path>/server/anytls/update`
- `POST /api/v1/<admin_path>/server/anytls/copy`
- `POST /api/v1/<admin_path>/server/v2node/save`
- `POST /api/v1/<admin_path>/server/v2node/drop`
- `POST /api/v1/<admin_path>/server/v2node/update`
- `POST /api/v1/<admin_path>/server/v2node/copy`
- `GET /api/v1/<admin_path>/invite/campaign/fetch`
- `POST /api/v1/<admin_path>/invite/campaign/detail`
- `GET /api/v1/<admin_path>/invite/campaign/records`
- `GET /api/v1/<admin_path>/notice/fetch`
- `POST /api/v1/<admin_path>/notice/save`
- `POST /api/v1/<admin_path>/notice/update`
- `POST /api/v1/<admin_path>/notice/drop`
- `POST /api/v1/<admin_path>/notice/show`
- `GET /api/v1/<admin_path>/coupon/fetch`
- `POST /api/v1/<admin_path>/coupon/generate`
- `POST /api/v1/<admin_path>/coupon/drop`
- `POST /api/v1/<admin_path>/coupon/show`
- `GET /api/v1/<admin_path>/giftcard/fetch`
- `POST /api/v1/<admin_path>/giftcard/generate`
- `POST /api/v1/<admin_path>/giftcard/drop`
- `GET /api/v1/<admin_path>/knowledge/fetch`
- `GET /api/v1/<admin_path>/knowledge/getCategory`
- `POST /api/v1/<admin_path>/knowledge/save`
- `POST /api/v1/<admin_path>/knowledge/show`
- `POST /api/v1/<admin_path>/knowledge/drop`
- `POST /api/v1/<admin_path>/knowledge/sort`
- `GET /api/v1/<admin_path>/ticket/fetch`
- `POST /api/v1/<admin_path>/ticket/reply`
- `POST /api/v1/<admin_path>/ticket/close`
- `GET /api/v1/<admin_path>/system/getQueueStats`
- `GET /api/v1/<admin_path>/system/getQueueWorkload`
- `GET /api/v1/<admin_path>/system/getQueueMasters`
- `GET /api/v1/<admin_path>/system/getSystemLog`
- `GET /api/v1/<admin_path>/stat/getStat`
- `GET /api/v1/<admin_path>/stat/getOverride`
- `GET /api/v1/<admin_path>/stat/getOrder`
- `GET /api/v1/<admin_path>/stat/getServerLastRank`
- `GET /api/v1/<admin_path>/stat/getServerTodayRank`
- `GET /api/v1/<admin_path>/stat/getUserLastRank`
- `GET /api/v1/<admin_path>/stat/getUserTodayRank`
- `GET /api/v1/<admin_path>/stat/getStatUser`
- `GET /api/v1/<admin_path>/stat/getRanking`
- `GET /api/v1/<admin_path>/stat/getStatRecord`
- `GET /api/v1/<admin_path>/user/fetch`
- `GET /api/v1/<admin_path>/user/getUserInfoById`
- `POST /api/v1/<admin_path>/user/update`
- `POST /api/v1/<admin_path>/user/setInviteUser`
- `POST /api/v1/<admin_path>/user/generate`
- `POST /api/v1/<admin_path>/user/dumpCSV`
- `POST /api/v1/<admin_path>/user/sendMail`
- `POST /api/v1/<admin_path>/user/ban`
- `POST /api/v1/<admin_path>/user/unban`
- `POST /api/v1/<admin_path>/user/resetSecret`
- `POST /api/v1/<admin_path>/user/delUser`
- `POST /api/v1/<admin_path>/user/allDel`
- `GET /api/v1/<admin_path>/apple-id/product/fetch`
- `POST /api/v1/<admin_path>/apple-id/product/save`
- `POST /api/v1/<admin_path>/apple-id/product/show`
- `POST /api/v1/<admin_path>/apple-id/product/drop`
- `GET /api/v1/<admin_path>/apple-id/inventory/fetch`
- `POST /api/v1/<admin_path>/apple-id/inventory/import`
- `POST /api/v1/<admin_path>/apple-id/inventory/disable`
- `GET /api/v1/<admin_path>/apple-id/order/fetch`
- `GET /api/v1/<admin_path>/apple-id/order/detail`
- `POST /api/v1/<admin_path>/apple-id/order/credentials`
- `POST /api/v1/<admin_path>/apple-id/order/replace`
- `POST /api/v1/<admin_path>/apple-id/order/refund`
- `GET /api/v1/<admin_path>/apple-id/order/audits`

## Apple ID commerce API

Apple ID purchases are an independent business type (`apple_id`). They reuse
the existing login and payment gateway infrastructure, but never create or
fulfill a `v2_order`; payment cannot change a user's plan, traffic, or expiry.
All monetary fields (`price`, `handling_amount`, and `total_amount`) are integer
cents. All timestamps are Unix seconds.

User routes require the normal `Authorization` token:

- `GET /api/v1/apple-id/products` returns enabled products and currently
  available stock.
- `POST /api/v1/apple-id/orders` accepts `product_id`. Send a stable
  `Idempotency-Key` header (or `idempotency_key` field) when retrying a create
  request. Reusing a key with a different `product_id` is rejected. The server
  determines the price and reserves exactly one inventory item for 15 minutes.
- `POST /api/v1/apple-id/orders/{trade_no}/payment` accepts the existing
  checkout fields `method` and optional `token`, and returns the existing
  payment result shape (`type` plus redirect URL or QR data).
- `GET /api/v1/apple-id/orders` and
  `GET /api/v1/apple-id/orders/{trade_no}` only return the authenticated user's
  orders.
- `GET /api/v1/apple-id/orders/{trade_no}/delivery` returns credentials only
  after fulfillment. The response is marked `Cache-Control: no-store` and each
  successful read is audited.
- `POST /api/v1/apple-id/orders/{trade_no}/cancel` cancels only an order still
  pending payment and releases its reserved inventory.

Order status values are `0` pending payment, `1` paid and delivered, `2`
canceled or reservation expired, `3` refund confirmed, and `4` paid but
requiring manual handling. Inventory status values are `0` available, `1`
reserved, `2` sold, and `3` disabled. Payment callbacks are idempotent. Shared
payment verification requires EPay `TRADE_SUCCESS`, Coinbase
`charge:confirmed`/`charge:resolved`, and a settled BTCPay invoice; signed
pending/failed notifications from these gateways do not fulfill either kind
of order. A late payment tries to atomically allocate another available item;
if none exists,
the order moves to status `4` for refund or manual resolution.

Administrator routes use the configured `<admin_path>` and require an admin
session. Product save accepts `name`, `region`, `owned_shadowrocket`, `price`,
`after_sales`, and optional `enabled` (plus `id` when editing). Batch inventory
import uses JSON with one opaque credential record per item:

```json
{
  "product_id": 1,
  "items": [
    {"credential": "buyer@example.com----password----security answer"}
  ]
}
```

The credential string is stored as one complete line; it may contain any
password, security-answer, or note fields without a required separator. The
legacy `account` plus `password` item shape remains accepted for compatibility.
At most 500 inventory rows may be imported per request; duplicate complete
records (case-insensitive) reject the whole batch. Credentials
are encrypted with a key derived from `APP_KEY`; keep `APP_KEY` stable or existing
inventory cannot be decrypted. Normal inventory and order responses expose
only masked accounts and never passwords. The explicit `order/credentials`
and `order/replace` operations record the acting admin in the audit log.
`order/refund` does not call a gateway: it confirms that an external refund has
already completed, changes the business order to status `3`, and writes an
audit entry. Sold or replaced credentials are never returned to sellable stock,
even after a refund. Apple ID orders remain visible to administrators if the
purchasing website user is later deleted; their original `user_id` remains and
`user_email` is empty.

Admin query/action parameters:

| Route suffix | Parameters |
| --- | --- |
| `product/show` | `id`, `enabled` (boolean) |
| `product/drop` | `id`; products with inventory or orders cannot be deleted |
| `inventory/fetch` | Optional `current`, `page_size`, `product_id`, `status` |
| `inventory/disable` | `id`; only available inventory can be disabled |
| `order/fetch` | Optional `current`, `page_size`, `product_id`, `status`, `email`, `trade_no` |
| `order/detail`, `order/credentials` | `id` (Apple ID order ID) |
| `order/replace` | `id`, optional `inventory_id` from the same product, optional `reason`; only delivered orders |
| `order/refund` | `id`, optional `reason` describing the completed external refund |
| `order/audits` | `id`, optional `current`, `page_size` |

Admin lists return `{ "data": [...], "total": 123 }`; pages default to 20
rows and are capped at 200. User lists return `{ "data": [...] }`, and
create/detail/delivery return `{ "data": { ... } }`. The user URL uses
`trade_no` from the create response, not the numeric `id`. Use
`GET /api/v1/user/order/getPaymentMethod` for the existing payment method list.
Checkout redirects return to `/#/apple-id/orders/{trade_no}`, which the website
must implement. Do not use the subscription order status endpoint to poll
Apple ID purchases.

Install/update SQL includes the new tables. Run the normal database update
before deploying the new server. The administrator interface includes an
"独享 Apple ID" navigation item at `/<admin_path>/apple-id`, with product,
inventory, and order tabs. Product prices are entered in yuan and submitted
as integer cents. Inventory import accepts one complete account record per line.
The record may include the account, password, security questions, and other
delivery data without a fixed separator. Empty lines are ignored and the batch
is limited to 500 records. Viewing credentials is an
explicit audited action; closing the order dialog clears the displayed
credentials. Replacement retires the previous account. Refund confirmation
requires the administrator to confirm that the gateway refund has already
completed. The customer website must connect to the user routes separately;
the existing App API contract is unchanged.

## Important boundary

Legacy PHP HTTP business routes now have Go route coverage.

- Runtime fallback to PHP has been removed.
- New regressions should be caught by the full legacy route parity test in `go-api/internal/http/route_parity_test.go`.
- User center `info/stat/subscribe/plan/notice/invite/ticket` and order create/cancel are in Go.
- Payment checkout in Go currently supports all registered gateways in `go-api/internal/payment/forms.go`: `AlipayF2F`, `BEasyPaymentUSDT`, `BTCPay`, `CoinPayments`, `Coinbase`, `EPay`, `EpusdtPay`, `MGate`, `StripeALL`, `StripeAlipay`, `StripeCheckout`, `StripeCredit`, `StripeWepay`, `WechatPayNative`.
- Payment callback notify in Go currently supports the same gateway set.
- Zero-amount or balance-covered order checkout is handled in Go.
- Admin `system/config/plan/user/invite-campaign/notice/coupon/giftcard/knowledge/ticket/order/payment` is now in Go.
- Admin `server/group`, `server/route`, and `server/manage` are now in Go.
- Legacy runtime entry files have been removed from the deployment path.
- Remaining differences are compatibility semantics, not missing business routes. Example: `/api/v1/<admin_path>/system/getQueueMasters` is currently served from the Go queue workload snapshot instead of the old Horizon master list output.
- Staff `plan/notice/ticket/user` is now in Go, with staff-only auth and forced `is_admin=0/is_staff=0` scope on staff user actions.
- `admin_path` defaults to `config/admin.json` `secure_path`, or can be overridden by `ADMIN_PATH`.
- Admin config persistence writes `config/admin.json`.
- Invite campaign admin list/detail/records are now served from Go; campaign setting toggles still go through admin config endpoints.
- Existing deployments can import old config files once with `./scripts/appctl migrate-config` during upgrade.

## Environment

```bash
export APP_NAME=forest
export APP_ADDR=:8080
export PUBLIC_DIR=../public
export POSTGRES_DSN='postgres://user:pass@127.0.0.1:5432/forest?sslmode=disable'
export APP_KEY='base64:xxxx'
export ADMIN_EMAIL='admin@example.com'
export ADMIN_PASSWORD='change-me'
export LOGIN_WITH_MAIL_LINK_ENABLE=false
export EMAIL_VERIFY=false
export INVITE_FORCE=false
export EMAIL_HOST=127.0.0.1
export EMAIL_PORT=25
export EMAIL_FROM_ADDRESS='noreply@example.com'
```

## Commands

```bash
./init.sh
./update.sh
./scripts/appctl cleanup
./scripts/appctl build
./scripts/appctl migrate-config
./scripts/appctl run
./scripts/appctl start
./scripts/appctl stop
./scripts/appctl restart
./scripts/appctl status
./scripts/appctl test
./scripts/appctl prompt-db
./scripts/appctl migrate-mysql
./scripts/appctl create-admin
./scripts/appctl seed-demo
BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-api.sh
BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-payment-api.sh
BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-payment-notify.sh
BASE_URL=http://127.0.0.1:8080 PAYMENT_GATEWAY=CoinPayments PENDING_TRADE_NO=seed-demo-order-cpay-pending-01 CALLBACK_NO=seed-demo-callback-cpay-01 ./scripts/verify-demo-payment-notify.sh
BASE_URL=http://127.0.0.1:8080 PAYMENT_GATEWAY=StripeCheckout PENDING_TRADE_NO=seed-demo-order-stchk-pending-01 CALLBACK_NO=seed-demo-callback-stchk-01 ./scripts/verify-demo-payment-notify.sh
BASE_URL=http://127.0.0.1:8080 DURATION_SEC=15 CONCURRENCY=8 ./scripts/soak-demo-api.sh
SUMMARY_JSON=/tmp/soak.json BASE_URL=http://127.0.0.1:8080 DURATION_SEC=15 CONCURRENCY=8 MAX_P95_MS=50 MAX_RSS_DELTA_KB=2048 ./scripts/soak-demo-api.sh
./scripts/appctl env-file
./scripts/appctl doctor
./scripts/appctl service-template
BASE_URL=http://127.0.0.1:8080 SERVER_TOKEN='your-server-token' NODE_ID=1 NODE_TYPE=vmess ./scripts/smoke-node-api.sh
```

`./scripts/verify-demo-api.sh` logs in with the seeded demo accounts and checks the mixed response shapes used by admin, user, staff, and `/monitor/api/stats`.
`./scripts/verify-demo-payment-api.sh` logs in with the seeded demo accounts and checks payment methods, payment form metadata, pending order detail, and real checkout redirect payload.
`./scripts/verify-demo-payment-notify.sh` logs in, finds the seeded payment config, sends a signed local notify callback, and verifies the order status changes from pending to paid. Demo seed currently covers `EPay`, `CoinPayments`, and `StripeCheckout` for real local notify verification.
`./scripts/soak-demo-api.sh` runs a short read-only concurrent smoke load against health/admin/user endpoints, can emit a JSON summary via `SUMMARY_JSON`, and can fail on `MAX_P95_MS` / `MAX_RSS_DELTA_KB` thresholds for repeatable local checks.

`./scripts/appctl prompt-db` rewrites `DB_*` fields and `POSTGRES_DSN` together; interactive `./update.sh` calls it automatically before the SQL update step when running in a TTY.
`./scripts/appctl migrate-mysql` bootstraps a PostgreSQL schema from `database/install.pgsql.sql` and copies legacy MySQL table data when the repository is upgrading from the old PHP stack.

## Recommended systemd setup

```bash
./scripts/appctl init-env
vi .env.go
./scripts/appctl service-template > /etc/systemd/system/forest.service
systemctl daemon-reload
systemctl enable --now forest
```

This uses `./scripts/appctl run` in foreground mode, which is suitable for `systemd`.
For BaoTa single-machine deployment details, see `docs/baota-go-single-machine.md`.
