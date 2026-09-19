# 发布前审查报告

审查日期：2026-09-16（2026-09-18 更新）
审查范围：当前项目代码、配置、测试与 Linux amd64 发布构建

## 一、结论

此前审计识别的 Resource ACL 缺口和跨凭据复用有状态 MCP 会话问题均已修复，并已有 Tasks 1–4 的聚焦测试与独立复审证据。

当前提交已完成最终发布门禁：全仓测试、核心包竞态检查、全仓静态检查和 Linux amd64 单文件发布构建均通过。发布构建过程中发现新增生产文件不会被既有 `./cmd/server/main.go` 单文件命令包含；会话绑定实现已机械迁回 `main.go`，定向与全量验证随后重新通过。

在完成下述部署配置确认后，当前代码可进入打包和受控发布。工作树当前仍包含 `README.md` 和本报告的文档变更，尚未提交；不得描述为 clean。

## 二、已修复的高风险问题

### 1. `require_user_mapping=true` 严格映射已恢复

`LocalRouter` 现在会执行 `RequireUserMapping`：严格模式下，缺少或未配置的 `userId` 返回 `ErrUserWikiNotConfigured`；关闭严格模式时仍回退到默认 Wiki。验证覆盖严格/非严格模式，以及 Wiki 搜索和 Resources 路由契约。

### 2. HTTP POST 请求体读取超时已增加

HTTP/SSE 服务对 POST 请求体设置 30 秒读取截止时间，同时保留 4 MiB 大小限制。该限制位于请求日志和认证之前；GET/SSE 不设置该读取截止时间。

### 3. Wiki Resource ACL 已绑定租户工具权限

`AUTH_TENANTS.allowed_tools` 现在覆盖 Wiki Resources：Catalog、Tree、Page 分别复用 `wiki_search`、`wiki_list_tree`、`wiki_get_page`，`*` 允许全部。缺少对应权限时，`resources/read` 在访问后端前返回 Resource not found；没有租户 ACL context 的兼容路径保持原行为。

### 4. 跨凭据 MCP 会话复用已拒绝

有状态 SSE 和 legacy Streamable HTTP 会话绑定到建连时的认证身份。后续请求使用不同或缺失的认证身份时，在进入 MCP SDK 前返回 HTTP 403；Token 切换或轮换后客户端必须丢弃旧会话并重新连接。

绑定仅保存进程内不透明身份，不保存或记录明文 Token。SSE 流结束及成功的 legacy Streamable HTTP DELETE 会清理绑定。现代 `2026-07-28` Streamable HTTP 为无状态模式，不创建绑定；stdio 也不创建绑定。

最终安全审查还发现默认 HTTP 元数据日志会通过 URL query、专用字段和请求头记录原始 MCP session ID。该 Important 问题已修复：query 与专用字段只保留脱敏值，`Mcp-Session-Id` 请求头统一显示为 `[REDACTED]`，请求与响应日志复用同一安全路径。修复后的独立安全复审结论为 APPROVED，Critical、Important、Minor 均为 0。

## 三、配置与运维风险

### 1. 本地 Wiki 运行配置不会随代码发布

`config/wiki.json` 被 `.gitignore` 忽略，版本库中仅跟踪 `config/wiki.example.json`。当前本地配置含本机绝对路径；干净环境或容器中若未注入该配置，服务会回退到 HTTP Wiki 模式，并要求 `API_TOKEN` 和 `BASE_URL`。

发布时必须独立注入 Wiki 配置。多实例有状态传输仍需要会话粘性；绑定表与 SDK session 都是进程内状态。

### 2. `/metrics` 默认可免认证访问

生产环境应通过 `METRICS_AUTH_TOKEN` 或反向代理/网络隔离保护 `/metrics`。`/health` 和 `/ready` 保持免认证时，也必须确保部署网络边界可靠。

## 四、Tasks 1–4 已有聚焦验证证据

以下只记录本次文档任务开始前的报告或复审已明确执行并通过的命令；它们不能替代第七节的最终发布门禁。

