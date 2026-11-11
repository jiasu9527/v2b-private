# New API 人民币分金额补丁

## 当前展示修复版本：forest-cny.2

`v1.0.0-rc.49-forest-cny.2.patch` 是相对官方 `v1.0.0-rc.49` 的完整补丁，已包含下述 `forest-cny.1` 功能；不要在已打旧补丁的源码上重复应用。
构建时将下面示例的补丁名及版本号换为 `forest-cny.2`。相邻 manifest 记录源码包、二进制与修改文件的校验值。

- 修复模型广场不能解析外层倍率、服务档位和上下文分档而显示巨型公式的问题。
- 卡片显示完整人民币价格区间，支持每百万／每千 Token；详情保留分档条件、缓存价格，原始公式默认折叠。
- 未知表达式不猜价格；互斥请求条件不会组合成不存在的最高价。
- 相比 `forest-cny.1`，只改前端展示，不修改后端计费、模型价格、支付配置或余额。
- 验收：382 项相关前端测试、TypeScript、lint/format；8 个线上文本模型共 192 种条件与原求值器比对；桌面／手机浏览器检查。

## forest-cny.1 基础功能与构建

目标为 QuantumNous/new-api 官方 `v1.0.0-rc.49`，不是本仓库的 Go 后端补丁。
保留上游 AGPL-3.0 许可及署名，提供完整对应源码。不得将部署配置、密钥或数据库混入源码包。

在干净的对应上游源码根目录执行：

```sh
git apply --check /path/to/v1.0.0-rc.49-forest-cny.1.patch
git apply /path/to/v1.0.0-rc.49-forest-cny.1.patch
cd web
bun install --frozen-lockfile
bun run build
cd ..
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags '-s -w -X github.com/QuantumNous/new-api/common.Version=v1.0.0-rc.49-forest-cny.1' \
  -o new-api .
```

源码、产物校验值见相邻 manifest。本站工作区协议说明见 `docs/new-api-integration.md`。

- Epay 新单按人民币整数分报价，最低 ¥1；`amount_fen` 与旧 `amount` 互斥，建单要求 `expected_quote_hash`。
- 新单保存到账额度与金额快照，回调核对签名付款金额，重复回调及人工补单只到账一次。
- 原模型价格、配额单位、旧余额、旧整数 `amount` 订单保持原义；不把充值率直接改成 1。
- 网页充值在纯 Epay 配置下采用人民币分输入。混合 Stripe/Waffo 等配置保留旧共享表单口径，避免仅改显示却误收金额。
- 支付合规开关保持管理员控制；报价与建单都检查开关，不代替管理员确认。

验收：model race、controller 全套、Forest bridge 真实 PostgreSQL race；SQLite、PostgreSQL、MySQL 新装与原版升级共六组真实 HTTP 回调测试；桌面和手机页面的 ¥1／¥1.23／¥10 输入及人民币历史记录。

升级前备份数据库和原二进制。存在新格式待支付单时不得直接退回原版二进制；必须核对并处理原单，不能恢复整库覆盖后续交易。升级其他上游版本时需重新合并补丁和运行资金回归，不能直接覆盖本修改版。
