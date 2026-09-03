# xktmcp 项目分析报告

> 文件归档：`docs/project-analysis-260902.md`
> 生成日期：2026-09-02
> 项目模块：`github.com/wuxujun/xktmcp`
> 运行时：Go 1.25.0 · MCP Go SDK v1.7.0

---

## 1. 项目定位

`xktmcp` 是一个面向**学客通（XKT）业务系统**的 MCP（Model Context Protocol）服务器，作为大模型与内部业务 API 之间的标准化工具网关。它将学员管理、人员检索、RAG 语义检索、LLM-Wiki 知识库等内部能力封装为标准 MCP Tools 和 MCP Resources，支持 Claude Desktop、Cursor、VS Code 等主流 MCP 客户端直接接入。

---

## 2. 技术栈

| 维度 | 选型 |
|:---|:---|
| 语言 | Go 1.25.0 |
| MCP 协议 SDK | `github.com/modelcontextprotocol/go-sdk v1.7.0` |
| 指标监控 | Prometheus (`prometheus/client_golang v1.20.5`) |
| 日志滚动 | Lumberjack (`gopkg.in/natefinch/lumberjack.v2 v2.2.1`) |
| JSON Schema | `github.com/google/jsonschema-go v0.4.3` |
| 配置加载 | `github.com/joho/godotenv v1.5.1` |
| 构建产物 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 静态二进制 |

---

## 3. 目录结构

```
xktmcp/
├── cmd/server/main.go          # 入口：传输、认证、优雅关闭
├── config/
│   └── wiki.json               # Wiki 后端配置（local/http 模式）
├── internal/
│   ├── auth/                   # Bearer 认证 + 多租户 + IP 白名单 + 远程验证
│   ├── client/                 # 上游 HTTP 客户端 + 熔断器
│   ├── logger/                 # 结构化日志（JSON）+ 审计日志
│   ├── metrics/                # Prometheus 埋点
│   ├── model/                  # DTO 数据模型
│   ├── pii/                    # 敏感信息脱敏（手机号、身份证）
│   ├── prompts/                # MCP Prompts 注册
│   ├── server/
│   │   ├── register.go         # 工具统一装配 + 埋点包装
│   │   └── wiki_resources.go   # MCP Resources 注册 & Handler
│   ├── service/                # 业务编排层（StudentService, WikiService…）
│   ├── tools/                  # MCP 工具声明 + Handler（11 个工具）
│   ├── trace/                  # Trace ID + userID 上下文传播
│   └── wiki/                   # LLM-Wiki 本地后端（索引、读写、Resources）
└── docs/                       # 设计文档
```

---

## 4. MCP 工具清单（11 个）

### 4.1 学员域（StudentAPI）

| 工具名 | 功能 | 缓存 TTL |
|:---|:---|:---:|
| `student_search` | 按姓名/手机号模糊分页检索学员基本信息 | 60s |
| `student_get` | 按精确 ID 获取学员详细档案 | 5min |
| `student_order` | 查询学员订单记录 | 60s |
| `student_exam` | 查询学员考试成绩 | 60s |

> 前置设计：`student_order` / `student_exam` / `student_get` 均强制要求先调用 `student_search` 取得精确 ID，避免大模型直接猜测 ID 导致数据错乱。

### 4.2 人员域（StaffAPI）

| 工具名 | 功能 |
|:---|:---|
| `staff_search` | 模糊检索员工/讲师信息 |

### 4.3 RAG 语义检索（RagAPI）

| 工具名 | 功能 |
|:---|:---|
| `rag_search` | 向量语义检索（可选语义重写，`RAG_SEMANTIC_REWRITE=true`）|

### 4.4 Wiki 知识库（WikiService）

| 工具名 | 功能 | 缓存 TTL |
|:---|:---|:---:|
| `wiki_search` | 关键词 + 分类检索词条摘要（TopK 默认 5） | 2min |
| `wiki_get_page` | 按 page_id 或 title 获取 Markdown 全文 | 5min |
| `wiki_list_tree` | 获取分类目录树（深度 1–10，默认 3） | 10min |
| `wiki_upsert_page` | 创建/覆盖/追加 Wiki 词条（支持 create/update/append 模式） | — |
| `wiki_get_backlinks` | 查询反向引用关系（知识图谱导航） | — |

---

## 5. MCP Resources（Phase 1 & 2 已落地）

### 5.1 Resource URI 规范

| URI | MIME 类型 | 用途 |
|:---|:---:|:---|
| `wiki://catalog` | `application/json` | 当前用户可见页面目录（按用户动态生成） |
| `wiki://tree` | `application/json` | 完整目录树（最大深度 10） |
| `wiki://page/{page_key}` | `text/markdown` | 单页 Markdown 正文（通过 ResourceTemplate） |

### 5.2 page_key 编码

`page_key` 为 `page_id` 的无填充 Base64URL 编码（`base64.RawURLEncoding`）：
- 避免 `/`、`..` 等路径语法注入 URI 路径段
- 校验：非空、编码规范、长度 ≤ 2048 字节、合法 UTF-8
- 不存在或任何校验失败均统一返回 `ResourceNotFoundError`（避免信息泄露）

