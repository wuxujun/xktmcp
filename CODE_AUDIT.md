# 代码审计与扩展建议

审查日期：2026-06-21  
审查范围：`cmd/server`、`internal/auth`、`internal/client`、`internal/tools`、`internal/service`、缓存、审计日志与测试。

## 总体结论

当前代码分层清晰，`userId` 在 HTTP/SSE 注入、统一审计、`rag_search`/`staff_search` 上游调用和 `staff_search` 缓存隔离方面已经基本闭合。全量测试通过。未发现 Critical 级问题；仍有几处 Medium/Low 风险建议尽快处理。

## 测试结果

- 已执行：`go test ./...`
- 结果：全部通过。

## 漏洞与 Bug

### Medium：上游 URL 中的 `userId` 未转义

- 位置：`internal/client/staff_client.go:44`、`internal/client/rag_client.go:44`
- 问题：`query` 使用了 `url.QueryEscape`，但 `userId` 直接拼接到查询串。
- 影响：当 `userId` 包含 `&`、`=`、空格等字符时，可能改变上游参数结构，造成参数注入或请求语义错误。
- 建议：使用 `url.Values` 统一构造查询串，例如 `v.Set("userId", userId)` / `v.Set("query", query)` 后调用 `v.Encode()`。

### Medium：多租户 ACL 读取请求体无大小限制

- 位置：`internal/auth/auth.go:238`、`internal/auth/auth.go:470`
- 问题：`extractToolName` 使用 `io.ReadAll(r.Body)` 读取完整请求体。
- 影响：已认证租户可以提交超大 POST body，导致内存压力；网络传输又刻意不设置 `ReadTimeout`/`WriteTimeout`，更需要控制 body 大小。
- 建议：用 `http.MaxBytesReader` 或 `io.LimitReader` 限制 JSON-RPC body 大小，并对超限返回 413/401。

### Medium：远程认证配置无效时仍视为“已启用认证”

- 位置：`internal/auth/auth.go:70`、`internal/auth/auth.go:158`
- 问题：`Config.Enabled()` 只检查 `RemoteVerifyURL` 非空；如果 URL 主机不在 `AllowedHosts`，`remoteOK=false`，但 `requireAuth` 仍允许 HTTP/SSE 启动。
- 影响：仅配置了无效远程认证时，服务会启动但所有请求都会认证失败，形成隐蔽可用性故障。
- 建议：在构造后暴露 `Authenticator.Enabled()` 为“实际可用认证方式”，或在 `buildAuthConfig` 阶段对远程 host 不合法直接 fail-closed 退出。

### Low：RAG 参数对外声明与实际行为不一致

- 位置：`internal/tools/rag.go:207`、`internal/tools/rag.go:240`
- 问题：`top_k`、`min_score`、`include_sources`、`include_chunks` 被放入缓存键和返回元数据，但没有真正限制结果数量、过滤分数或隐藏 sources/chunks。
- 影响：调用方以为参数已生效，实际返回内容不受这些参数控制，可能造成结果过多或暴露不必要字段。
- 建议：在 handler 中按 `min_score` 过滤、按 `top_k` 截断，并根据 include flags 控制结构化返回字段。

### Low：部分工具入口日志未使用 effective userId

- 位置：`internal/tools/student.go:111`、`internal/tools/student.go:146`、`internal/tools/student.go:181`、`internal/tools/student.go:216`
- 问题：统一审计已使用 `trace.EffectiveUserID`，但 student 工具的入口日志仍直接打印 `args.UserID`。
- 影响：当 `userId` 来自 URL query context 时，审计日志正确，普通工具日志可能为空，排障时不一致。
- 建议：student 工具日志也统一使用 `trace.EffectiveUserID(ctx, args.UserID)`。

## 已确认闭合项

- `/mcp`、`/sse`、`/messages/` 均已套用 `userIDMiddleware`。
- 统一审计使用 `trace.EffectiveUserID(ctx, in.Querier())`。
- `rag_search` 和 `staff_search` 使用相同 effective `userId` 逻辑。
- `staff_search` 缓存键包含 `userId`：`staff:search:<userId>:<query>`。
- `staff_search` 已有跨用户缓存隔离测试。
- `AUTH_IP_ALLOWLIST` 默认不信任伪造的 forwarded headers，相关测试覆盖较好。

## 待扩展功能建议

1. 上游 URL 构造统一化：为所有 client 增加小型 helper，使用 `url.Values` 和 `url.JoinPath`，减少手写拼接。
2. RAG 检索增强：真正支持 `top_k`、`min_score`、`include_sources`、`include_chunks`，并补充单测。
3. 请求体安全限制：为 MCP JSON-RPC POST 增加 body size 上限，并记录超限指标。
4. 多租户增强：支持 tenant 维度审计字段、默认拒绝空 `allowed_tools`、按 tenant 暴露限流指标。
5. 缓存治理：为各工具暴露 cache hit/miss 指标，并支持按工具清理缓存。
6. 配置校验：启动时集中校验 auth、RAG、上游 API 配置，输出明确错误，避免半可用状态。
7. 文档同步：将 `AUTH_TENANTS`、URL `userId` 注入、RAG 参数实际语义补充到 `README.md`。
