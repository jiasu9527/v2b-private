# Quantumult X 原生节点订阅

## 兼容修复

原实现没有 Quantumult X 专用输出分支，其 UA 会落到通用的整包 Base64 URI 列表。资源解析器可转换这种内容，但直接作为圈 X 节点资源导入可能提示「内容无效」。这不意味着圈 X 不支持任何 URI 资源；官方也提供明文 SS URI 示例。

更新后，`Quantumult X`、`Quantumult%20X`、`QuantumultX` UA 返回 UTF-8 原生节点资源：每行一个节点，不加 Base64 或 `[server_local]` 节头。流量和到期信息保留在 `subscription-userinfo` 响应头，不生成虚拟提示节点。

也可显式指定 `flag=quantumult%20x`（别名 `qx`、`quanx`、`quantumultx`）。已有查询参数时用 `&` 追加；已有 `flag` 时应替换，而不是再加一个。显式 `flag=clash` 等格式仍然优先于 UA。该参数必须在后端更新后才生效。

## 不改变的行为

- Token 鉴权、UA 封控、限流及原有入口规则照常执行。
- 原始 UA 原样用于入口匹配；格式识别中的 `%20` 兼容不会改写入口规则的匹配值。如果需要同时匹配空格和编码空格版本，可将 UA 关键词设为 `quantumult`。
- 下发历史仅记录本次实际输出的节点和入口，不存订阅正文或凭据。
- 普通 Quantumult（非 X）仍走原通用格式。
- TLS／Reality 严格以节点当前模式为准，普通 TLS 设置中遗留的 Reality 公钥不会自动开启 Reality。

## 能力边界与排查

### AnyTLS 已导入但握手失败，资源解析器转换后正常

旧原生输出曾将「存在 Reality 公钥」也视为启用 Reality。当节点使用普通 TLS，
但 `tls_settings` 仍保留旧 `public_key/short_id` 时，QX 会错误收到
`reality-base64-pubkey` 和 `reality-hex-shortid`；同节点的通用 AnyTLS 分享链接不带这两项，
因此经小火箭格式转换后可能正常。现在仅当前 `tls=2` 模式输出 Reality 参数；
普通 TLS 忽略残留字段，不删除后台设置，不更改 SNI、密码或证书校验选项。

后端部署此修复后，需在 QX 更新对应远程节点资源，让旧配置被重新生成；
仅重启客户端不会修正已缓存的节点参数。明确启用 Reality 的节点不会被一律去掉参数。
回归已覆盖真实订阅 HTTP 路径及小火箭格式对照，但不替代客户设备握手验证。

原生输出根据官方格式映射节点；无法表达的协议、传输或连接参数不伪装成其他协议。若入口规则筛选后没有可输出节点，内容仍为空，不能仅凭 HTTP 200 判断客户端可导入。较新协议也需要相应版本的圈 X 支持。

遇到问题依次检查：

1. 后端确实已更新；请求没有遗留 `flag=clash` 等强制其他格式的参数。
2. HTTP 403/429 或 `X-Subscribe-Guard` 表示鉴权／封控问题，不是节点文本兼容问题。
3. 入口规则是否命中实际 UA，特别是「仅入口分配」节点。
4. 返回正文是否为原生节点行，是否至少有一个客户端版本支持的节点。

本地回归覆盖格式选择、封控不绕过、自定义订阅路径、元信息和下发记录；实际设备及线上请求仍需独立验证。

官方依据：

- <https://github.com/crossutility/Quantumult-X/blob/master/sample.conf>
- <https://github.com/crossutility/Quantumult-X/blob/master/server-complete.snippet>
- <https://github.com/crossutility/Quantumult-X/blob/master/resource-parser.js>
