# 安装文档

# **V2Board Go Runtime**

- Go
- PostgreSQL
- 单机
- `systemd`

## 安装前准备

- 准备一个空的 PostgreSQL 数据库
- 如果你用在线安装脚本，不需要提前把代码拉到当前目录

说明：

- 不要求系统预装 Go
- `install.sh` 会自动拉取或更新仓库，然后继续执行安装
- `./init.sh` 和 `./update.sh` 在缺少 Go 时会自动下载本地工具链到项目目录 `.local/go`
- 如果你机器里本来就有 Go，也会优先直接用系统 Go
- 如果脚本当前只在私有 GitHub 仓库里，匿名 `curl/wget raw` 默认会 `404`，需要先放到公开地址

## 一行命令安装

```bash
bash <(curl -fsSL <public-install-url>/install.sh)
```

或者：

```bash
wget -qO- <public-install-url>/install.sh | bash
```

脚本会自动做这些事：

- 拉取或更新仓库到目标目录
- 初始化 `.env.go`
- 安装全局 `forest` 命令
- 让你填写 PostgreSQL 和管理员信息，或直接读取你传入的环境变量
- 执行全新安装，或者按旧项目路径执行迁移安装

支持的非交互环境变量：

- `FOREST_INSTALL_DIR`
- `FOREST_POSTGRES_DSN`
- `FOREST_ADMIN_EMAIL`
- `FOREST_ADMIN_PASSWORD`
- `FOREST_LEGACY_ROOT`

## 配置环境文件

```bash
./scripts/appctl init-env
vi .env.go
```

最少配置：

```bash
POSTGRES_DSN='postgres://postgres:password@127.0.0.1:5432/forest?sslmode=disable'
ADMIN_EMAIL='admin@example.com'
ADMIN_PASSWORD='change-me'
```

如果你不想写 `POSTGRES_DSN`，也可以改成：

```bash
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=postgres
DB_PASSWORD=your_password
DB_SSLMODE=disable
```

## 安装命令

```bash
./init.sh
```

如果旧 PHP 项目不在当前目录，直接把旧目录路径带上即可一键迁移安装：

```bash
./init.sh /path/to/legacy-v2board
./scripts/appctl install-legacy /path/to/legacy-v2board
```

这个入口会自动做几件事：

- 从旧目录读取 MySQL 源库配置
- 把共享环境变量写入新的 `.env.go`
- 迁移旧后台配置到 `config/admin.json` 和 `config/theme/*.json`
- 初始化 PostgreSQL 并导入旧 MySQL 数据
- 构建 Go 二进制

迁移旧目录时需要这些文件：

- 必须：`旧目录/.env`
- 可选：`旧目录/config/v2board.php`
- 可选：`旧目录/config/theme/*.php`

这些旧文件不需要：

- `vendor`
- `node_modules`
- `storage/logs`
- Redis 数据目录
- Webman / PM2 运行文件

注意：

- 旧 `.env` 里的 MySQL 必须还能连通
- PostgreSQL 目标库最好是空库
- 第一次新建 `.env.go` 时，会优先引导你填写新的 PostgreSQL 目标配置

如果你更习惯交互式编号菜单，也可以先运行：

```bash
./menu.sh
```

如果想装成全局命令：

```bash
./scripts/appctl install-link
forest
```

装好后，任意目录都可以直接执行，不需要先 `cd` 到网站根目录：

```bash
forest install
forest install-legacy /path/to/legacy-v2board
forest update
forest start
forest status
```

新的配置文件位置：

- 运行环境：`.env.go`
- 后台主配置：`config/admin.json`
- 主题配置：`config/theme/*.json`
- 日志：`go-api/run/forest-go-api.log`

## 安装后启动

前台运行：

```bash
./scripts/appctl run
```

常驻运行：

```bash
./scripts/appctl service-template > /etc/systemd/system/forest-go-api.service
systemctl daemon-reload
systemctl enable --now forest-go-api
```

## 安装后检查

```bash
./scripts/appctl doctor
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/monitor/api/stats
```

重点看：

- `postgres_configured=true`
- `/readyz` 返回 `200`
- `/monitor/api/stats` 里 `status=running`

## 相关文档

- 更新文档：`docs/update.md`
- 单机命令总览：`docs/pg-single-command.md`
- 宝塔单机部署：`docs/baota-go-single-machine.md`
- 上线检查：`docs/go-live-checklist.md`
