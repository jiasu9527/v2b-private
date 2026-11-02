# Go 已公开漏洞核查与修复（2026-11）

核查日期：2026-11-02 UTC（本地 2026-11-01）。范围为当前 Go 后端、AI SSH、运维 CLI、探针及构建工具链；未连接业务服务器，未读取线上凭据或业务数据。这里核查的是官方已经公开的漏洞，不代表确认发生了零日在野攻击，也不保证发现未公开漏洞。

## 基线与修复

核查前 `go.mod` 允许 Go 1.25.0，本地实际编译器为 Go 1.25.7。官方发布 API 当时列出的稳定版本是 Go 1.27.3 / 1.26.10，均于 2026-10-19 发布；1.25 已不在当前两个受支持分支内。本次使用 Go **1.27.3**，不把系统安装版本误当成线上二进制版本。

| 组件 | 原版本 | 修复后 |
| --- | --- | --- |
| Go 最低版本 / 本次实际编译器 | 1.25.0 / 1.25.7 | 1.27.3 |
| `golang.org/x/crypto` | v0.49.0 | v0.58.0 |
| `github.com/jackc/pgx/v5` | v5.9.1 | v5.9.2 |
| `golang.org/x/image` | v0.32.0 | v0.47.0 |
| `golang.org/x/text` | v0.35.0 | v0.43.0 |
| `filippo.io/edwards25519` | v1.1.0 | v1.1.1 |

依赖解析同步更新 `x/sync` 至 v0.24.0，并引入兼容的 `x/sys v0.49.0`。保留无关依赖版本，不重写业务权限、支付、订阅或 SSH 授权逻辑。

### 重点适用性

