# 管理前端与后端一起更新

执行 `./update.sh` 即可依次拉取当前分支的上游、更新数据库、编译 Go、编译管理前端并重启服务。`./scripts/appctl update` 使用同一流程，不需要再单独 `git pull` 或运行 `npm run build`。首次从旧版脚本升级时，优先运行 `./update.sh`；不要运行尚未更新的旧 `full-update.sh`，它在拉取新代码后仍可能继续执行旧版手工构建步骤。升级完成后，新版 `bash full-update.sh` 也会复用同一流程。

管理前端需要 Node.js/npm。若不存在 npm、TypeScript 检查失败、Vite 编译失败或缺少 `admin.js`/`index.css`，更新会报错且保留上一版前端，不会静默跳过。Go 编译使用只读模块元数据，不会运行 `go mod tidy` 改写 `go.mod`/`go.sum`。首次安装缺少管理前端源码时仍可作为纯 API 部署。

构建产物位于 Git 忽略的 `.local/admin-new/releases/<版本>/`，验证后切换 `.local/admin-new/current`；Go 服务优先经 `/assets/admin-new/` 提供这一版，仓库自带 `public/assets/admin-new/` 是尚未首次构建的备用文件。这样下一次 `git pull --ff-only` 不会因为前端生成文件被改写而失败。不要手工清空 `public/assets/admin/`，那里有另外的受 Git 跟踪资源。

如果前面另有 Nginx/CDN 静态文件规则，须确保 `/assets/admin-new/` 被转发到本项目 Go 服务，而不是直接从磁盘上旧的 `public/assets/admin-new/` 读取。否则 Go 的运行时前端版本虽然更新，浏览器仍会拿到旧文件。检查返回页的 `admin.js?v=<版本>` 与实际请求是否一致。

更新命令只处理 V2B 后端和本仓库管理前端；Forest 用户前端是分离站点，不由这个脚本发布。
