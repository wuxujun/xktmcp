# xktmcp 项目分析报告

> 生成时间：2026-09-13  
> 模块路径：`github.com/wuxujun/xktmcp`  
> Go 版本：1.25.0  
> 服务版本：v1.0.1

---

## 一、项目概述

`xktmcp` 是一个基于 **Model Context Protocol (MCP)** 构建的 Go 服务端模块，专为 XKT（学课堂）平台设计。它作为 AI Agent 与上游业务 API 之间的桥梁，向 LLM/AI 工具暴露结构化的学员、RAG 知识库、员工和 Wiki 能力，使 AI 能够安全、可观测地调用这些服务。

### 核心定位

```
AI Agent (Claude / n8n) ──MCP协议──▶ xktmcp ──HTTP──▶ 上游业务 API
                                      │
                                      ├─ 认证鉴权（多策略）
                                      ├─ PII 脱敏
                                      ├─ 结果缓存
                                      ├─ 熔断保护
                                      └─ Prometheus 指标
```

---

## 二、架构全景

### 目录结构

```
xktmcp/
├── cmd/server/main.go          # 入口：传输、认证、日志、优雅关闭
├── internal/
│   ├── auth/                   # 多策略认证中间件
│   ├── client/                 # 上游 HTTP 客户端 + 熔断器
│   ├── logger/                 # 结构化日志（同步 stderr + 文件轮转）
│   ├── metrics/                # Prometheus 指标
│   ├── model/                  # DTO 数据模型
│   ├── pii/                    # PII 脱敏（手机号 / 身份证等）
│   ├── prompts/                # MCP Prompt 模板注册
│   ├── server/                 # MCP 工具注册 + Wiki Resource 注册
│   ├── service/                # 业务编排层（校验、路由）
│   ├── tools/                  # MCP 工具定义 + Handler（含缓存）
│   ├── trace/                  # 请求追踪（correlationId / userId）
│   └── wiki/                   # 本地 Wiki 后端（搜索/CRUD/分词/反向链接）
├── config/wiki.json            # Wiki 后端配置
└── go.mod
```

### 代码规模

| 指标 | 数量 |
|------|------|
| Go 源文件总数 | 87 |
| 测试文件数 | 47 |
| 测试覆盖包数 | 12 / 13 |
| 全套测试结果 | ✅ 全部通过 |

---

## 三、传输层与通信协议

服务支持三种传输模式，通过 `-transport` 启动参数切换：

| 模式 | 路径 | 协议特性 |
|------|------|---------|
| `stdio` | — | 本地进程通信，免认证 |
| `sse` | `/sse` + `/messages/` | 服务端事件流，持久连接 |
| `http` | `/mcp` | Streamable HTTP，双模式协议适配 |

### 协议版本适配（Streamable HTTP）

HTTP 模式自动检测协议版本，兼容新旧协议：

- **Legacy**（`2024-11-05`、`2025-03-26`、`2025-06-18`、`2025-11-25`）→ 有状态会话 + `application/json` 响应
- **Modern**（`2026-07-28+`）→ 无状态模式 + `text/event-stream` SSE

版本识别顺序：`Mcp-Protocol-Version` 请求头 → 请求体 `params.protocolVersion` → `_meta.io.modelcontextprotocol/protocolVersion`。

### HTTP 公共端点

| 路径 | 认证 | 用途 |
|------|------|------|
| `/health` | 免 | 存活探针，返回 `{"status":"ok"}` |
| `/ready` | 免 | 就绪探针，返回 `{"status":"ready"}` |
| `/metrics` | 免（建议网络隔离） | Prometheus 指标抓取 |

---

## 四、MCP 工具清单

工具可通过环境变量 `MCP_ENABLED_TOOLS`（逗号分隔）按需启用。共 **10 个工具**：

### 学员工具（Student）

| 工具名 | 说明 | 缓存 TTL |
|--------|------|----------|
| `student_search` | 按姓名/手机等模糊搜索学员 | 60s |
| `student_order` | 查询学员订单列表 | 60s |
| `student_exam` | 查询学员考试记录 | 60s |
| `student_get` | 按 ID 精确获取学员详情 | 5min |

### RAG 知识库工具

| 工具名 | 说明 | 特性 |
|--------|------|------|
| `rag_search` | 语义检索知识库，支持查询改写（Rewrite） | 缓存 + 可配置 TopK/MinScore |

### 员工工具（Staff）

| 工具名 | 说明 |
|--------|------|
| `staff_search` | 搜索员工/教师信息 |

### Wiki 知识库工具

