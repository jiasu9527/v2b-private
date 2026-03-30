# Go 单机上线检查清单

下面这张清单默认你现在跑的是 Go runtime + PostgreSQL + 单机。

如果你是宝塔装 PostgreSQL、程序自己纯命令跑，先看 `docs/baota-go-single-machine.md`，再按下面清单过一遍。

## 1. 基础配置先过一遍

```bash
./scripts/appctl doctor
```

必须至少确认这几个值是对的：

- `env_exists=true`
- `postgres_configured=true`
- `admin_email_configured=true`
- `legacy_env_ignored` 如果是 `true`，说明旧 `.env` 被正确忽略了

## 2. 先把代码和测试跑通

```bash
./scripts/appctl test
./scripts/appctl build
```

如果这里都不过，不要继续上线。

## 3. 生成并托管 systemd 服务

```bash
./scripts/appctl service-template > /etc/systemd/system/forest-go-api.service
systemctl daemon-reload
systemctl enable --now forest-go-api
systemctl status forest-go-api --no-pager
```

如果你是宝塔装 PostgreSQL、程序自己纯命令跑，这一步就是推荐方式。

## 4. 先看基础健康接口

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/api/_meta/runtime
curl -fsS http://127.0.0.1:8080/monitor/api/stats
```

重点看两点：

- `/readyz` 要返回 HTTP `200`
- `/monitor/api/stats` 里 `status` 要是 `running`

`current_jobs=0` 在空闲时是正常的，不代表队列没启动。关键是 `status=running`，而不是当前作业量必须大于 0。

## 5. 做一次节点接口烟雾测试

```bash
BASE_URL=http://127.0.0.1:8080 \
SERVER_TOKEN='your-server-token' \
NODE_ID=1 \
NODE_TYPE=vmess \
./scripts/smoke-node-api.sh
```

详细参数看 [docs/node-smoke.md](/Users/anan/Documents/v2b/docs/node-smoke.md)。

## 6. 登录后台做人测

- 管理员后台能登录
- 用户管理能打开
- 节点管理能打开
- 支付方式能打开
- 系统状态页不报错

## 7. 切流后盯日志

```bash
tail -f go-api/run/forest-go-api.log
```

如果你用的是 systemd，同时看：

```bash
journalctl -u forest-go-api -n 100 --no-pager
```

## 8. 出问题先查什么

先按这个顺序排：

1. `./scripts/appctl doctor`
2. `curl -fsS http://127.0.0.1:8080/readyz`
3. `curl -fsS http://127.0.0.1:8080/monitor/api/stats`
4. `./scripts/smoke-node-api.sh`
5. `tail -f go-api/run/forest-go-api.log`

这样能很快把问题定位到配置、数据库、队列运行态、节点接口、还是业务日志。
