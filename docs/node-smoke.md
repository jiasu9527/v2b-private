# 节点 API 烟雾测试

`./scripts/smoke-node-api.sh` 用来做 Go 节点接口的只读验活。

- 它会检查基础健康接口：`/healthz`、`/readyz`、`/api/_meta/runtime`、`/monitor/api/stats`
- 它会检查统一节点接口：`/api/v1/server/UniProxy/user`、`/config`、`/alivelist`
- 对 `vmess`、`shadowsocks`、`trojan`，默认还会顺带检查旧客户端兼容接口
- 它不会调用流量上报或在线上报接口，不会主动写用户流量数据

## 依赖

- `curl`

## 必填环境变量

- `BASE_URL`：Go API 地址，例如 `http://127.0.0.1:8080`
- `SERVER_TOKEN`：节点通信 token，对应后台配置里的 `server_token`
- `NODE_ID`：数据库里的节点 ID
- `NODE_TYPE`：节点类型，例如 `vmess`、`v2ray`、`shadowsocks`、`trojan`、`vless`、`tuic`、`hysteria`、`anytls`、`v2node`

## 可选环境变量

- `LOCAL_PORT`：旧兼容配置接口使用的本地端口，默认 `12345`
- `TIMEOUT`：单次请求超时秒数，默认 `8`
- `CHECK_LEGACY_COMPAT`：`1` 表示连旧兼容接口一起测，`0` 表示只测 UniProxy，默认 `1`
- `CURL_BIN`：自定义 `curl` 路径

## 用法

vmess/v2ray:

```bash
BASE_URL=http://127.0.0.1:8080 \
SERVER_TOKEN='your-server-token' \
NODE_ID=1 \
NODE_TYPE=vmess \
./scripts/smoke-node-api.sh
```

trojan:

```bash
BASE_URL=http://127.0.0.1:8080 \
SERVER_TOKEN='your-server-token' \
NODE_ID=2 \
NODE_TYPE=trojan \
./scripts/smoke-node-api.sh
```

只测 UniProxy，不测旧兼容接口:

```bash
BASE_URL=http://127.0.0.1:8080 \
SERVER_TOKEN='your-server-token' \
NODE_ID=3 \
NODE_TYPE=vless \
CHECK_LEGACY_COMPAT=0 \
./scripts/smoke-node-api.sh
```

## 成功输出

成功时会看到一串 `[ok]`，最后一行是：

```text
[ok] node smoke finished
```

## 常见失败

- `readyz: expected HTTP 200`：服务进程起来了，但数据库或依赖还没准备好
- `queue-monitor: response does not contain "status":"running"`：队列运行态没有起来
- `expected HTTP 200, got 500` 且 body 里有 `token is error`：`SERVER_TOKEN` 不对
- `expected HTTP 200, got 500` 且 body 里有 `server is not exist`：`NODE_ID` / `NODE_TYPE` 不匹配数据库里的节点
- `uniproxy-config` 或旧兼容 `config` 失败：节点表配置字段不完整，先去后台节点配置页补齐

## 什么时候用

- 新装完首次验活
- 更新后怀疑节点下发异常
- 迁移旧节点到 Go 运行时后，逐个节点抽检
- 切流前做只读回归
