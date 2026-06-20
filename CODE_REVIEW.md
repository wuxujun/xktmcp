# 代码审查结果

审查日期：2026-06-20  
审查范围：`cmd/server`、`internal/auth`、`internal/client`、`internal/service`、`internal/tools`、缓存、日志与测试。

## 结论

整体结构清晰，分层基本合理，认证、PII 脱敏、缓存容量控制和测试覆盖都有基础保障。`go test ./...` 已通过。主要风险集中在 `userId` 的传递一致性、按用户隔离的缓存，以及构造上游 URL 时未完整转义。

## 主要发现

### High：`userId` fallback 未进入统一审计，且只被部分工具使用

- 位置：`cmd/server/main.go:133`、`cmd/server/main.go:206`、`internal/tools/rag.go:204`、`internal/server/register.go:83`
- 现象：HTTP `/mcp?userId=...` 会把 `userId` 写入 `context`，但统一审计日志在 `addTool` 中读取的是原始入参 `in.Querier()`。`rag_search` handler 内部会从 context 补 `args.UserID`，但这是 handler 的局部副本，审计仍可能记录为空。`staff_search` 和 `student_*` 也没有同样的 context fallback。
- 影响：依赖 URL query 传 `userId` 时，审计中的 `querier` 可能缺失；`staff_search` 上游请求也可能拿不到调用者身份。
- 建议：集中实现 `effectiveUserID(ctx, args.UserID)`，并让统一审计和所有需要上游 `userId` 的 handler 使用同一逻辑；或在注册包装层支持回写/覆盖 querier。

### High：`staff_search` 缓存未按 `userId` 隔离

- 位置：`internal/tools/staff.go:35`、`internal/tools/staff.go:42`
- 现象：缓存键只包含 `query`，但上游调用包含 `userId`。
- 影响：如果上游按用户权限返回不同员工/机构数据，不同用户查询同一关键词会复用同一缓存结果，存在越权数据泄露风险。
- 建议：把有效 `userId` 纳入缓存键，例如 `staff:search:<userId>:<query>`；同时补充跨用户缓存隔离测试。

### Medium：上游 URL 中的 `userId` 未转义

- 位置：`internal/client/staff_client.go:44`、`internal/client/rag_client.go:44`
- 现象：`query` 使用了 `url.QueryEscape`，但 `userId` 直接拼接到查询串。
- 影响：包含 `&`、`=`、空格等字符的 `userId` 可能改变上游参数结构，造成请求语义错误或参数注入。
- 建议：用 `url.Values` 构造查询参数，或至少对 `userId` 使用 `url.QueryEscape`。优先推荐 `url.Values`，避免后续新增参数时重复出错。

### Medium：SSE 与 HTTP 的 `userId` 注入行为不一致

- 位置：`cmd/server/main.go:110`、`cmd/server/main.go:112`、`cmd/server/main.go:133`
- 现象：HTTP `/mcp` 包了 `userIDMiddleware`，SSE 的 `/sse` 与 `/messages/` 没有包同样中间件。
- 影响：同样通过 URL 传 `userId`，HTTP transport 可读，SSE transport 不可读，调用者在切换 transport 后审计和上游参数行为会变化。
- 建议：如果 URL `userId` 是正式兼容入口，应在 SSE 相关路径也应用同一中间件；如果不是正式入口，应删除或文档化该差异。

## 测试结果

- 沙箱内首次执行 `go test ./...` 失败，原因是 `httptest` 监听本地端口和 Go build cache 写入受限。
- 提升权限后重新执行 `go test ./...`，全部通过。

## 建议补充测试

- `rag_search` 使用 `/mcp?userId=...` 时，统一审计 `querier` 不为空。
- `staff_search` 在不同 `userId`、相同 `query` 下不会命中彼此缓存。
- `SearchStaffs` 和 `SearchRags` 对特殊字符 `userId` 生成正确查询串。
- SSE 路径是否支持 URL `userId` 的行为测试，按最终设计固定下来。
