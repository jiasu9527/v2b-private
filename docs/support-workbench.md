# 受限员工工作台

## 使用

1. 先注册一个普通用户账号；不要给它开启旧版 `is_staff` 或管理员权限。
2. 管理员登录后台，在「用户 → 员工管理 → 员工权限」按完整邮箱绑定账号，启用工作台并选择允许指定的目标入口。
3. 不选目标入口时为只读：能查看入口检测、精准查询用户和自己的操作记录，不能指定或恢复入口。
4. 员工从同一个后台地址登录，默认进入「入口状态」，仅有「入口状态 / 精准查询用户 / 我的操作记录」三个页内选项。
5. 管理员在「员工管理 → 全部操作记录」查看全部单用户入口变更。启停员工、调整授权均撤销该员工现有会话，需重新登录。

本模块不改网站用户前端，不要求 App 新增接口。已有 `user/checkLogin` 兼容 query、JSON、form 的 `auth_data`，仅新增 `id`、`email`、`is_support` 身份字段；旧管理员字段保持兼容。

新员工不继承旧版 staff 或 Telegram 运维权限。旧 staff 的用户管理接口也不能读取、修改或批量处理新员工账号（包括已停用员工）；旧 staff 用户详情不返回认证凭据或完整邀请人记录。管理员原有用户管理能力保持不变。

## 入口指定的生效边界

- 自动规则先按套餐节点权限、条件、UA、固定名单和第一命中顺序决定可见节点。
- 人工指定记录键是 **用户 ID + 原规则 ID**，只替换该原规则已下发的正常节点入口地址及地址解析方式；不增加节点、不绕过隐藏、不复制目标规则的附加节点或条件。
- 源规则和目标入口必须拥有相同的完整成员节点集合，不只是用户当前可见节点或两者交集。目标可以是普通 override 规则或固定二分的叶子组。
- 员工必须获授权目标入口；授权不允许编辑入口地址、规则、探针、节点权限或批量分流。
- UA 是查询及提交时的预览上下文，不是人工指定的额外 UA 绑定。其他客户端如果也命中同一原规则，就会采用同一人工入口。
- 目标保存为引用：管理员修改入口地址、解析方式会跟随；停用、删除、节点集合不兼容或临时指定到期时回退自动规则。无效引用未被静默删除，仍可在精准查询中看到并恢复。
- 无期限指定持续到恢复或失效；临时指定支持 1 小时、24 小时、7 天。生效依赖用户下次更新订阅，不会强制切换已建立连接。
- 同时提交用用户行锁及 `version` 校验；`request_id` 相同且内容一致时幂等，不重复变更和写记录。内容不同返回 409。

## 检测首页

- 复用「入口分配」已有 TCP 检测结果，按规范化地址和端口去重；不同域名即便解析为同一 IP 也不合并。
- 主表显示唯一入口，展开查看关联规则/固定组，再展开查看各探针、阈值、检测时间及错误。
- 状态包括正常、部分异常、异常、过期/未知、未监测和停用；异常或未知排前面。不同来源的正常结果不会掩盖异常、过期或未监测结果。
- 页面每 30 秒刷新已有结果，不因刷新触发即时探测或配置变更。
- 覆盖范围为入口分配中普通独立入口、固定分组叶子入口及 original 规则对应原节点；独立「客户端入口」配置和其他未关联节点不在此检测范围。
- 后端只读 DTO 不包含探针密钥、远程登录资料或完整管理员配置。

## API

成功响应包含 `data`，错误响应包含 `message`，敏感响应 `Cache-Control: no-store`。员工接口使用现有登录会话，推荐 `Authorization: Bearer <auth_data>`。写接口仅接受 `Content-Type: application/json`，认证字段不得混入 JSON。

员工接口（独立于管理员路径）：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/support/overview` | 去重入口健康总览 |
| GET | `/api/v1/support/user?query=完整邮箱或ID&ua=可选UA` | 单用户精准查询；不支持列表、模糊或导出 |
| POST | `/api/v1/support/entry` | 指定/恢复单用户入口 |
| GET | `/api/v1/support/operations?page=1` | 本员工实际变更记录，每页 20 条 |

指定请求：

```json
{
  "user_id": 123,
  "source_policy_id": 10,
  "target_key": "policy:20:split-group:30",
  "restore": false,
  "reason": "用户连接排障",
  "expires_at": 0,
  "version": 0,
  "request_id": "8c287040-1d1f-4e56-b6a7-a960eab5d267",
  "ua": ""
}
```

恢复时 `restore=true`、`target_key=""`、`expires_at=0`，并提交当前 `version` 和新的 `request_id`。查询结果包含白名单 `user`、`scopes`、`ua`、`can_restore`。每个 scope 包含 `source_policy_id,name,automatic_entry_host,version,override,targets,nodes,applicable`；只有 `override.active && override.status == "active" && applicable` 才表示当前预览生效，不能只看 active 原始开关。

管理员接口前缀 `/api/v1/{secure_path}`：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/support/staff` | 员工列表和可授权入口 |
| POST | `/support/staff/save` | `{email,enabled,target_keys,version}`；新建 version=0 |
| GET | `/support/operations?page=1` | 全部员工的入口变更记录 |

错误：400 参数/格式；403 会话/角色失效或操作未授权；404 用户不可操作；409 版本或幂等冲突；415 非 JSON；429 限频。精准查询每员工每分钟 30 次，变更每分钟 20 次；限频仅保存每员工每动作一个计数器，不记录查询内容。

## 部署与记录保留

Go 服务启动前自动幂等创建 `v2_support_staff`、`v2_support_request_limit`、`v2_support_entry_override`、`v2_support_operation`。无须手工开旧员工权限。需要更新 Go 二进制及 `public/assets/admin-new` 构建产物，重启后生效。

成功指定或恢复与结构化操作记录同一事务；不写搜索、查看、轮询日志，不存完整请求体或用户凭据。变更记录保留 90 天，由现有每日维护任务分批清理。撤销员工权限不会抹掉历史记录，也不会自动撤销该员工此前为客户做出的有效指定。

## 验证

```sh
cd go-api
go test ./...
go test -race ./internal/admin ./internal/user ./internal/http ./internal/session -run 'Test(Support|ServersSupport|Fresh|CheckLoginPreserves|LegacyStaff|RouterStaff)' -count=1
```

可选真实 SQL/订阅链路验收测试：先准备**一次性测试数据库**并导入 `database/install.pgsql.sql`，再设置 `FOREST_SUPPORT_TEST_DSN` 运行 `go test ./internal/admin -run '^TestSupportPostgresEndToEnd$' -v -count=1`。未设置时跳过；测试会创建并清理自身样例。该测试也可连接兼容 PostgreSQL 协议的隔离 PGlite 实例；此时不能据此声称已验证真实多实例数据库锁竞争。