| 官方漏洞 | 项目证据及判断 | 处理 |
| --- | --- | --- |
| [GO-2026-6609](https://pkg.go.dev/vuln/GO-2026-6609)，CVE-2026-78667：Range 解析资源消耗 | HTTP 静态资源 `http.FileServer`、探针下载 `http.ServeFile` 存在可达路径 | 升级 Go，重编所有程序 |
| [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)，CVE-2026-111943：不可信中间证书 | HTTPS 外联使用标准库证书验证；不能凭源码认定已连接受影响证书 | 升级至含最新证书拒绝列表的 Go |
| [GO-2026-6354](https://pkg.go.dev/vuln/GO-2026-6354)、[GO-2026-6355](https://pkg.go.dev/vuln/GO-2026-6355)：SSH channel 死锁 | AI 运维实际调用 `ssh.NewClientConn` / `NewSession`，恶意 SSH peer 是相关输入边界 | 升级 `x/crypto`，同时覆盖此前 SSH panic、资源消耗等修复 |
| [GO-2026-5004](https://pkg.go.dev/vuln/GO-2026-5004)，CVE-2026-41889：pgx 占位符混淆 SQL 注入 | 版本匹配，但需非默认 simple protocol、特定 dollar-quoted SQL 和可控值。源码默认连接未启用该模式；静态可达不等于已证明业务可利用 | 升级同分支补丁 v5.9.2 |
| [GO-2026-4962](https://pkg.go.dev/vuln/GO-2026-4962)：恶意 SFNT 字体内存消耗 | 字体解析函数可达，但只解析编译嵌入的固定 Noto 字体，没有发现用户上传字体入口 | 升级 `x/image`，一并覆盖其余已知图片解码修复 |
| [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970)、[GO-2026-6629](https://pkg.go.dev/vuln/GO-2026-6629)：Unicode 处理死循环 / panic | 静态分析存在 pgx SCRAM → PRECIS / Unicode 的调用链；具体输入和 profile 前提仍需区分 | 升级 `x/text` |
| [GO-2026-4503](https://pkg.go.dev/vuln/GO-2026-4503)：Edwards25519 错误结果 | 间接依赖版本匹配，本次未发现受影响函数的实际调用链 | 升级补丁 v1.1.1 |

旧标准库还匹配多项 TLS、HTTP、URL、模板、XML 等历史公告，统一由新版 Go 覆盖。没有把所有包级匹配直接当成线上可利用漏洞：例如目前主服务使用明文 `ListenAndServe`，不能仅因 `net/http` 被链接就断言 HTTP/2 服务端漏洞可由生产入口触发；模板包可达也不能直接断言现有模板有 XSS。

## 扫描结论的口径

使用官方 `govulncheck v1.9.0`，漏洞数据库 `https://vuln.go.dev`；本次返回的数据库更新时间为 `2026-10-27T21:16:18Z`。

- 修复前 Linux/amd64 源码扫描有 **73 条版本匹配公告**，其中 **47 条有静态函数可达路径**。这不是 47 个已经验证可利用的业务漏洞，仍受运行平台、协议、配置和输入来源约束。
- 修复后 Linux/amd64 源码扫描**没有包级或函数级可达的已知漏洞**。
- 仍有一条模块级提示 [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)：`x/crypto/openpgp` 不再维护、没有修复版。本项目不导入该包；不能因为同模块中的 SSH 是必需依赖，就把未使用的 OpenPGP 算作未修复的运行漏洞。
- 统计 JSON 中是否有 `finding.trace[].function/package`，而不是把 `-json` 返回码 0 当作零漏洞。普通文本模式遇到可达漏洞会返回失败，扫描加载失败也不能当作通过。

仅代表当前官方数据库与所检查代码／产物的结果，不能保证排除未知零日、业务逻辑漏洞、操作系统、反向代理、PHP 遗留站或独立落地节点软件的问题。

## 构建链加固

- `go.mod` 最低版本提高为 `1.27.3`。旧版本自动工具链升级可正常使用；禁用自动选择且仍用 Go 1.25.7 时，实测构建被拒绝。
- 自动 bootstrap 在解压前核对官方 SHA256，清单覆盖 Linux / Darwin 的 amd64 / arm64。
- 下载、校验、解压失败不会先删除旧工具链；新目录激活失败恢复旧目录。
- 自定义下载内容不能绕过校验。非清单版本须显式提供从独立可信来源核对的 `GO_BOOTSTRAP_SHA256`，不要使用与不可信下载内容一起提供的摘要。
- 不修改机器全局 Go，也不读取／操作线上服务；本次构建产物保留在临时审查目录，不将二进制提交进 Git。

## 更新生效

按现有部署方式执行 `./scripts/appctl update` 或 `./update.sh`，必须让新的 Go 工具链完成构建并重启进程。仅拉取 `go.mod/go.sum`、更新系统 Go 或重启旧二进制，都不会替换已静态编入的旧标准库。

工具链自动下载依赖网络和校验服务。若报版本太旧或下载失败，修复下载环境，或安装经校验的 Go 1.27.3；不要降低 `go.mod` 版本，也不要关闭校验。可在 `go-api` 内执行 `go version` 核对项目实际采用的版本。

主站更新会重编可下载探针，但**远程已运行的探针不会自动变成新版本**：通过既有探针部署流程更新并重启。运维 CLI 也要使用新构建产物；用户前端和 App 不需要修改。

部署后使用 `go version -m <实际运行的二进制路径>` 核对嵌入的 Go 与依赖版本，并确认服务确实重启到该文件。本次未登录线上，不能声称线上已经完成修复。

## 本次验证结果

- `go mod verify`、全部 Go 单元测试、`go vet ./...` 通过。
- Linux amd64 / arm64 及 Darwin arm64 源码扫描通过：无已知包级／函数级可达漏洞，仅保留上述未使用 OpenPGP 的模块提示。
- Linux amd64 / arm64 的 `server`、`ops`、`probe` 共六个程序构建及二进制漏洞扫描通过；嵌入 Go 版本为 1.27.3。后端／CLI 同样只有 OpenPGP 模块级提示，探针没有漏洞提示。
- 一次性 PostgreSQL 17 下的历史订阅记录／HTTP 端到端和 34 个 AI／SSH 相关主测试以 `-race` 通过，无跳过、无 race；临时数据库已停止。
- 四个构建脚本专项测试通过，包括篡改下载、下载失败、解压失败、替换失败时保留旧工具链；官方四平台摘要逐一核对通过。

## 复核命令

```sh
cd go-api
go version
go mod verify
go test ./...
go vet ./...

# 扫描器本身也应由当前工具链编译，避免旧扫描器不认识新 Go 语法。
audit_dir="$(mktemp -d)"
GOTOOLCHAIN=go1.27.3 GOBIN="$audit_dir" go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
GOOS=linux GOARCH=amd64 "$audit_dir/govulncheck" ./...
GOOS=linux GOARCH=arm64 "$audit_dir/govulncheck" ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$audit_dir/server" ./cmd/server
go version -m "$audit_dir/server"
"$audit_dir/govulncheck" -mode=binary "$audit_dir/server"
```

集成测试只能使用一次性本地数据库，不能把真实业务 DSN 传给会重置测试表的用例。

## 官方来源

- [Go 安全政策与受支持版本](https://go.dev/doc/security)
- [Go 发布历史](https://go.dev/doc/devel/release)
- [Go 官方下载与摘要](https://go.dev/dl/?mode=json)
- [Go 漏洞数据库](https://vuln.go.dev/)
- [pgx 官方安全公告](https://github.com/jackc/pgx/security/advisories/GHSA-j88v-2chj-qfwx)
