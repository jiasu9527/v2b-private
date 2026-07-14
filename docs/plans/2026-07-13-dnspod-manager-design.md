# DNSPod 域名解析管理设计

## 目标

在现有 Forest 管理后台增加独立的“域名解析”菜单，通过 DNSPod 3.0 API 管理当前账号下已经托管的域名和解析记录。管理员可以查看域名、进入域名查看子域名记录，并完成新增、编辑、删除、暂停和启用操作。

## 范围

- 使用腾讯云 DNSPod API 3.0，服务地址为 `https://dnspod.tencentcloudapi.com`，版本为 `2021-03-23`。
- 使用 `SecretId + SecretKey` 认证。
- 管理已有域名及其解析记录，不实现域名购买、续费和注册。
- 支持 DNSPod 返回的记录类型与解析线路，不在前端维护静态线路全集。
- 支持记录的主机名、类型、线路、记录值、TTL、MX 优先级和权重。

## 架构

浏览器只访问 Forest 管理员 API，不直接请求 DNSPod。Go 后端实现精简的 TC3-HMAC-SHA256 客户端，负责签名、超时、错误转换和响应解析。管理员 API 在完成现有登录与管理员权限校验后调用该客户端。

凭证通过“域名解析”页面的账号设置保存到现有 JSON 配置文件中；`DNSPOD_SECRET_ID` 和 `DNSPOD_SECRET_KEY` 环境变量拥有更高优先级。读取配置状态时只返回是否已配置及脱敏后的 SecretId，SecretKey 永远不返回浏览器。

## 后台交互

域名解析首页显示账号状态、搜索框、刷新按钮、账号设置入口和域名表格。域名表格显示域名、状态、DNS 状态、套餐、记录数和更新时间。点击域名进入记录视图，保留返回域名列表的入口。

记录视图提供搜索、记录类型过滤、新建记录和刷新。编辑表单根据所选记录类型实时读取 DNSPod 支持的解析线路。暂停、启用和删除使用行内操作，危险操作需要二次确认。

## API

- `GET /dns/config`：返回配置状态和脱敏 SecretId。
- `POST /dns/config/save`：保存或清除凭证，并可立即验证。
- `POST /dns/config/test`：验证当前或临时凭证。
- `GET /dns/domain/list`：分页查询域名。
- `GET /dns/record/list`：分页查询指定域名记录。
- `GET /dns/record/types`：读取套餐支持的记录类型。
- `GET /dns/record/lines`：按域名、套餐和记录类型读取线路。
- `POST /dns/record/save`：新增或修改记录。
- `POST /dns/record/delete`：删除记录。
- `POST /dns/record/status`：暂停或启用记录。

## 错误与测试

DNSPod 返回的 `RequestId` 和错误码保留在服务端错误中，并向管理员显示可读消息。请求使用有限超时，不自动重试写操作，避免重复创建记录。

测试覆盖 TC3 签名、DNSPod 错误解析、凭证脱敏、API 参数校验、管理员鉴权、域名与记录响应映射，以及前端 TypeScript 编译和生产构建。
