# 订阅实际下发统计

本次只补订阅记录与只读查询，复用现有 `v2_entry_delivery_request`／`v2_entry_delivery_node`。不调整入口分配、二分、探针检测、封禁或候选档案流程，不新增侧栏菜单。

## 使用

在 **订阅防护 → 实际下发统计 · 历史快照与用户明细** 展开查询。默认最近 24 小时、端口 `54101`；端口可清空。展开不自动查询全部用户。

支持组合条件：实际下发 host／port、规则 ID、固定叶子 ID、合集 ID、节点协议与 ID、用户 ID／下发时完整邮箱、UA 包含及完成时间范围。条件为 AND，必须命中同一请求、同一条节点快照；不会用一个节点的规则去匹配另一个节点的 IP。修改条件后点击查询生效，翻页沿用已提交条件。

- **实际下发去重用户**：同一用户拉取多次只算一人。
- **下发请求数**：同一订阅中多个节点共用入口只计一次；按合集统计时用户和请求分别取并集，不相加成员人数。
- **用户明细**：历史用户 ID、下发时邮箱、匹配次数、首次／最近匹配时间；不依赖当前用户仍存在，也不取当前邮箱冒充历史邮箱。
- **下发快照版本**：按历史规则快照、固定叶子、合集、provider、原 host、实际 host／port、来源及解析状态组合。点击版本只读查看该快照的用户，保留原时间和 UA 等条件。

只计 `response_complete=true`、`coverage=complete`、2xx 且包含有效目标节点的记录。拦截、生成失败、短写／写入失败、304、未覆盖完整的响应不计入已确认人数。客户端格式不支持而被过滤掉的节点不会因在候选列表中出现就被计入。

## 边界

1. **快照版本不是新的变更轮次系统**。本次不修改换 IP、二分或合集变更流程，也不生成这些操作的独立轮次 ID。相同配置重新使用、旧版本缺少规则摘要、秒级更新时间碰撞等情况，不能保证区分不同变更轮次。接口返回的 `version` 是查询快照组合的标识，不是生效序号。
2. `first_at` 是所选条件下最早保留的下发记录，不是入口启用时间。要统计明确一次换 IP 到故障之间的范围，应传入已知的 `from`／`to`，而不是把最早记录当作变更时刻。
3. 统计只覆盖保留期内采集成功的记录，原始记录默认 7 天，沿用现有 1～365 天设置。没有当时节点快照的旧访问日志不能补算。`rule_revision` 为空表示历史版本未知；合集未采集的旧记录不会按今天的合集关系回填。
4. 后端节点／DNS 缓存命中仍需经过最终响应采集。CDN、反向代理缓存或客户端本地缓存直接返回时，不到达后端，后端无法统计；304 不发送新订阅正文，不能记作新下发。
5. 服务端成功写入包含入口的响应，不证明客户端收到、导入、使用或连接。无记录不能证明用户从未拿到入口。
6. 不保存订阅正文、完整链接、Token、UUID 或节点密码。只保留现有白名单元数据；UI 与 AI 查询不返回订阅凭据。

## 只读接口

路径相对于现有管理员 API 前缀：

| GET 路径 | 返回 |
| --- | --- |
| `/server/client-entry-user-policy/investigations/delivery-stats` | `users, requests, first_at, last_at` |
| `/server/client-entry-user-policy/investigations/delivery-users` | `items[{user_id,email_at_event,requests,first_at,last_at}], total, current, page_size` |
| `/server/client-entry-user-policy/investigations/delivery-versions` | `items[{version,policy_id,group_id,collection_id,provider_id,rule_revision,source_host,delivered_host,port,source,resolved,users,requests,first_at,last_at}], total, current, page_size` |

共同返回 `{data: ...}`，含 `keep_days, available_from, available_to, warnings, query`。可用时间是全局保留记录范围，不保证这段时间内没有采集缺口。请求失败不能解释成 0 人。

可选参数：`policy_id, group_id, collection_id, provider_id, server_type, server_id, host, port, from, to, ua, account, transport, rule_revision, version, current, page_size`。

