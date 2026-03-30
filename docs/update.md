# 更新文档

# **V2Board Go Runtime**

## 普通更新

直接执行：

```bash
./update.sh
```

如果这次更新前你要修改 PostgreSQL 配置：

```bash
./scripts/appctl prompt-db
./update.sh
```

如果你想强制 `./update.sh` 先弹出 PostgreSQL 配置步骤：

```bash
FORCE_INTERACTIVE_DB_CONFIG=1 ./update.sh
```

## 旧 PHP + MySQL 首次升级

先不要删旧站点目录里的 legacy `.env`。

然后直接执行：

```bash
./update.sh
```

会自动做这些事：

- 读取旧 MySQL 源库配置
- 如果 PostgreSQL 目标还没配，提示你填写 PostgreSQL
- 初始化 PostgreSQL 表结构
- 自动执行一次 MySQL -> PostgreSQL 数据迁移
- 构建新的 Go 程序

注意：

- 旧 MySQL 连接信息不用手填，直接从 legacy `.env` 读取
- 你真正要确认的是新的 PostgreSQL 目标库
- 目标 PostgreSQL 最好是空库

## 手动迁移命令

如果你想先迁移，再更新：

```bash
./scripts/appctl migrate-mysql
./update.sh
```

## 更新后检查

```bash
./scripts/appctl doctor
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/monitor/api/stats
tail -f go-api/run/forest-go-api.log
```

重点看：

- `postgres_configured=true`
- `/readyz` 返回 `200`
- `/monitor/api/stats` 里 `status=running`

## 旧配置说明

第一次生成 `.env.go` 时，会把 legacy `.env` 里这些共享配置带过来：

- `APP_KEY`
- `APP_URL`
- `ADMIN_EMAIL`
- `ADMIN_PASSWORD`
- 邮件相关配置

所以第一次升级时，旧 `.env` 先别删。

## 相关文档

- 安装文档：`docs/install.md`
- 单机命令总览：`docs/pg-single-command.md`
- 宝塔单机部署：`docs/baota-go-single-machine.md`
- 上线检查：`docs/go-live-checklist.md`