| 工具名 | 说明 | 后端模式 |
|--------|------|---------|
| `wiki_search` | 关键词检索 Wiki 词条概览 | HTTP / Local |
| `wiki_get_page` | 获取 Wiki 词条完整 Markdown 正文 | HTTP / Local |
| `wiki_list_tree` | 浏览 Wiki 分类树/目录大纲 | HTTP / Local |
| `wiki_upsert_page` | 新建/更新/追加 Wiki 词条（写操作） | Local only |
| `wiki_get_backlinks` | 查询指向指定词条的反向链接 | HTTP / Local |

---

## 五、Wiki 本地后端（核心功能）

Wiki 支持两种后端模式，通过 `config/wiki.json` 的 `mode` 字段控制：

### HTTP 模式
调用上游远程 Wiki API，适合生产环境。

### Local 模式（`internal/wiki/`）
基于本地 Markdown 文件目录，完全离线运行，功能完整：

```
local_search.go        # 全文搜索引擎（TF-IDF + 分词）
local_page.go          # 单页读取
local_tree.go          # 分类树构建
local_upsert.go        # 原子写入（create/update/append 模式）
local_backlinks.go     # 反向链接索引（内存 + 后台刷新）
local_resources.go     # MCP Resource 目录注册
search_tokenizer.go    # 分词器（内置 / go-ego/gse 中文分词）
config.go              # 配置加载与校验
markdown.go            # Markdown 前置 matter 解析
```

**本地 Wiki 特性：**
- 支持内置分词器和 **GSE 中文分词**（`zh` / `zh_s` 词典）
- 搜索索引后台定期刷新（`refresh_interval_seconds`）
- 支持**多用户隔离**：每个 userId 可映射独立的内容目录
- `require_user_mapping` 模式下未知用户请求被拦截
- MCP Resources 订阅（`subscriptions_enabled`）支持 VsCode 等客户端实时浏览

---

## 六、认证系统

认证仅对网络传输（`http` / `sse`）生效，`stdio` 免认证（本地传输）。采用**分层认证，fail-closed** 策略：

```
请求 ──▶ IP 白名单检查（CIDR）──▶ 放行（免 Token）
          │（未命中）
          ▼
        多租户 Token 比对（SHA-256 哈希，常量时间）──▶ 按租户鉴权
          │（未匹配）
          ▼
        本地静态 Token 比对（constant-time）──▶ 放行
          │（未匹配）
          ▼
        远程验证（可选，带缓存 + 限流 + SSRF 防护）──▶ 结果缓存
          │（拒绝）
          ▼
        返回 401
```

### 认证配置（环境变量）

| 变量 | 说明 |
|------|------|
| `AUTH_TOKEN` | 本地静态 Bearer Token |
| `AUTH_TENANTS` | 多租户 JSON 配置（支持 `token_hash` 存储） |
| `AUTH_REMOTE_VERIFY_URL` | 远程验证端点 URL |
| `AUTH_REMOTE_ALLOWED_HOSTS` | 远程验证白名单主机（SSRF 防护） |
| `AUTH_IP_ALLOWLIST` | IP CIDR 白名单，命中即放行 |
| `AUTH_TRUST_FORWARDED_HEADER` | 是否信任 X-Forwarded-For（仅可信代理后） |
| `AUTH_REMOTE_CACHE_MAX_ENTRIES` | 远程验证缓存最大条目数（默认 4096） |

**安全要点：**
- Token 以 SHA-256 哈希存储内存，运行时不保留明文
- 日志仅打印掩码，绝不记录原始 Token
- 远程验证带正/负结果双 TTL 缓存 + 全局令牌桶限流

---

## 七、可观测性

### 日志
- 结构化日志同步写入 `stderr` 和文件
- **Lumberjack** 自动分割：单文件上限 100MB，保留 7 天/7 份备份，gzip 压缩
- 每日凌晨 0 点自动触发轮转
- 请求/响应全链路日志（可选开启 Body 记录，支持最大字节截断）
- 敏感请求头（`Authorization`、`Cookie`、`X-API-Key` 等）自动脱敏
- PII 信息（手机号、身份证等）落日志前脱敏

### 指标
- Prometheus 指标通过 `/metrics` 暴露
- 追踪维度：工具名称、请求状态（success/error/cache_hit）

### 追踪
- 每请求注入 `correlationId`（优先 `toolCallId` > `sessionId`，否则自动生成）
- `userId` 从 URL Query `?userId=xxx` 注入 context，全链路透传

---

## 八、客户端层（上游 HTTP）

`internal/client/` 统一管理所有上游 HTTP 调用：

