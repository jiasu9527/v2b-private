# 宝塔单机 Go 部署

这套运行方式现在是：

- Go API
- PostgreSQL
- 单机
- `systemd` 托管进程
- 宝塔只负责站点、Nginx、HTTPS、PostgreSQL

不再需要：

- PHP-FPM
- Redis
- Webman
- PM2

## 1. 宝塔里保留什么

- 安装 PostgreSQL
- 用宝塔站点管理域名和 HTTPS
- Nginx 反向代理到 `127.0.0.1:8080`

程序本体不要再走 PHP 站点运行链路，Go 进程直接由系统命令启动。

## 2. 首次安装

最省事的方式是一行命令直接拉起安装：

```bash
bash <(curl -fsSL <public-install-url>/install.sh)
```

或者：

```bash
wget -qO- <public-install-url>/install.sh | bash
```

脚本会自动安装全局 `forest` 命令，后续就不需要再进入站点目录。
如果脚本当前只在私有 GitHub 仓库里，匿名 `curl/wget raw` 默认会 `404`，需要先放到公开地址。

先生成 Go 环境文件：

```bash
./scripts/appctl init-env
vi .env.go
```

最少改这几个值：

```bash
POSTGRES_DSN='postgres://postgres:password@127.0.0.1:5432/forest?sslmode=disable'
ADMIN_EMAIL='admin@example.com'
ADMIN_PASSWORD='change-me'
```

如果你不想手写 `POSTGRES_DSN`，也可以只填：

```bash
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=postgres
DB_PASSWORD=your_password
DB_SSLMODE=disable
```

然后直接安装：

```bash
./init.sh
```

如果旧 PHP 项目不在当前仓库目录，也可以直接指定旧目录一键迁移安装：

```bash
./init.sh /path/to/legacy-v2board
./scripts/appctl install-legacy /path/to/legacy-v2board
```

旧目录只要求这些内容：

- 必须：`旧目录/.env`
- 可选：`旧目录/config/v2board.php`
- 可选：`旧目录/config/theme/*.php`

不需要把旧目录里的 PHP 运行环境、Redis、Webman、PM2、`vendor`、日志一起搬过来。

## 3. 更新

平时更新就一条命令：

```bash
./update.sh
```

如果你更习惯看菜单操作，也可以直接运行：

```bash
./menu.sh
```

如果你想做成全局命令：

```bash
./scripts/appctl install-link
forest
```

装好后，不需要再进入站点目录，任意目录都可以直接执行：

```bash
forest install
forest install-legacy /path/to/legacy-v2board
forest update
forest start
forest status
```

如果这是旧 PHP + MySQL 架构第一次切到现在这套 Go + PostgreSQL：

- 保留旧站点目录里的 legacy `.env`
- 直接运行 `./update.sh`
- 脚本会自动读取旧 MySQL 源库配置
- 你只需要确认新的 PostgreSQL 目标配置
- 目标 PostgreSQL 是空库时，会自动执行一次 MySQL -> PostgreSQL 数据迁移

如果这次更新前你要顺手改 PostgreSQL 账号、密码、库名，先执行：

```bash
./scripts/appctl prompt-db
./update.sh
```

如果你是直接在交互终端里运行 `./update.sh`，脚本会先问你要不要更新数据库配置。

如果你在某些环境里想强制弹出这个交互步骤：

```bash
FORCE_INTERACTIVE_DB_CONFIG=1 ./update.sh
```

如果你想手动单独跑迁移：

```bash
./scripts/appctl migrate-mysql
```

## 4. 用 systemd 常驻

```bash
./scripts/appctl service-template > /etc/systemd/system/forest-go-api.service
systemctl daemon-reload
systemctl enable --now forest-go-api
systemctl status forest-go-api --no-pager
```

后续常用命令：

```bash
systemctl restart forest-go-api
systemctl stop forest-go-api
journalctl -u forest-go-api -n 100 --no-pager
```

## 5. 宝塔站点怎么配

宝塔站点只要把域名反代到本机 Go 端口即可：

- 反向代理目标：`http://127.0.0.1:8080`
- HTTPS 证书：继续在宝塔里申请和续期
- 静态目录、PHP 版本、PHP 扩展不再是这套运行时的关键路径

## 6. 上线前检查

```bash
./scripts/appctl doctor
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/monitor/api/stats
```

重点看：

- `doctor` 里 `postgres_configured=true`
- `/readyz` 返回 `200`
- `/monitor/api/stats` 里 `status=running`

`current_jobs=0` 在空闲时是正常的，不代表队列没启动。

## 7. 出问题先查哪里

```bash
tail -f go-api/run/forest-go-api.log
journalctl -u forest-go-api -f
```

再配合：

```bash
./scripts/appctl doctor
BASE_URL=http://127.0.0.1:8080 SERVER_TOKEN='your-server-token' NODE_ID=1 NODE_TYPE=vmess ./scripts/smoke-node-api.sh
```

相关说明：

- 安装流程见 `docs/install.md`
- 更新流程见 `docs/update.md`
- 命令总览见 `docs/pg-single-command.md`
- Go 路由与兼容性边界见 `docs/go-api.md`
- 上线检查清单见 `docs/go-live-checklist.md`
