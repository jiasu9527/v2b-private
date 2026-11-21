# Forest AI 运维网页登录执行器

本目录不是对公网提供服务的程序。正式调用只能由 Forest 后端发起：后端会为本次输入的网站固定一个只允许该域名和 443 端口的本地 CONNECT 代理。执行器不接收 AI 生成的脚本、选择器、命令或网络目标。

## 安装运行环境

在可信部署目录中，以运行浏览器的非 root 账号安装：

    cd /绝对路径/v2b/scripts/aiops-web-login
    npm ci --ignore-scripts
    PLAYWRIGHT_BROWSERS_PATH=0 npx --no-install playwright install chromium

Linux 须安装 Playwright 的 Chromium 系统依赖并能启用 Chromium 沙箱。若 Forest 服务以 root 运行，需要通过固定的、部署者控制的降权入口启动此执行器，并保证该账号可访问 Node、Chromium 缓存及本目录。**不要加 `--no-sandbox` 或用 root 跑浏览器。** 建议浏览器使用专用系统用户、私有临时目录及系统级网络隔离（仅放行到本次 Forest 提供的本地代理），不要赋予其他文件、SSH 或生产服务权限。

将 Forest 的执行器路径配置为本目录下 `run` 的绝对路径。需要特定的 Node 或 Chromium 缓存路径时，应由部署者在固定启动器内设置，不能从对话中传入；Forest 不向运行器继承后端密钥环境。

## 能力与边界

- 一次任务一个全新 Chromium 浏览器上下文；工具只接受标准 HTTPS 域名、初始登录地址、同源目标页和一份账号密码。
- 支持唯一可识别的同源登录链接、常见用户名/密码表单，以及普通两步登录。只激活实际选择的登录表单，不重试账号或密码。原生表单只允许提交到观察到的 action，JS 登录则要求可识别的同源认证接口。
- 不处理跨域单点登录、验证码、扫码、MFA、证书错误、多个候选表单和原生 GET 密码表单。没有登录表单或无法确认步骤时停止，不尝试猜测或绕过。
- 登录后只加载指定目标页或登录的落地页，读取有界可见文本，不提供任意点击、下单或保存配置；其它页面请求阻止非读方法。仍应只对确认拥有访问权限的站点使用：如果站点将副作用错误地实现为 GET，浏览器页面读取并不等同于该站点内部绝对没有副作用。
- 不写账号密码、Cookie、浏览器状态或截图到日志，不导出登录态；每次结束销毁上下文。页面文本在给模型前还由 Forest 后端做脱敏。

## 测试

    npm test
    PLAYWRIGHT_BROWSERS_PATH=0 FOREST_WEB_LOGIN_TEST_BROWSER=1 npm test

第二条需要已安装 Playwright Chromium 及 openssl；只用本机生成的 HTTPS 夹具和本机 CONNECT 代理，不访问真实站点。只有夹具会忽略本机自签证书，生产路径严格校验证书。