| 客户端 | 目标 API |
|--------|---------|
| `StudentAPI` | 学员查询、订单、考试、详情 |
| `RagAPI` | RAG 知识库语义检索 |
| `StaffAPI` | 员工信息检索 |
| `WikiAPI` | 远程 Wiki 读写操作 |

**弹性机制（熔断器）：**
- 每个 API 独立熔断器（`CircuitBreaker`），从环境变量加载配置
- 熔断状态：Closed → Open → Half-Open
- 支持配置失败阈值、恢复探测间隔

---

## 九、工具缓存

Student、RAG、Wiki 工具均共享全局 `sharedCache`，采用 LRU 策略：

| 场景 | TTL |
|------|-----|
| 学员搜索/订单/考试 | 60 秒 |
| 学员详情 | 5 分钟 |
| Wiki 搜索 | 2 分钟 |
| Wiki 页面 | 5 分钟 |
| Wiki 目录树 | 10 分钟 |

缓存键融合工具参数，命中时直接返回 MCP 结构化结果，并在指标中记录 `cache_hit`。

---

## 十、主要依赖

| 依赖 | 用途 |
|------|------|
| `github.com/modelcontextprotocol/go-sdk v1.7.0` | MCP 协议 SDK（核心） |
| `github.com/go-ego/gse v1.0.2` | 中文分词（Wiki 本地搜索） |
| `github.com/google/jsonschema-go v0.4.3` | MCP 工具 Schema 生成 |
| `github.com/joho/godotenv v1.5.1` | `.env` 配置加载 |
| `github.com/prometheus/client_golang v1.20.5` | Prometheus 指标 |
| `gopkg.in/natefinch/lumberjack.v2 v2.2.1` | 日志自动分割 |

---

## 十一、构建与运行

```bash
# stdio 模式（本地/调试）
go run ./cmd/server/main.go

# Streamable HTTP 模式
go run ./cmd/server/main.go -transport=http -port=8081

# SSE 模式
go run ./cmd/server/main.go -transport=sse -port=8081

# 生产 Linux 二进制
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -tags=jsoniter -ldflags="-s -w" \
  -o mcp-server ./cmd/server/main.go

# 运行全套测试
go test ./...
```

---

## 十二、测试现状

```
ok  github.com/wuxujun/xktmcp/cmd/server        ✅
ok  github.com/wuxujun/xktmcp/internal/auth     ✅
ok  github.com/wuxujun/xktmcp/internal/client   ✅
ok  github.com/wuxujun/xktmcp/internal/logger   ✅
ok  github.com/wuxujun/xktmcp/internal/metrics  ✅
ok  github.com/wuxujun/xktmcp/internal/pii      ✅
ok  github.com/wuxujun/xktmcp/internal/prompts  ✅
ok  github.com/wuxujun/xktmcp/internal/server   ✅
ok  github.com/wuxujun/xktmcp/internal/service  ✅
ok  github.com/wuxujun/xktmcp/internal/tools    ✅
ok  github.com/wuxujun/xktmcp/internal/trace    ✅
ok  github.com/wuxujun/xktmcp/internal/wiki     ✅（含基准测试）
```

Wiki 包包含丰富的基准测试（搜索、反向链接、索引刷新），关注检索性能。

---

## 十三、近期开发重点（最近 15 次提交）

近期工作集中在 **Wiki 本地后端**功能的完善：

1. Wiki Resources 用户隔离与 MCP 订阅支持
2. 本地 Wiki 目录树、反向链接索引构建
3. 搜索分词器优化（GSE 中文分词集成）
4. Wiki 搜索性能基准测试与调优
5. Wiki 检索缓存增强与索引刷新逻辑优化
6. URL 自定义支持

---

## 十四、架构亮点总结

| 特性 | 实现 |
|------|------|
| 多传输协议 | stdio / SSE / Streamable HTTP 三模式 |
| 协议向下兼容 | 自动检测 MCP 协议版本，Legacy/Modern 双路由 |
| 安全纵深 | IP 白名单 → 多租户 Token → 静态 Token → 远程验证 |
| 零明文存储 | Token SHA-256 哈希内存存储，日志掩码 |
| 弹性保护 | 独立熔断器（每 API），带状态转换 |
| 可观测 | 结构化日志 + Prometheus 指标 + 请求级 traceId |
| PII 保护 | 手机号/身份证在落日志/响应前自动脱敏 |
| Wiki 离线 | 完整本地 Markdown Wiki 引擎，支持中文搜索 |
| 多用户隔离 | Wiki 目录按 userId 映射隔离 |
| 生产就绪 | 优雅关闭（15s）、日志轮转、就绪/存活探针 |
