# 节点目标观察（默认关闭）

此功能仅记录**已鉴权用户的连接元数据**，不记录网页正文、完整 URL、TLS 明文或订阅凭据。当前 `sensitive_audit` 只记录预设域名规则命中，与本功能不同。

当前支持两种**逐节点**模式：`mode=all` 记录该节点全部已鉴权用户的路由尝试，不改变节点鉴权名单；`mode=allowlist` 只允许指定用户连接专用节点并记录。旧配置未写 `mode` 时默认保留 `allowlist` 语义。**`all` 不是全站总开关**：只有实际经过已升级且已启用观察的 v2node 节点才会产生记录；未升级节点、外部协议节点、直连流量与队列丢失都不在覆盖内。

## 启用顺序

1. **先升级节点程序**到支持 `observation_audit.mode` 的版本，测试一个 v2node 与一个测试账号。新版节点请求配置时带 `X-Observation-Capability: all-v1`；旧节点没有该能力头，后端会拒绝向它下发 `all` 模式配置，避免把空名单误当成撤销全部用户。因此不能先把共享节点配置为 `all` 再升级节点。外部协议（Juicity、Mieru、Naive）不支持本采集。
2. 在后台「入口规则 → 异常排查 → 节点访问观察」查看所有 v2node 的配置及最近路由记录，对受支持节点逐行点击「配置」，选择“该节点全部已鉴权用户”并保存。也可在管理员会话调用 `POST /api/v1/{admin_path}/observation/nodes`：

   ```json
   {"node_type":"v2node","node_id":123,"enabled":true,"mode":"all","allowed_user_ids":[]}
   ```

   响应含**仅出现一次**的 `token`。只把它写入对应节点的本地私密配置／环境变量；后台只存其哈希。**每次保存都会轮换旧 token，需同步更新节点本地 `ObservationToken`**。`GET .../observation/nodes` 不返回 token。节点端令牌未更新期间不会上报，不能把这段空窗解释为用户没有路由尝试。
3. `all` 模式的节点 `/user` 保持正常合资格用户列表，不做白名单过滤；`allowlist` 模式只能用于 `client_entry_only=1` 的专用节点，且即使暂停采集仍保留名单限制。`allowlist` 下传空名单并关闭采集可撤销全部用户。仅控制订阅下发**不是**节点端鉴权白名单。
4. 节点 `/config` 返回 `observation_audit.enable`、`mode`、`allowed_user_ids` 与上报间隔，**不返回 token**。节点端只有同时具备本地 token 和该开关时才采集。
5. 先用测试账号验证域名目标、纯 IP 目标与断网上报重试，再逐个节点开启；现有事件表示路由尝试，不能区分目标连接成功或失败。高流量场景还需监看队列丢弃、上报失败和数据库体积。

## 上报契约

`POST /api/v1/server/UniProxy/observation?node_type=v2node&node_id=123`

请求头：`Content-Type: application/json`、`X-Observation-Token: <per-node-token>`。体限制 64 KiB、每批 1～100 个事件：

```json
{"events":[{"event_id":"4e9dd96f_01","user_id":1001,"occurred_at":1794920000,"client_ip":"203.0.113.10","target_host":"example.com","target_ip":"","target_port":443,"network":"tcp","result":"route_attempt","uplink_bytes":0,"downlink_bytes":0}]}
```

`target_host` 为可见的域名；只有 IP 或嗅探失败时填 `target_ip` 并让 `target_host` 为空。当前只接受 `result=route_attempt`，代表已鉴权用户发起代理路由，**不能证明目标网站连接成功**；流量字段当前必须为 0，不能伪装成已验证用量。`event_id` 在**同一节点**内必须稳定且唯一；上报重试保持同一个 ID。成功响应 `data.inserted` 为新增事件数，已收录的重复事件不再插入。节点端收到 HTTP 200 后方可从有界队列删除事件。

接收端验证节点独立 token、启用状态、用户存在性／白名单（按模式）、字段、时间窗口；每分钟分批清理超过 7 天的事件，避免高流量下单次全表删除。上传路径不使用旧的全节点共享 `server_token`。名单移除或用户删除后，队列里的旧事件返回 HTTP 400，节点会逐条隔离并继续上报有效事件；令牌错误或采集关闭则返回 HTTP 401，节点保留队列并等待配置修正。

## 管理员查询

- `GET /api/v1/{admin_path}/observation/events`：`node_type=v2node`、`node_id`、`user_id`、`from`、`to`、`page`、`page_size` 可选，最多查询最近 7 天，每页最多 100 条。
- `GET /api/v1/{admin_path}/observation/summary`：同样过滤，返回事件数、去重用户数、域名可识别／未知事件数，以及按**去重用户数**排序的前 30 个域名和目标 IP。

不要把共享 CDN、测速、系统更新或正常客户端自动请求直接当成“内鬼”特征。域名未知表示没有可靠域名，不等于没有访问。结论应结合独立监测、下发轮次和对照组；仅单探针超时、同出口 IP 或共同网站均不足以定性。
