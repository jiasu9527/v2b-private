# AI 入口查询真实目标关联（2026-11-13）

## 复现与根因

生产只读核对发现 5 个轮次反复把规则、节点或猜测的数字作为检测目标 ID。
例如规则 488 + target 2426、规则 490 + target 490、规则 493 + target 1
均持续返回空结果；真实目标分别为 13615664、13618896、12401674。
新建会话第一轮同样出现，不能归因于单纯上下文过长。

原来的纠错提示和第五次重复终止保护限制了循环，但仍要求模型自行找出关联 ID。
规则 533 则属于另一情况：本来源未配监测，但相同 host:port 有其他来源观测，
不能把它说成已配置，也不能只因其目标为空就断言故障。

## 本次改变

- `entry_simulate.monitoring` 自动关联命中规则的当前候选来源、真实检测目标和已有 TCP 观测。
  固定分组按模拟返回的 host 限定；同 host 多叶子仍标为候选，不推断具体叶子。
- `entry_probe_targets` 的原 `targets` 仍严格执行 policy/target 交集。
  空结果另返 `recovery.code`、`same_policy_targets` 及 `policy_status`，
  不自动替换筛选、不扩展成全站查询、不自动发探测。
- 新增 `ReadClientEntryMonitors`，只读已保存目标和观测，不初始化表或刷新目标，
  不读取探针凭据。配置 revision 在读取期间变化时拒绝返回混合版本。
  保存目标与当前来源不符时返回 `target_current=false`、`enabled=false`。
- 来源无监测与相同地址其他来源的参考观测分开；参考要求精确标准化 host + port，
  不用解析后的 IP 代替域名。禁用、离线、过期、未知状态保持原义。
- 关联摘要上限 8KB，整个工具返回仍不超过 30KB，裁剪标明总数与 truncated。
- 防循环阈值不变；终止说明按实际空结果、参数错误或版本冲突分类，
  不再对每次空结果同时提示无关 revision 错误，并引导阅读已返回的关联证据。

## 语义边界

这不是历史实际下发查询，也不是用户所有节点的逐节点模拟。
当前候选监测端口不保证等于订阅连接端口；单次 TCP 成功不等于业务全链路正常。
权限检查、主动探测审批和自动化配置均未放宽；关联失败不抹掉已成功的模拟结果。

## 验证

- SQL mock 验证新读方法首读仅 SELECT、不读探针 Key、不刷新旧目标及 revision 冲突。
- 录制错误 ID 的回归、跨规则目标不混入原筛选、无监测/离线/过期/禁用、
  同 host 不同端口及域名/IP 不误合并、固定组限定、恶意长字符串输出限制。
- 隔离 PostgreSQL + 模拟模型接口验证完整工具链：一次查询即向模型提供真实目标、
  端口和延迟，下一步可结束回答；未生成写动作。普通用户与他人会话无法读取。
- 防循环、探测审批与结果持久化回归；`go build -p 1 ./...`。
- 单元测试（未设置测试 DSN）：`go test -p 1 ./internal/aiops ./internal/admin ./internal/http -count=1`。
- 隔离数据库全量回归发现两项既有失败，并在未修改的 `b8bb9d7e` 归档上重现：
  `TestAIPanelDNSOperationRetryPostgres/temporary_preflight_recovers_within_same_action`
  （results=0 want=1）与 `TestAIPanelRecoveryUnknownReadOnlyPostgres`
  （测试把已登记的只读 `entry_evidence.cases` 当成应被禁止的操作）。
  本次不夹带修改这两个独立问题，相关入口专项回归另行通过。

本次仅提交后端代码；无数据库迁移，不修改线上入口、监测开关或探针配置。
GitHub 推送不等于生产部署，服务器需拉取并重新构建/重启后生效。
