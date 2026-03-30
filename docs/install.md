# 安装文档

# **V2Board Go Runtime**

- Go
- PostgreSQL
- 单机
- `systemd`

## 安装前准备

- 准备一个空的 PostgreSQL 数据库
- 项目代码已经拉到当前目录

说明：

- 不要求系统预装 Go
- `./init.sh` 和 `./update.sh` 在缺少 Go 时会自动下载本地工具链到项目目录 `.local/go`
- 如果你机器里本来就有 Go，也会优先直接用系统 Go

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

如果你更习惯交互式编号菜单，也可以先运行：

```bash
./menu.sh
```

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
