# 认证边界加固设计

[English](2026-08-30-auth-boundary-hardening-design.md)

日期：2026-08-30

## 1. 目标

在不破坏现有 n8n 部署兼容性的前提下，加固第一阶段认证边界。现有部署可能使用共享 `AUTH_TOKEN`、IP 白名单或 stdio，并通过信封字段传入 `userId`。

本阶段处理五个相互关联的缺陷：

1. 请求提供的 `userId` 当前可以覆盖远程认证得到的身份。
2. 租户 ACL 检查会在 64 KiB 处静默截断请求体，并在解析失败时跳过授权。
3. 远程认证缓存没有容量上限，也不会主动删除过期条目。
4. 有状态会话到用户的映射没有完整的生命周期清理机制，同时无法解决无状态 HTTP 的身份传递问题。
5. 显式配置但无效的远程认证地址仍会被视为已启用的认证方式。

POST 重试安全和 RAG 响应契约修复不属于本阶段范围。

## 2. 兼容性与信任模型

服务将可信认证主体与不可信的路由 `userId` 明确区分。

可信主体来源包括：

- Token 远程验证成功，并且响应包含 `userid`。
- `AUTH_TENANTS` 条目显式配置了 `user_id`。

仅用于路由的身份来源包括：

- MCP 工具信封字段 `userId`。
- HTTP 查询参数 `userId`。

共享 `AUTH_TOKEN`、IP 白名单或 stdio 认证方式无法证明具体用户身份。为保持向后兼容，这些模式仍可使用仅用于路由的 `userId`，但该值不得被描述或用作安全边界。

存在可信主体时：

- 工具执行、缓存隔离、Wiki 路由和审计日志均以该主体为权威身份。
- 请求中的 `userId` 与可信主体一致时允许执行。
- 请求中的 `userId` 与可信主体冲突时，HTTP 层返回 403；非 HTTP 防御层返回 MCP 工具错误。
- 请求没有提供 `userId` 时，在 MCP SDK 处理调用之前注入可信主体。

## 3. 选定架构

### 3.1 HTTP 主体绑定

认证中间件将在认证完成后、请求交给 MCP SDK 之前，把可信主体直接绑定到 `tools/call` 请求体。

选择该方案的原因是 MCP SDK 会将 HTTP 请求 Context 分离，并且现代无状态 Streamable HTTP 不提供稳定的 `Mcp-Session-Id`。把主体绑定到工具参数，可以同时适配有状态 Streamable HTTP、无状态 Streamable HTTP 和 SSE 消息 POST。

请求处理流程如下：

1. 对包含 Body 的 POST 请求，最多读取 4 MiB，再额外读取一个字节用于判断是否超限。
2. Payload 超过 4 MiB 时返回 HTTP 413。
3. 检查完成后恢复完整且字节一致的 Body，确保下游 MCP 处理收到完整请求。
4. 对请求执行认证，生成包含可选可信主体和可选租户的认证决策。
5. 对租户 ACL 或主体绑定所需的合法 JSON-RPC Payload 进行解析。
6. 对租户的 `tools/call` 请求，必须可靠提取工具名，并以 fail-closed 方式执行 `allowed_tools` 检查。
7. 存在可信主体时，拒绝与之冲突的参数 `userId`；不存在冲突时，把可信主体写入 `arguments.userId`。
8. 序列化修改后的信封，恢复为请求 Body，然后调用 MCP Handler。

认证通过后，初始化、通知和 ping 等合法的非 `tools/call` MCP 方法继续正常放行。需要检查的 Payload 如果包含非法 JSON，则返回 HTTP 400。

本阶段不新增 JSON-RPC 批量请求支持。无法确定性授权的租户批量 Payload 必须拒绝，不能放行。

### 3.2 纵深防御身份解析

`trace` 包将分别保存以下 Context 值：

- 可信认证主体。
- 仅用于路由的用户 ID。

该包将提供统一解析器，返回有效用户 ID 或身份冲突错误。解析顺序如下：

1. 可信认证主体。
2. 工具参数中显式提供的 `userId`。
3. URL 注入 Context 的仅路由用户 ID。

存在可信主体时，两个不可信来源必须为空或与可信主体相同。

通用工具包装器将在调用 Handler 前解析身份。这样，直接 Handler 测试、未来新增 Transport，以及任何绕过 HTTP 主体绑定的路径，仍会拒绝冲突身份。审计日志使用解析后的身份，而不是原始信封值。

Handler 可以继续使用便捷的 `EffectiveUserID` 辅助函数，但该函数必须优先返回可信主体。身份冲突时，由通用包装器返回结构化 MCP 错误。

### 3.3 移除会话身份状态

在 HTTP 主体绑定具备测试保护后，删除 `sessionUserID` Map、`MCPMiddleware` 会话查询以及未实际使用的 `CleanSession` 机制。

这样可以消除无界会话 Map，不再依赖无法适配全部 Transport 的生命周期 Hook，同时解决旧方案固有的首次请求和无状态会话缺口。

## 4. 请求大小与 ACL 规则

需要检查的 MCP 请求体上限为 4 MiB。该值大于当前本地 Wiki 默认 2 MiB 文件上限，为 JSON-RPC 信封和转义后的 Markdown 内容预留空间。

行为固定如下：

| 条件 | 结果 |
| --- | --- |
| Body 超过 4 MiB | HTTP 413 |
| 需要检查的 JSON-RPC 格式非法 | HTTP 400 |
| 租户 `tools/call` 无法可靠获取工具名 | 拒绝，不允许绕过 ACL |
| 租户无权调用目标工具 | HTTP 401，保持现有认证拒绝行为 |
| 可信主体与请求 `userId` 冲突 | HTTP 403 |
| 认证凭据无效 | HTTP 401 |
| 合法的非工具 MCP 方法 | 正常继续处理 |