### 5.3 多租户隔离设计

受 Go SDK v1.7.0 约束（`resources/list` 为服务级静态集合，不支持按请求动态过滤）：
- 静态注册仅限 `wiki://catalog` 与 `wiki://tree` 两个无租户信息的固定入口
- 页面通过 `wiki://page/{page_key}` Resource Template 访问
- 所有 Handler 从 `trace.EffectiveUserID(ctx, "")` 获取认证主体，按用户选择对应 `LocalSearcher` 实例
- 资源 URI 不包含 userID，租户选择完全来自可信上下文

### 5.4 配置开关

```json
{
  "mode": "local",
  "resources": {
    "enabled": true,
    "subscriptions_enabled": false,
    "max_catalog_entries": 1000
  }
}
```

- `enabled` 默认 `false`；`mode=http` 时设为 `true` 直接返回配置错误
- `subscriptions_enabled` Phase 1–2 阶段恒为 `false`，设为 `true` 直接报错（待 Phase 3 放开）
- `max_catalog_entries` 合法范围 1–10000，默认 1000

### 5.5 安全边界

1. **身份来源**：只信任认证中间件写入的上下文主体，不从 URI 解析 userID
2. **路径安全**：删除 `wiki://raw/` URI，页面仅按索引 page_id 查找，不拼接本地路径
3. **PII 脱敏**：Catalog 标题/摘要、Tree JSON、Page 正文均经 `pii.Redact`
4. **错误最小化**：权限错误、不存在、格式错误统一返回 `ResourceNotFoundError`
5. **只读边界**：Resource Handler 无任何写入能力
6. **符号链接防护**：索引刷新时忽略符号链接

---

## 6. 核心基础设施

### 6.1 认证体系（`internal/auth`）

| 认证方式 | 说明 |
|:---|:---|
| **本地 Bearer Token** | `AUTH_TOKEN` 环境变量，`crypto/subtle` 常量时间比较防侧信道 |
| **多租户 Token** | `AUTH_TENANTS` JSON 配置，支持 `token_hash`（SHA-256 hex），内存中仅存哈希；支持租户级工具白名单和 RPS 限流 |
| **IP 白名单** | `AUTH_IP_ALLOWLIST` CIDR 列表，命中直接放行 |
| **远程验证** | `AUTH_REMOTE_VERIFY_URL`，带目标主机白名单防 SSRF，正/负结果缓存（默认 4096 条） |
| **stdio 免认证** | stdio 为本地进程传输，不启用网络认证 |

### 6.2 熔断器（`internal/client`）

所有上游 API 调用均受独立熔断器保护，支持从环境变量加载每个后端独立策略。

### 6.3 缓存（`internal/tools`）

查询型工具按 `userID + 查询参数` 作为 key 进行内存缓存。`wiki_upsert_page` 写入成功后按 `userID` 前缀精准失效 Wiki 相关缓存。

### 6.4 日志与追踪（`internal/logger` / `internal/trace`）

- **Trace ID**：每次工具调用自动生成，或从 n8n 上游透传的 `toolCallId/sessionId` 继承
- **审计日志**：记录 `tool / querier / subject(已脱敏) / status / latency_ms`，满足合规留痕诉求
- **PII 脱敏**：所有响应内容经 `pii.Redact` 掩码手机号、身份证等敏感信息
- **日志滚动**：Lumberjack 按大小（100MB/文件）和天数（7 天）自动切割

### 6.5 传输协议支持

| 传输方式 | 端点 | 说明 |
|:---|:---|:---|
| `stdio` | — | 本地进程，免认证，Claude Desktop 默认模式 |
| `sse` | `/sse`, `/messages/` | 旧式 SSE 长连接 |
| `http` | `/mcp` | Streamable HTTP，自动协议版本检测（legacy JSON / 现代 SSE） |

---

## 7. Wiki 本地后端架构（`internal/wiki`）

- **`LocalRouter`**：根据 `userID` 选择对应 `LocalSearcher`；`require_user_mapping=true` 时拒绝未知用户
- **`LocalSearcher`**：`RWMutex` 保护内存索引快照，默认 30s 周期刷新，所有循环检查 `ctx.Err()`
- **`local_upsert.go`**：create/update/append 三种写入模式，写入后即时刷新索引并失效缓存
- **`local_backlinks.go`**：从索引扫描反向引用关系
- **`local_tree.go`**：递归构建目录树，最大深度 10
- **`local_resources.go`**：`ListResources(ctx, limit)` + `ReadPageResource(ctx, uri)` 领域层实现

---

## 8. 工具统一装配模式

所有工具调用均经 `wrapToolHandler` 包装，保证：
1. Trace ID 注入/继承
2. 用户身份校验（认证主体与显式 `userId` 参数冲突检测）
3. Prometheus 耗时 & 状态指标上报
4. 审计日志
5. 摘要日志

