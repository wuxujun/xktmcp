# MCP 会话认证身份绑定设计

日期：2026-09-17
状态：已确认，待实施

## 背景

`AUTH_TENANTS.allowed_tools` 已扩展到 Wiki Resources：Catalog、Tree、Page 分别复用 `wiki_search`、`wiki_list_tree`、`wiki_get_page` 权限。无权限时 Resource handler 返回 `Resource not found`。

仅把租户主体和 ACL 写入 HTTP 请求 context 仍不足以保护有状态传输。SSE 和 legacy Streamable HTTP 会在初始化时创建持久 MCP session，后续消息继续使用建会话时的 context。若客户端在同一 session ID 上更换 Bearer Token，主体和 ACL 仍来自旧 context，可能导致跨租户读取或错误拒绝。

## 目标

- 将有状态 MCP session 绑定到建立它的认证身份。
- 后续请求只有在认证身份一致时才能进入 MCP SDK handler。
- 保留 Resource handler 的工具级 ACL，形成传输层和业务层两层校验。
- 不保存或记录明文 Token。
- 保持现代无状态 Streamable HTTP、stdio 和非会话端点的既有行为。

## 非目标

- 不替换现有认证器或迁移到 OAuth。
- 不改变 `AUTH_TENANTS` 配置格式。
- 不允许在存量 MCP session 内轮换 Token；轮换后客户端必须重新连接。
- 不修改 MCP SDK 源码或引入新依赖。

## 认证身份

认证器在每次成功认证后向请求 context 写入一个进程内使用的、不透明的会话身份：

- Bearer 认证：使用规范化 Token 的 SHA-256 摘要。
- IP 白名单认证：使用安全解析后的来源 IP 与认证模式生成 SHA-256 摘要。

摘要只用于常量时间相等性判断，不写日志、不返回客户端。认证失败或网络请求缺少可绑定身份时，不得创建或访问有状态 session。stdio 不经过该机制。

## 会话绑定存储

新增并发安全的进程内绑定表：

```text
transport + session ID -> authentication identity
```

传输类型必须纳入 key，避免 SSE 与 Streamable HTTP 的 session ID 命名空间冲突。

绑定规则：

1. 首次观察到新 session 时，以当前认证身份原子写入。
2. 已存在绑定时，使用常量时间比较当前身份与绑定身份。
3. 不一致、当前身份缺失或 session 请求没有绑定时 fail closed，返回 HTTP 403。
4. SSE 流结束或 Streamable HTTP 收到 DELETE 后删除绑定。
5. 绑定表生命周期与进程及 SDK 内存 session 一致，不做跨进程持久化。

## Streamable HTTP

会话绑定中间件放在认证器内部、MCP SDK handler 外部：

```text
HTTP request -> authentication -> session binding -> MCP SDK
```

- 无 `Mcp-Session-Id` 的初始化 POST 可以进入 SDK。
- 中间件包装 ResponseWriter，在 SDK 首次写响应头之前读取响应中的 `Mcp-Session-Id`，并先完成身份绑定，再向客户端发送响应头，避免客户端收到 ID 后立即发请求造成竞态。
- 带 `Mcp-Session-Id` 的 GET、POST、DELETE 必须先通过绑定校验。
- DELETE 完成后清理绑定。
- 现代 `2026-07-28` 无状态处理器不返回 session ID，因此不会创建绑定。
- 包装器必须通过 `Unwrap` 保留 ResponseController 能力，并正确透传 Header、Write、WriteHeader 和 Flush。

## SSE

SSE session ID 由 SDK 写入首个 `endpoint` 事件，后续消息通过 `/messages/?sessionid=...` 发送。

- 建立 `/sse` GET 时，中间件暂存首个 SSE 事件，直到读到完整事件边界。
- 仅接受 SDK 生成的 `endpoint` 事件，从其 URL query 解析 `sessionid`。
- 在把 endpoint 事件发送给客户端之前完成身份绑定，避免竞态。
- 后续 `/messages/` POST 在进入 SDK 前根据 query 中的 `sessionid` 校验身份。
- GET 流结束时删除绑定。
- 首个事件格式非法、超过小型固定上限、缺少 session ID 或无法绑定时终止响应并记录不含 Token 的错误。
- 包装器保持 `http.Flusher` 和 `Unwrap` 能力，不能缓存 endpoint 之后的正常 SSE 数据。

## Resource ACL

Resource handler 继续执行下列映射：

- `wiki://catalog` 需要 `wiki_search`。
- `wiki://tree` 需要 `wiki_list_tree`。
- Page Resource 需要 `wiki_get_page`。
- `allowed_tools=["*"]` 允许全部。
- 非租户请求没有租户 ACL context 时维持既有行为。

无权限时返回 `ResourceNotFoundError`，避免暴露资源是否存在。会话身份不匹配属于传输层安全失败，返回 HTTP 403。

## 并发与错误处理

- 绑定表使用互斥锁保护；比较时复制所需值后释放锁，不在持锁状态调用下游 handler。
- 创建绑定采用原子“首次写入或比较”语义，并发重复初始化不能覆盖已有身份。
- 身份摘要使用 `subtle.ConstantTimeCompare`。
- 日志只包含传输类型、脱敏后的 session ID 和拒绝原因，不包含 Token、摘要或 Wiki 内容。
- 包装器写入失败直接向上返回，不能在绑定失败后继续发送 session ID。

## 测试策略

按 TDD 添加以下测试：

1. 认证器为租户、本地 Token、远程 Token 和 IP 白名单生成稳定身份，且不同凭据身份不同。
2. 绑定表允许同一身份、拒绝不同或缺失身份，并覆盖并发首次绑定。
3. legacy Streamable HTTP：同 Token 可复用 session；切换 Token 返回 403；DELETE 清理绑定。
4. SSE：同 Token 可发送消息；切换 Token 返回 403；GET 结束清理绑定。
5. 高权限 Token 建会话后切换低权限 Token 读取 Page Resource 必须失败。
6. 不同用户但拥有相同工具权限时仍不得复用 session。
7. 现代无状态 Streamable HTTP 不创建绑定且保持现有行为。
8. Resource ACL 映射、通配符和非租户兼容路径继续通过。
9. 运行全量测试、核心包竞态测试、`go vet` 和 Linux amd64 发布构建。

## 兼容性与部署影响

- 不新增环境变量或配置字段。
- Token 轮换会使已有 SSE 或 legacy HTTP session 返回 403；客户端必须重新连接。
- 现代无状态 HTTP 和 stdio 无会话绑定变化。
- 多实例部署仍要求既有的会话粘性；绑定表与 SDK session 都是进程内状态，本设计不新增分布式状态要求。

## 预计修改范围

- `internal/auth/auth.go`、`internal/auth/auth_test.go`：生成并传播不透明认证身份。
- `cmd/server/main.go`：接入两类会话绑定中间件。
- `cmd/server/*_test.go`：绑定表、SSE、legacy 与无状态传输回归测试。
- `internal/server/wiki_resources.go`、对应测试：保留并验证 Resource ACL。
- `README.md`、`docs/release-readiness-audit-20260916.md`：说明会话绑定和发布结论。