中间件必须只读取请求体一次，绝不能再用截断后的前缀替换请求体。

## 5. 租户主体配置

`TenantConfig` 新增：

```go
UserID string `json:"user_id"`
```

`user_id` 为可选字段。配置后，它将成为该租户的可信主体；未配置时，租户继续保留现有 Token 验证、限流和工具 ACL 行为，但不会建立用户级可信主体。

不会把租户 `name` 隐式用作 `user_id`，因为显示名称与应用用户标识属于不同概念。

## 6. 有界远程认证缓存

认证包将拥有专用的并发 TTL/LRU 缓存，不导入 Tools 层缓存。

具体属性如下：

- 默认最大容量：4096 条。
- 可通过 `AUTH_REMOTE_CACHE_MAX_ENTRIES` 配置。
- 保持现有正缓存默认 TTL：5 分钟。
- 保持现有负缓存默认 TTL：30 秒。
- `Get` 发现条目过期时立即删除。
- `Set` 更新 LRU 顺序、清理过期条目，并淘汰最久未使用条目，直至不超过容量上限。
- 不需要 Janitor Goroutine，因此缓存没有额外的关闭生命周期。
- 配置容量必须为正数；非法值将导致启动失败。

该缓存保留现有远程验证限流器，并确保在持续收到不同无效 Token 时，内存占用仍有明确上限。

## 7. 认证配置校验

`auth.New` 将返回 `(*Authenticator, error)`，显式配置错误不能再被静默禁用。

以下情况构造失败：

- 配置了 `AUTH_REMOTE_VERIFY_URL`，但它不是合法的 HTTP(S) URL。
- 远程验证地址的 Host 不在 `AUTH_REMOTE_ALLOWED_HOSTS` 中。
- 远程缓存容量不是正数，或 Server 配置装配时无法解析该值。
- 配置了 `AUTH_TENANTS`，但没有产生任何包含可用 Token 或 Token Hash 的租户。

`Authenticator.Enabled()` 根据实际成功构造的认证方式返回结果：

- 非空本地 Token。
- 至少一个有效租户。
- 有效远程认证配置。
- 至少一个允许的 CIDR。

Server 入口将记录构造错误并拒绝启动。错误信息不得包含原始 Token。

## 8. 错误与审计行为

- Payload 超限返回 413 和通用错误信息。
- 需要检查的 Payload 格式非法时，返回 400 和通用错误信息。
- 主体冲突返回 403，并生成结构化安全或审计日志；日志只包含脱敏身份、请求路径和认证模式。
- 无效凭据继续返回 401，并包含 `WWW-Authenticate`。
- Token 和 Authorization Header 继续保持脱敏。
- 工具层身份冲突返回 `CallToolResult.IsError=true`，且不得调用业务 Handler。

## 9. 文件与职责

预计修改的生产文件：

- `internal/trace/trace.go`：分离可信主体和路由身份，并检测身份冲突。
- `internal/auth/auth.go`：认证决策、有界 Payload 检查、租户主体绑定、fail-closed ACL、有界远程缓存和构造校验。
- `cmd/server/main.go`：解析远程缓存容量、处理 `auth.New` 错误，并移除过时的 MCP 会话中间件注册。
- `internal/server/register.go`：在通用工具包装器中统一解析身份，并使用解析后的主体写审计日志。
- `README.md`：说明 `AUTH_TENANTS.user_id`、4 MiB 请求行为、远程缓存容量，以及路由身份与认证身份的区别。

预计修改的测试文件：

- `internal/trace/trace_test.go`：可信主体优先级和身份冲突场景。
- `internal/auth/auth_test.go`：大 Body 完整保留、413、非法 Payload fail-closed、ACL、主体绑定与冲突、缓存容量和 TTL、构造失败及租户主体行为。
- `cmd/server/main_test.go`：按需补充配置解析和 HTTP 集成行为。
- `internal/server/register_test.go`：如确有需要，增加通用包装器冲突拒绝及审计或 Handler 身份测试。

## 10. 测试策略

实现过程严格遵循红—绿—重构循环。每项生产代码变更之前，必须先加入一个聚焦的失败测试，其预期结果应独立推导。

必须覆盖以下回归场景：

1. 允许访问的租户发送 100 KiB、类似 Wiki 的 `tools/call` Payload 时，下游 Handler 收到完整内容。
2. Payload 超过 4 MiB 时返回 413，并且不得调用 Handler。
3. 格式非法或无法检查的租户工具调用不能绕过 ACL。
4. `arguments.userId` 缺失时，注入远程认证主体。
5. 请求身份一致时成功；冲突时返回 403。
6. 共享 Token、IP 白名单和没有 principal 的租户流程继续兼容仅路由 `userId`。
7. 无状态 HTTP 工具调用不依赖 Session Map 也能获得绑定后的 principal。
8. 认证缓存永不超过配置容量，并删除过期条目。
9. 无效远程验证配置导致 Authenticator 构造失败。
10. 租户 `user_id` 被用作可信主体。

最终验证命令：

```bash
go test ./internal/trace ./internal/auth ./internal/server ./cmd/server
go test ./...
go test -race ./...
go vet ./...
```

使用 `httptest.NewServer` 的测试需要执行环境允许绑定本地回环端口。

## 11. 范围之外

- POST 重试 Body 重建和写入幂等性。
- RAG `top_k`、`min_score`、`include_sources` 和 `include_chunks` 行为。
- 除了把可信主体绑定到现有用户映射之外的完整 Wiki 授权策略。
- JSON-RPC 批量请求支持。
- 通用工具选择和仅本地 Wiki 启动模式。
