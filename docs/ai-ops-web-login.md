# AI 运维：自动网页登录与只读查询

在 **AI 运维 → 网页登录** 中填同一个网站的地址、账号密码和要查询的问题。无需提前登记该网站，也不需要人工接管浏览器。输入网站首页或直接登录页皆可；填写同源的目标页面地址后，登录完成会打开该页面，不填则读登录后的落地页。

第一版给后台管理员提供，员工和 Telegram 小程序不可用。

- 自动识别常见账号密码表单、先账号后密码的两步表单，以及明确的同源登录链接。找不到唯一登录流程就停止，可改填直接登录页。不给表单猜密码，也不自动重复提交。
- 仅支持公开 HTTPS 域名的默认 443 端口。跨域单点登录、验证码、扫码、额外验证、私网或本机地址不支持。依赖跨域基础资源的网站可能无法完整渲染。
- 登录后只读取页面可见正文，不操作保存、删除、付款或系统命令；也不从网页正文选择下一步操作。不要填本身会触发变更的页面地址。
- 账号密码仅在本次任务中使用，进程通过标准输入传给浏览器，不发给模型、不写入聊天记录或配置；没有长期保存或复用登录态。提交后清除前端密码框。
- 捕获的可见正文最多约 5,000 字符，脱敏后发送给所选模型配置的主中转，未切换到其它中转；不要选择包含密钥、支付资料或其它不适合发给中转的页面。结果纯文本展示，不进入可执行运维工具的会话。
- 页面本身、网络和验证码由网站控制；系统返回的分析只反映它此次读取到的内容，不证明所有功能都可用。

## 安装和启用

默认关闭。需要 HTTPS 后台、一个已可用的 AI 运维模型、Node.js 20+ 以及项目锁定版本的 Playwright Chromium。浏览器必须使用**专用非 root 用户和 Chromium 沙箱**。不要给浏览器帐号数据库、站点配置、SSH 凭据或其他生产权限。

1. 将仓库中的 `scripts/aiops-web-login/` 放在可信的绝对路径（比如 `/opt/forest-aiops-web-login`）。不要使用可被后台普通用户修改的启动脚本。安装依赖和浏览器：
   ```sh
   cd /opt/forest-aiops-web-login
   npm ci --ignore-scripts
   PLAYWRIGHT_BROWSERS_PATH=0 npx --no-install playwright install chromium
   ```
   Linux 还需安装 Chromium 系统依赖。构建机或安装帐号下载完成后，浏览器运行帐号只需读/执行此处文件。
2. 使用系统管理员维护的固定启动器，把浏览器放到不同于 Forest 服务的专用帐户下。例如 Forest 服务以 root 运行时，在 Linux 下建立 `/usr/local/libexec/forest-aiops-web-login`（root 所有、不可被其他账户改写）：
   ```sh
   #!/bin/sh
   set -eu
   exec /usr/sbin/runuser -u forest-browser -- /opt/forest-aiops-web-login/run
   ```
   `forest-browser` 应是预先创建的无登录权限的专用系统用户。启动器只允许固定的执行器，不使用来自任务的命令、路径或环境变量。如果 Forest 已以非 root 账号运行，管理员可单独配置仅允许此固定命令的降权执行入口；不要授予通用提权权限。
3. 在 Forest 服务的环境文件（通常是仓库根目录 `.env.go`）设置：
   ```dotenv
   AI_OPS_WEB_LOGIN_RUNNER=/usr/local/libexec/forest-aiops-web-login
   ```
   构建和发布后台前端及 Go 服务后，重启 Forest 读取环境配置。若使用反向代理，请保持 HTTPS，并为此接口预留约 145 秒响应时间，不记录请求体。页面的“可用”只表示找到了可执行的启动器；Chromium 依赖和沙箱仍需用测试站点验证。
4. 停用时从环境文件去掉该配置并重启。如果浏览器依赖或沙箱不可用，请修复运行环境，不关闭 Chromium 沙箱。

Forest 后端读取一次目标的 DNS，验证公开地址并固定连接地址；环回代理只接受该域名的 443 HTTPS CONNECT。浏览器另外禁止同源以外请求、Service Worker、WebSocket 和业务写请求。生产部署可以再用平台网络隔离限制浏览器进程只连接本次环回代理，不给它接入私网的通道。

## 验证

```sh
(cd go-api && go test ./internal/weblogin ./internal/aiops ./internal/http -run 'Test(.*WebLogin|ParseTarget|Tunnel)')
(cd admin-src && npm run check && node --test scripts/test-ai-ops-web-login.mjs)
(cd scripts/aiops-web-login && npm test)
(cd scripts/aiops-web-login && PLAYWRIGHT_BROWSERS_PATH=0 FOREST_WEB_LOGIN_TEST_BROWSER=1 npm test)
```

浏览器回归使用本机的模拟 HTTPS 登录站及 CONNECT 代理。它验证普通登录、两步和 JS 登录、目标页只读、密码不出现在结果、跨域/验证码安全停止、以及 307 不重放密码。