- `GOCACHE=/tmp/xktmcp-go-cache go test ./internal/auth ./internal/server -run 'TestSessionIdentity|TestTenantToolAccessPropagatesToResourceRequests|TestWikiResourcesRequireCorrespondingTenantTool' -count=1`：PASS。
- `GOCACHE=/tmp/xktmcp-task2-rereview-normal-cache go test ./cmd/server -run 'TestSessionBindings|TestStreamableSessionBindingMiddleware' -count=1`：PASS。
- `GOCACHE=/tmp/xktmcp-task2-rereview-race-cache go test -race ./cmd/server -run 'TestSessionBindings|TestStreamableSessionBindingMiddleware' -count=1`：PASS，无 race 报告。
- `GOCACHE=/tmp/xktmcp-task3-rereview-cache go test ./cmd/server -run 'TestSSESessionBindingMiddleware|TestSSEEndpointBindingWriter' -count=1`：PASS。
- `GOCACHE=/tmp/xktmcp-task3-rereview-race-cache go test -race ./cmd/server -run 'TestSSESessionBindingMiddleware|TestSSEEndpointBindingWriter' -count=1`：PASS，无 race 报告。
- `GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server -run 'TestAuthenticatedWikiResourcesTransportsIsolateTenants|TestStreamableHTTP.*2026|TestRequestBodyReadTimeoutMiddleware' -count=1 -timeout=90s`：PASS；后续独立复审确认对应 focused normal/race 检查通过。
- `GOCACHE=/tmp/xktmcp-task2-rereview-vet-cache go vet ./cmd/server`：PASS。

Tasks 1–4 的最终规格与质量复审均为 APPROVED。验证覆盖 Resource ACL 映射、身份不披露、原子绑定、ResponseWriter 能力、SSE endpoint 预转发解析、跨凭据拒绝、清理和 `2026-07-28` 无状态兼容路径。

## 五、已确认修复的旧问题

- RAG 的 `top_k`、`min_score`、`include_sources`、`include_chunks` 已实际生效。
- MCP POST 请求体已限制为 4 MiB。
- Staff/RAG 上游请求中的 `userId` 已进行 URL 编码。
- 工具缓存已按用户维度隔离，Wiki 写入后会清理 Wiki 缓存。
- 上游写请求默认不自动重试。
- 文件搜索使用 `os.OpenRoot` 和路径校验限制目录越界。
- 认证主体与请求 `userId` 的冲突校验已覆盖 HTTP 请求路径。

## 六、后续优化顺序

1. 完善可发布配置模板和容器部署说明。
2. 在公网入口配置反向代理连接数限制和读取超时。
3. 将测试、竞态检查、静态检查和 Linux 构建门禁接入 CI。

## 七、最终发布验证：通过

2026-09-18 在最终代码布局上重新执行：

- `gofmt -w`（Task 5 列出的目标 Go 文件）及 `git diff --check`：PASS。
- `GOCACHE=/tmp/xktmcp-go-cache go test -p=1 ./... -count=1 -timeout=120s`：PASS。
- `GOCACHE=/tmp/xktmcp-go-cache go test -race ./internal/auth ./internal/wiki ./internal/server ./cmd/server -count=1 -timeout=180s`：PASS，无 race 报告。
- `GOCACHE=/tmp/xktmcp-go-cache go vet ./...`：PASS。
- `GOCACHE=/tmp/xktmcp-go-cache CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags='-s -w' -o /tmp/xktmcp-release-check ./cmd/server/main.go`：PASS。
- `file /tmp/xktmcp-release-check`：`ELF 64-bit LSB executable, x86-64, statically linked, stripped`；产物约 43 MiB。
- 最终 `git diff --check`：PASS；文档提交前 `git status --short` 仅包含 `README.md` 修改和本报告未跟踪文件。

这些结果是在单文件构建兼容修复提交后重新取得，可作为本轮最终发布门禁证据。

随后提交的 session ID 日志脱敏修复再次触发并通过同一套全仓测试、核心 race、全仓 vet 和单文件 Linux 构建门禁；独立安全复审同时通过。

## 八、发布建议

Resource ACL、严格用户映射、POST 读取超时和跨凭据会话复用修复均已有聚焦及最终门禁证据。当前代码可以进入打包和受控发布流程。

即使最终门禁通过，发布仍需确认本地 Wiki 配置注入、`require_user_mapping=true` 的多租户部署要求、`/metrics` 与运维探针的网络保护，以及有状态 HTTP/SSE 的会话粘性。
