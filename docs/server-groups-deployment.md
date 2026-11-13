# AI 运维服务器分组部署与验证

服务器分组已接入 Go 后端和管理前端，不需要手工复制源码或执行 SQL。既有服务器和 SSH 凭据不变；首次调用 AI 运维接口时会自动为旧库补齐 `group_name`，空值表示“未分组”。

## 更新

在已部署的 V2B 仓库执行一次：

```bash
cd /etc/forest
./update.sh
```

`./scripts/appctl update` 和**新版本** `bash full-update.sh` 走同一更新流程：拉取当前跟踪分支、迁移、编译 Go 与管理前端、重启服务。首次从旧版升级请优先使用 `./update.sh`，不要运行尚未更新的旧 `full-update.sh`。管理前端必须有 Node.js/npm；缺失或编译失败会明确报错，不会把旧页面误报为更新成功。构建产物保存在 `.local/admin-new/releases/`，不会改写 Git 跟踪的 `public/assets/admin/`。**不要运行旧文档中的 `rm -rf ../public/assets/admin/*`。** 详情见 [管理前端更新说明](admin-frontend-update.md)。

## 验证

1. 打开 AI 运维 → 服务器，确认每台服务器有“分组”操作。
2. 为一台旧服务器选择或新建分组，保存后刷新页面，确认标签仍在；再清空分组并保存，确认回到“未分组”。
3. 后台列表按分组筛选，确认数量与标签一致。已有服务器的连接信息、密码和指纹不应因单独修改分组而变化。
4. 若浏览器仍是旧页面，检查管理页引用的 `/assets/admin-new/admin.js?v=<版本>` 是否变化，以及该 URL 是否由 Go 服务提供。前置 Nginx/CDN 不应把 `/assets/admin-new/` 固定代理到旧静态目录。

AI 批量登记服务器时可在文本、JSON、表格中提供 `group` / `group_name` / `分组`，登记后分组会随服务器保存。普通编辑表单可填写分组；移出分组请使用列表中的“分组”操作。