- `from`／`to` 为 Unix 秒，开始包含、结束不含；省略则查询保留记录。
- `account` 为精确用户 ID 或完整历史邮箱，UA 为忽略大小写的字面包含，不按 SQL 通配符解释。
- `port` 为精确字符串，可含端口范围。`server_id` 可为负数（附加节点），最好同时传 `server_type`。
- `rule_revision` 为真实记录的 64 位小写 hex；`version` 为版本列表返回的 32 位标识。无需自行计算或用当前规则 revision 替代。
- `transport` 可为 `subscription/server_json/app_clash/forest_runtime/forest_provider`，与客户端 UA 不是同一维度。
- `current` 从 1 开始，`page_size` 默认 20、最多 100，偏移最多 10000；更多记录请缩小范围。
- 三接口总是限定成功完整下发，不能通过 `complete_only=false` 放宽。

沿用管理员／员工认证及禁止缓存设置。AI 操作目录开放 `entry_evidence.delivery_stats`、`entry_evidence.delivery_users`、`entry_evidence.delivery_versions`，仅查询；这些操作也允许安全只读重试。已有其他写操作权限不因本次改动变化。

## 部署与验证

更新 Go 后端与管理端静态资源并重启后端；不需要修改用户前端或客户端，不需要另开采集开关。沿用现有自动 schema 初始化，安装／升级 SQL 同步维护。不能对已有数据库执行包含 DROP 的全新安装 SQL。

测试使用本地模拟请求与一次性 PostgreSQL，不访问业务服务器：

```sh
cd go-api
go test ./...
go vet ./...
# 各库独立，切勿使用生产库；初始化用 database/install.pgsql.sql。
FOREST_ENTRY_EVIDENCE_TEST_DSN='...' go test -race ./internal/entryevidence -count=1
FOREST_DELIVERY_HTTP_TEST_DSN='...' go test -race ./internal/http -run 'TestEntryDeliveryStatisticsHTTPPostgresEndToEnd' -count=1
cd ../admin-src
npm run check
node --test scripts/test-subscription-delivery.mjs
npm run build
```

## 入口分组：历史拉取 IP

在 **节点管理 → 入口分配 → 历史拉取 IP** 选择已有下发地址或直接输入 IP，默认查询最近 7 天，可选择精确到分钟的开始和结束时间（浏览器本地时区，包含开始、不包含结束）。列表读取最终订阅响应中的 `delivered_host`，跨规则、节点和端口按用户 ID 去重，不按当前入口规则回推，也不把客户端后来 DNS 解析出的 IP 当作服务端历史下发。

可分页查看历史用户、拉取时邮箱、首次/最近下发时间和请求次数。已删除用户仍展示历史，但不能加入新固定分组。展开“保存固定名单”，可选择“不二分，全部放一个组”或“平均二分为 A/B 两组”。单组至少 1 人，只需一个入口，默认沿用所选历史 IP；二分至少 2 人，填写 A/B 入口并按 ID 升序均分。两种方式都保存为固定名单，单组之后也可以继续二分。创建不修改原规则，不增加套餐或节点权限，需检查新规则的优先级。

预览保存固定的半开时间窗口和名单哈希。执行时重新验证同一窗口，日志清理、补写或用户删除导致名单变化会拒绝旧预览，需重新查询；查询之后发生的新拉取不会自动加入。固定名单独立保存，不随原始订阅下发日志的 7 天滚动清理而删除。

`GET /server/client-entry-user-policy/investigations/delivery-hosts` 支持现有下发筛选和分页，返回去重 host、历史去重人数和最近下发时间。`split-preview` 新增 `history_host`，可带 `history_from/history_to` 或最近 `minutes`；返回 `snapshot_hash/from/to/user_count`。`split-create` 使用 `history_host/history_from/history_to/snapshot_hash` 和现有节点/入口参数，不能传空快照绕过验证。`history_single_group=true` 仅用于历史名单，创建一个固定叶子且不接受 B 入口；省略或 false 保持原来的双组行为。

原始日志默认保留 7 天；尚未手动设置过的旧默认 30 天配置迁移为 7 天，管理员已经保存的自定义保留期不覆盖。仍可在“入口排查 → 记录保留”设置 1～365 天。不保存完整订阅内容、Token、UUID 或节点密码。服务端完整返回只能证明下发，不能证明客户端已经使用。
