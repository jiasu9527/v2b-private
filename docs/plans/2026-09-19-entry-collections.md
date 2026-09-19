# 入口合集

## 使用

在「用户入口分配 → 合集管理」勾选至少两条未归入合集的规则，点击「合并已选 … 条到合集」，填写名称和统一入口域名/IP。合集默认折叠，编辑合集可统一改入口或增减成员。

- 合集支持普通覆盖入口规则和固定二分叶子；不支持隐藏节点、下发原入口规则。
- 只统一入口，不合并匹配条件、名单、节点、启停、解析设置或备注。
- 「全局排序」完整平铺所有原始规则和叶子；创建、折叠、解散合集不会改变实际优先级。
- 成员若需单独换入口，先移出合集；相同入口下仍可编辑原有其他设置。
- 移出/解散保留当前地址，不删除规则。最后一个成员离开后自动清理空合集。
- 继续二分或转换二分时旧成员退出合集，新 A/B 使用各自填写的入口，可之后重新加入合集。

## 实现边界

独立 `v2_client_entry_collection` 和 `v2_client_entry_collection_member` 保存管理关系；事务内将地址同步写回原规则/叶子的 `entry_host`，订阅查询不变。统一使用全局排序事务锁，并用合集 `version` 拒绝陈旧编辑。单独规则保存接口也在数据库层阻止绕过统一入口。

API 基础路径：`/api/v1/{admin_path}/server/client-entry-user-policy/collection/`

- `GET fetch`：读取合集。
- `POST save`：`{id?,version?,name,entry_host,items:[{kind,id}]}`，新建省略 id/version。
- `POST remove`：`{id,version,item:{kind,id}}`，移出一个成员。
- `POST drop`：`{id,version}`，解散合集。

所有接口仅管理员可用，写接口为严格 JSON。认证参数传 query，不写入 JSON。

## 验证

```sh
cd go-api
go test ./...
go vet ./...
# 仅指向临时、可丢弃且已执行 database/install.pgsql.sql 的 PostgreSQL 数据库：
FOREST_COLLECTION_TEST_DSN='postgres://.../testdb' go test ./internal/admin -run TestClientEntryCollectionsPostgresIntegration -count=1
cd ../admin-src
npm run check
node --test tests/entry-collections.test.mjs
npm run build
```
