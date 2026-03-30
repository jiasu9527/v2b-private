<img src="https://avatars.githubusercontent.com/u/56885001?s=200&v=4" alt="logo" width="130" height="130" align="right"/>

[![](https://img.shields.io/badge/TgChat-@UnOfficialV2board讨论-blue.svg)](https://t.me/unofficialV2board)

## 当前运行方式

- Go runtime
- PostgreSQL
- 单机部署
- 统一入口脚本 `./scripts/appctl`

文档入口:

- 安装文档 `docs/install.md`
- 更新文档 `docs/update.md`

## 一行命令

安装:

```bash
./init.sh
```

更新:

```bash
./update.sh
```

更新前重写 PostgreSQL 配置:

```bash
./scripts/appctl prompt-db
```

旧 MySQL 架构首次升级到 Go + PostgreSQL:

```bash
./update.sh
```

说明:

- 如果仓库里还保留旧 PHP 栈的 legacy `.env`，并且目标 PostgreSQL 还是空库，`./update.sh` 会自动走一次 MySQL -> PostgreSQL 迁移
- 旧 MySQL 源库连接信息直接从 legacy `.env` 读取
- 你真正需要确认/填写的是新的 PostgreSQL 目标配置
- 第一次生成 `.env.go` 时，会顺带把 legacy `.env` 里的 `APP_KEY`、`APP_URL`、`ADMIN_EMAIL`、邮件相关配置带过来

启动:

```bash
./scripts/appctl start
```

前台运行:

```bash
./scripts/appctl run
```

清理旧运行残留:

```bash
./scripts/appctl cleanup
```

导入旧配置到 Go JSON:

```bash
./scripts/appctl migrate-config
```

查看 Go 实际使用的环境文件:

```bash
./scripts/appctl env-file
```

检查当前 Go 部署配置:

```bash
./scripts/appctl doctor
```

节点 API 烟雾测试:

```bash
BASE_URL=http://127.0.0.1:8080 SERVER_TOKEN='your-server-token' NODE_ID=1 NODE_TYPE=vmess ./scripts/smoke-node-api.sh
```

重置/创建管理员:

```bash
ADMIN_EMAIL=admin@example.com ADMIN_PASSWORD='new-password' ./scripts/appctl create-admin
```

创建一套本地 demo 数据:

```bash
./scripts/appctl seed-demo
```

生成 systemd 服务模板:

```bash
./scripts/appctl service-template > /etc/systemd/system/forest-go-api.service
systemctl daemon-reload
systemctl enable --now forest-go-api
```

## 环境变量

至少需要:

```bash
./scripts/appctl init-env
vi .env.go
```

最少配置:

```bash
POSTGRES_DSN='postgres://postgres:password@127.0.0.1:5432/forest?sslmode=disable'
ADMIN_EMAIL='admin@example.com'
ADMIN_PASSWORD='change-me'
```

也可以不写 `POSTGRES_DSN`，改用 `DB_HOST/DB_PORT/DB_DATABASE/DB_USERNAME/DB_PASSWORD`。

说明:

- Go runtime 优先读取 `.env.go`
- `./scripts/appctl init-env` 会优先从 `.env.go.example` 生成 `.env.go`
- 如果没有 `.env.go`，才会回退读取 `.env`
- 当 `.env` 仍然是旧 MySQL/Redis 栈配置时，`appctl` 会忽略它并改用 `.env.go`
- `./scripts/appctl doctor` 可以直接看到当前是否仍在忽略旧 `.env`，以及 PostgreSQL/管理员邮箱是否已配置
- 交互终端里执行 `./update.sh` 时，会先询问是否要更新 PostgreSQL 配置；也可以手动先跑 `./scripts/appctl prompt-db`
- 如果检测到 legacy MySQL `.env` 且目标 PostgreSQL 为空，`./update.sh` 会自动迁移旧 MySQL 数据

## 当前状态

- legacy PHP HTTP 业务路由已由 Go 覆盖，兼容性边界见 `docs/go-api.md`。
- 安装和更新的根入口分别是 `./init.sh` 和 `./update.sh`，内部统一调用 `./scripts/appctl`。
- 旧 MySQL 单库首次升级时，也可以手动执行 `./scripts/appctl migrate-mysql`。
- 单机常驻建议直接使用 `systemd` 配合 `./scripts/appctl service-template`。
- 后台配置已经切到 `config/admin.json` 和 `config/theme/*.json`。
- 仓库中的旧运行树已移除，当前部署链路是 Go + PostgreSQL。
- 宝塔单机部署说明见 `docs/baota-go-single-machine.md`。
- 安装文档见 `docs/install.md`。
- 更新文档见 `docs/update.md`。
- 节点验活文档见 `docs/node-smoke.md`。
- 上线前检查清单见 `docs/go-live-checklist.md`。
- `/monitor/api/stats` 里 `current_jobs=0` 在空闲时是正常的，关键看 `status=running`。
- 本地联调可直接用 `./scripts/appctl seed-demo` 生成后台测试数据。
- 生成演示数据后，可直接用 `BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-api.sh` 校验 admin/user/staff 关键接口。
- 支付联调可直接用 `BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-payment-api.sh` 校验支付方式、订单详情和 checkout。
- 支付回调联调可直接用 `BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-payment-notify.sh` 校验 `EPay`，也可通过 `PAYMENT_GATEWAY=CoinPayments PENDING_TRADE_NO=seed-demo-order-cpay-pending-01` 或 `PAYMENT_GATEWAY=StripeCheckout PENDING_TRADE_NO=seed-demo-order-stchk-pending-01` 校验其他已种子化回调。
- 短时只读压测可直接用 `BASE_URL=http://127.0.0.1:8080 DURATION_SEC=15 CONCURRENCY=8 ./scripts/soak-demo-api.sh` 看错误率、延迟和 RSS 采样；可额外传 `SUMMARY_JSON=/tmp/soak.json`、`MAX_P95_MS=50`、`MAX_RSS_DELTA_KB=2048` 做阈值验收。

## Demo
[Demo_user](https://v2bdemo.v-50.me/)
[Demo_admin](https://v2bdemo.v-50.me/admindashboard)
邮箱和密码可随意输入

## Document
[Click](https://v2board.com)

## Sponsors
Thanks to the open source project license provided by [Jetbrains](https://www.jetbrains.com/)

## Community
🔔Telegram Group: [@unofficialV2board](https://t.me/unofficialV2board)  

## How to Feedback
Follow the template in the issue to submit your question correctly, and we will have someone follow up with you.