工具通过 `MCP_ENABLED_TOOLS` 环境变量按名称白名单控制，未知工具名启动时直接报错。

---

## 9. 测试现状

`go test ./...` 全部通过（2026-09-02 实测）：

| 包 | 状态 |
|:---|:---:|
| `cmd/server` | ✅ PASS |
| `internal/auth` | ✅ PASS |
| `internal/client` | ✅ PASS |
| `internal/logger` | ✅ PASS |
| `internal/metrics` | ✅ PASS |
| `internal/pii` | ✅ PASS |
| `internal/prompts` | ✅ PASS |
| `internal/server` | ✅ PASS |
| `internal/service` | ✅ PASS |
| `internal/tools` | ✅ PASS |
| `internal/trace` | ✅ PASS |
| `internal/wiki` | ✅ PASS |

主要覆盖场景：Auth 多策略验证、Wiki Resources URI 编解码与多租户隔离、Upsert 三种模式、熔断器状态机、MCP 内存传输集成测试（Catalog/Tree/Page Handler）。

---

## 10. 近期提交记录（最近 15 条）

| Commit | 说明 |
|:---|:---|
| `fe069da` | 修复 Wiki Resources 最终评审问题 |
| `43916fe` | test: verify wiki resources transports |
| `1c892ac` | feat: register local wiki resources |
| `85bfc0f` | feat: isolate wiki resources by user |
| `d2fc2dd` | feat: add local wiki resource catalog |
| `7ca2daf` | feat: configure wiki resources |
| `91b920b` | docs: plan wiki resources phases 1 and 2 |
| `5b7e765` | docs: revise wiki resources capability design |
| `668db8f` | fix: scope wiki cache invalidation by user |
| `55e1590` | docs: refresh wiki progress report |
| `69389c8` | chore: remove tracked compressed log |
| `75dd5f8` | feat: configure circuit breaker policy |
| `f2c4def` | docs: refresh coverage measurement |
| `abdaa65` | test: cover remaining env parser branches |
| `882dd36` | test: cover server environment parsing |

---

## 11. 未完成事项与后续规划

### Phase 3：订阅与更新通知（待实施，按需评估）

| 任务 | 说明 |
|:---|:---|
| 订阅启动装配 | `main.go` 中在 `mcp.NewServer` 前完成 `LocalRouter` 创建，配置 `SubscribeHandler` / `UnsubscribeHandler` |
| Upsert 事件接口 | `wiki_upsert_page` 注入可选 `WikiResourceEvents`，写入成功后触发 Page/Catalog/Tree 三条通知 |
| 外部文件变化通知 | 索引刷新前后比较快照，仅在实际变更时发送通知 |

> **约束**：`local.users` 非空（多租户）时禁用订阅（配置层已强制报错）；订阅仅在单租户或每租户独立 Server 中开启。

### 其他潜在改进

| 项目 | 说明 |
|:---|:---|
| Resources 监控指标 | 为 `resources/read` 增加 Prometheus 耗时/计数指标，与 Tools 保持同等可观测性 |
| page_key 编码容错 | `ParsePageResourceURI` 可增加对标准 Base64（带 `=` 填充）的宽容兼容 |
| Catalog 摘要长度截断 | 大规模知识库下限制 description 摘要长度（如前 100 字符）控制 JSON 包体积 |
| HTTP Wiki 后端 Resources | 目前仅 `mode=local` 支持，HTTP 模式按需评估 |

---

## 12. 运行配置参考

### 关键环境变量

| 变量 | 说明 | 是否必填 |
|:---|:---|:---:|
| `AUTH_TOKEN` | Bearer 本地令牌（http/sse 传输必须配置认证） | 条件必填 |
| `AUTH_TENANTS` | 多租户配置 JSON | 可选 |
| `AUTH_REMOTE_VERIFY_URL` | 远程令牌验证 URL | 可选 |
| `AUTH_IP_ALLOWLIST` | IP 白名单 CIDR 列表 | 可选 |
| `BASE_URL` | 上游业务 API 基础地址 | http 模式必填 |
| `API_TOKEN` | 上游 API 认证令牌 | http 模式必填 |
| `MCP_ENABLED_TOOLS` | 逗号分隔工具白名单，留空启用全部 | 可选 |
| `RAG_SEMANTIC_REWRITE` | `true` 启用 RAG 语义查询改写 | 可选 |
| `LOG_HTTP_PAYLOADS` | `true` 记录 HTTP 请求/响应 Body | 可选 |

### 启动命令示例

```bash
# stdio（本地 Claude Desktop 连接）
go run ./cmd/server/main.go

# Streamable HTTP（端口 8081）
go run ./cmd/server/main.go -transport=http -port=8081

# SSE
go run ./cmd/server/main.go -transport=sse -port=8081

# Linux 发布构建
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -tags=jsoniter \
  -ldflags="-s -w -X main.version=1.0.1" \
  -o mcp-server ./cmd/server/main.go
```

---

*本报告由 Antigravity 于 2026-09-02 自动生成，基于代码静态分析与 `go test ./...` 实测结果。*
