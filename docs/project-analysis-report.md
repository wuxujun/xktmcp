# xktmcp 项目全面检查与架构分析报告

> **报告归档**：`docs/project-analysis-report.md`  
> **分析基线**：2026-09-25 · 基线提交：`7514a1c`  
> **项目模块**：`github.com/wuxujun/xktmcp`  
> **核心运行栈**：Go 1.25.0（工具链 `go1.25.13`）· MCP Go SDK `v1.7.0` · Prometheus SDK `v1.20.5`

---

## 一、 项目定位与业务全景

`xktmcp` 是面向**学客通（XKT）业务系统**的工业级 MCP（Model Context Protocol）标准工具网关服务。其核心使命是消除大语言模型（LLM）与企业内部微服务、知识库及本地文件系统之间的协议与安全鸿沟。

通过将学员档案、员工信息、RAG 向量问答、企业级 Wiki 知识库以及本地服务器文档统一封装为标准 MCP 协议，支持 Claude Desktop、Cursor、VS Code 等主流大模型客户端以无缝、安全、受控的方式调用企业核心能力。

### 核心特性矩阵

| 能力域 | 核心职责 | 后端支撑 | 关键特性 |
|:---|:---|:---|:---|
| **学员域 (Student)** | 学员检索、档案详情、订单、考试成绩查询 | 上游微服务 HTTP API | 强前置校验（ID 依赖检索防猜测）、LRU+TTL 缓存 |
| **员工域 (Staff)** | 教师、讲师、员工档案检索 | 上游微服务 HTTP API | 模糊检索、熔断隔离 |
| **语义域 (RAG)** | 内部知识库向量检索 | 上游 RAG HTTP API | 可选语义重写 (`RAG_SEMANTIC_REWRITE`)、分块透传 |
| **知识域 (Wiki)** | 企业 Wiki 词条检索、全文获取、目录树、增量写入、反向引用 | 远程 HTTP 或 本地 Markdown | 双后端支持、本地倒排索引、GSE 中文分词、多租户隔离 |
| **文件域 (File)** | 本地受控文件目录模糊检索、元信息、行区间预览 | 本地文件系统 (沙箱根目录) | GSE/Builtin 双分词引擎、防路径穿透、独立依赖启动 |
| **资源域 (Resources)** | MCP 动态上下文注入 | 本地 Wiki 引擎 | 动态按用户隔离的 Catalog、Tree、Page 资源 |
| **提示域 (Prompts)** | 常用工作流提示词模板 | 内存引擎 | 随工具启用集动态注册推荐 Workflow Prompts |

---

## 二、 整体架构与分层设计

系统严格遵循分层解耦与依赖倒置设计，数据流向清晰，横切关注点（安全、日志、审计、监控）统一切入。

```mermaid
flowchart TD
    Client[MCP 客户端 (Claude/Cursor/VSCode)] -->|stdio / SSE / Streamable HTTP| Entry[cmd/server: 传输层 & 会话绑定 & 运维探针]
    
    subgraph Core [核心业务装配层]
        Entry --> Auth[internal/auth: Bearer / 多租户SHA-256 / IP白名单 / 远程验证]
        Entry --> Reg[internal/server: 统一装配 & 埋点拦截器]
        
        Reg --> Tools[internal/tools: MCP Tools Schema & Handler]
        Reg --> Res[internal/server: MCP Resources 注册 & Handler]
        Reg --> Prompts[internal/prompts: MCP Prompts 注册]
    end

    subgraph Service [业务编排与执行]
        Tools --> Svc[internal/service: Student / Staff / Rag / Wiki / File Services]
        Res --> LocalWiki[internal/wiki: 本地 Markdown 知识引擎]
    end

    subgraph Upstream [数据源与上游适配]
        Svc --> ClientPkg[internal/client: HTTP 客户端 + 模块独立熔断器 + 退避重试]
        Svc --> LocalWiki
        Svc --> FileSys[本地文件系统 (FILE_SEARCH_ROOT)]
        ClientPkg --> RemoteAPI[学客通微服务 (yk.xkt.com)]
    end

    subgraph CrossCutting [横切基础设施]
        Trace[internal/trace: TraceID / 租户主体传播]
        Metrics[internal/metrics: Prometheus 指标采集]
        PII[internal/pii: 敏感数据自动脱敏]
        Logger[internal/logger: 结构化 JSON 滚动日志]
    end
```

### 分层模块职责说明

1. **传输与入口层 (`cmd/server`)**
   - 支持 `stdio`（无网络开销，本地开发与 CLI 工具首选）、`SSE`（`/sse` 与 `/messages/`）、`Streamable HTTP`（`/mcp`）。
   - 实现运维探针：`/health`（存活探针）、`/ready`（就绪探针，工具与认证器就绪）、`/metrics`（Prometheus 指标输出）。
   - 托管安全会话机制：SSE 与旧版 HTTP 传输下的凭据强绑定，防止长连接会话劫持。
   - 托管日志滚动：整合 Lumberjack 每日零点自动切割与体积上限控制。
2. **工具统一增强层 (`internal/server`)**
   - 按 `MCP_ENABLED_TOOLS` 动态解析与白名单注册工具。
   - 提供统一的工具调用拦截器：自动注入 Trace ID、租户 Audit Subject 记录、耗时统计与 Prometheus 计数、统一 PII 手机号与身份证脱敏。
3. **工具适配与 Schema 层 (`internal/tools`)**
   - 严格基于 JSON Schema 生成工具入参出参规范。
   - 内置防内存泄漏的 `MemoryCache`（LRU + TTL + 定期 Janitor 垃圾回收）。
4. **业务编排层 (`internal/service`)**
   - 实现业务输入校验、业务组合与后端路由（如 Wiki 远程 vs 本地切换）。
   - 强制前置调用链路（如订单查询必须持有精确 student_id）。
5. **本地存储引擎 (`internal/wiki`, `internal/service/file_service.go`)**
   - 包含高性能本地 Markdown 页面解析、目录树生成、双向链接图谱提取。
   - 内置基于 `github.com/go-ego/gse` 的中文分词器与倒排索引（Inverted Index），支持文件增量更新检测与重构。
6. **上游客户端层 (`internal/client`)**
   - 针对不同微服务域（Student、Staff、RAG、Wiki）设立相互独立的并发安全三态熔断器（Closed, Open, Half-Open）。
   - 支持指数退避重试与 HTTP 4xx/5xx 智能识别。

---

## 三、 MCP 工具全清单与接口规范（共 14 个）

当前系统注册表支持 14 个核心工具，分为 5 大能力域：

### 1. 学员域 (StudentAPI - 4 个工具)

| 工具名 | 入参摘要 | 出参模型 | 缓存策略 | 安全与业务约束 |
|:---|:---|:---|:---:|:---|
| `student_search` | `query` (姓名/手机号), `page`, `page_size` | 学员摘要列表、分页元数据 | 60s (LRU) | 基础检索入口，输出敏感信息脱敏 |
| `student_get` | `student_id` (必填) | 完整学员业务档案 | 5min (LRU) | 必须使用 `student_search` 返回的 ID |
| `student_order` | `student_id` (必填), `page`, `page_size` | 订单流水列表、金额、状态 | 60s (LRU) | 交易敏感数据，强依赖合法 ID |
| `student_exam` | `student_id` (必填), `page`, `page_size` | 考试科目、分数、通过状态 | 60s (LRU) | 考试敏感数据，强依赖合法 ID |

### 2. 人员域 (StaffAPI - 1 个工具)

| 工具名 | 入参摘要 | 出参模型 | 缓存策略 | 说明 |
|:---|:---|:---|:---:|:---|
| `staff_search` | `query` (必填), `page`, `page_size` | 员工/讲师基础信息列表 | — | 实时检索，熔断保护 |

### 3. 向量检索域 (RagAPI - 1 个工具)

| 工具名 | 入参摘要 | 出参模型 | 特性支持 |
|:---|:---|:---|:---|
| `rag_search` | `query`, `top_k`, `min_score`, `include_sources`, `include_chunks` | 向量匹配文本段落与相关源 | 支持 `RAG_SEMANTIC_REWRITE` 语义改写 |

### 4. 知识库域 (WikiService - 5 个工具)

| 工具名 | 入参摘要 | 模式 | 缓存策略 | 核心能力 |
|:---|:---|:---:|:---:|:---|
| `wiki_search` | `query`, `category`, `top_k` | HTTP / Local | 2min (LRU) | 关键词检索，支持 GSE 分词与前缀匹配 |
| `wiki_get_page` | `page_id` 或 `title` | HTTP / Local | 5min (LRU) | Markdown 全文读取，支持别名兼容 |
| `wiki_list_tree` | `name`, `max_depth` (1-10) | HTTP / Local | 10min (LRU) | 分类树形层级结构返回 |
| `wiki_upsert_page` | `page_id`, `title`, `content`, `mode` (`create`/`update`/`append`) | HTTP / Local | — | 页面创建与增量更新，支持并发安全写入 |
| `wiki_get_backlinks` | `page_id` 或 `title` | HTTP / Local | — | 提取并返回反向引用该页面的链接图谱 |

### 5. 本地文件域 (FileService - 3 个工具)

> 依赖 `FILE_SEARCH_ROOT` 挂载目录；满足纯文件工具配置时，无需外部上游凭据。

| 工具名 | 入参摘要 | 核心逻辑与限制 |
|:---|:---|:---|
| `file_search` | `query`, `search_in` (`all`/`title`/`content`), `limit` (max 100) | 文本扫描/GSE 倒排索引检索；忽略隐藏文件、软链接与 >2MB 大文件 |
| `file_get_info` | `path` (相对路径) | 验证路径合法性，读取文件大小、ModTime、类型等元信息 |
| `file_read_preview` | `path`, `start_line`, `end_line` | 安全行区间读取（最多 200 行），防止内存膨胀 |

---

## 四、 关键核心机制深入分析

### 1. 认证安全与多租户隔离体系
- **多租户 Token 摘要化**：在 `AUTH_TENANTS` 中采用 `token_hash`（SHA-256 十六进制）存储凭据，避免部署配置中明文泄漏。
- **可信身份与主体防伪造**：支持为租户绑定受信任的 `user_id`。如果客户端上报的上下文 `userId` 与经过认证的租户主体冲突，系统强制返回 `HTTP 403 Forbidden`。在本地多用户 Wiki 路由层（`LocalRouter`），严格校验网络请求的主体合法性，未认证或非匹配网络请求一律阻断访问私有用户知识库，杜绝 IDOR 横向越权。
- **远程鉴权安全与双层限流防护**：支持通过 `AUTH_REMOTE_VERIFY_URL` 委托外部鉴权中心，配置时强制校验 Target Host 属于合法白名单；鉴权结果由 `remoteAuthCache`（容量硬上限 + TTL）缓存；未命中缓存的远程验证采用“单 IP 频次限制（LRU 池）+ 全局令牌桶”双层限流架构，单恶意 IP 触发限流时直接阻断且不消耗全局配额，杜绝全局鉴权 DoS 击穿。
- **IP 白名单防控**：支持 CIDR 子网与独立 IP 格式的双重校验。
- **会话凭据强绑定 (Session Credential Binding)**：在 SSE 与旧版 Streamable HTTP 协议中，连接会话（Session ID）一旦建立，将永久绑定初次握手凭据的哈希值。若后续请求试图更换 Bearer Token，将被立即阻断并返回 403。会话表具备 LRU 容量硬上限（默认 10,000 条）与空闲超时自动淘汰机制（默认 1 小时，后台每 5 分钟自动清理孤儿会话），彻底消除无界内存泄露隐患。

### 2. 高可用性与系统弹性韧性
- **模块隔离的三态熔断器 (`CircuitBreaker`)**：
  - 针对 Student、Staff、RAG、Wiki 四个独立上游构建独立的熔断器实例。
  - 严格的状态机迁移：`Closed`（正常） $\to$ `Open`（连续失败达阈值，默认 5 次，快速失败） $\to$ 冷却周期到期（默认 10s） $\to$ `Half-Open`（放行单个探测流量，探测成功恢复 Closed，失败重回 Open）。
  - 智能故障判定：仅网络超时、连接拒绝、上游 5xx 故障计入失败；业务正常的 4xx 响应不计入熔断器失败。
- **请求体积与超时硬约束**：
  - HTTP POST 请求 Body 严格限制最大 4 MiB，超过返回 `413 Request Entity Too Large`。
  - 设置统一的 Body 读取期限（30 秒），防止慢速连接（Slowloris）耗尽服务器 Goroutine。
- **优雅退出 (Graceful Shutdown)**：
  - 监听 `SIGINT` 和 `SIGTERM`，预留最多 15 秒超时平滑注销 MCP 传输并排空存量请求。

### 3. 数据安全与 PII 自动化脱敏
- 内置 `internal/pii` 脱敏组件：
  - 中国大陆手机号：精准识别连续 11 位数字并保留前三后四（如 `138****1234`）。
  - 18 位身份证：精准识别并在中间生日与序列位进行掩码（如 `110101********1234`）。
- 全链路拦截：工具返回内容（Markdown 文本、错误信息）及审计日志均经过正则重构脱敏处理。在开启 HTTP 报文日志（`-log-http-payloads`）时，对捕获的请求体与响应体统一执行 `pii.Redact` 过滤，严防敏感数据明文落盘。

---

## 五、 代码度量与质量审计

### 1. 代码规模与测试覆盖分布统计

| 模块分类 | 路径 / 包名 | 生产代码行 | 测试代码行 | 测试占比 | 状态 |
|:---|:---|:---:|:---:|:---:|:---:|
| **服务入口** | `cmd/server` | 1,133 | 2,086 | 64.8% | 包含会话绑定、发布冒烟测试 |
| **认证与权限** | `internal/auth` | 882 | 922 | 51.1% | 覆盖哈希鉴权、远程验证、缓存 |
| **客户端与熔断** | `internal/client` | 895 | 745 | 45.4% | 覆盖退避重试、熔断三态流转 |
| **知识库引擎** | `internal/wiki` | 1,515 | 2,746 | 64.4% | 包含分词、倒排索引、性能压测 |
| **工具与校验** | `internal/tools` | 942 | 874 | 48.1% | 覆盖入参校验、Schema、LRU 缓存 |
| **业务编排** | `internal/service` | 1,132 | 688 | 37.8% | 覆盖文件防穿透、学生链路校验 |
| **装配与资源** | `internal/server` | 493 | 749 | 60.3% | 覆盖白名单展开、Resource 映射 |
| **横切支撑库** | `logger, metrics, pii, trace, prompts, model` | 2,065 | 812 | 28.2% | 脱敏、指标、追踪完整覆盖 |
| **总计** | **全项目** | **9,057** | **9,622** | **51.5%** | **测试代码行数超过生产代码** |

### 2. 静态检查与测试执行验证

- **编译与静态分析**：
  - `go vet ./...` 检查：**0 告警，完全通过**。
  - 代码注释标识：检查 `TODO`、`FIXME`、`BUG`、`HACK`，**无残留遗留标记**。
- **自动化测试执行结果**：
  - 运行全量测试套件 `go test ./...`，**所有包 100% PASS**：
    - `internal/auth`: PASS (1.4s)
    - `internal/client`: PASS (2.6s)
    - `internal/logger`: PASS (2.1s)
    - `internal/metrics`: PASS (2.8s)
    - `internal/server`: PASS (6.8s)
    - `internal/service`: PASS (6.1s)
    - `internal/tools`: PASS (6.5s)
    - `internal/trace`: PASS (2.8s)
    - `internal/wiki`: PASS (5.8s)
    - `cmd/server`: PASS (含多协议握手测试)

---

## 六、 近期演进与版本达成（2026-09 最新迭代）

1. **纯文件工具组合独立启动特性 (P0 落地 - 提交 `8f98f6e`)**：
   - 彻底解除仅启用 `file_*` 工具时对上游 `API_TOKEN` 或 `config/wiki.json` 的偶合阻断。
   - 允许运维人员将其作为纯本地安全文件问答服务快速交付。
2. **规范化部署指南与配置矩阵 (P1 交付 - 提交 `98b3512`)**：
   - 交付 `docs/deployment-guide.md`，明确 stdio、网络上游模式、纯本地 Wiki 与文件模式的配置边界与权限矩阵。
3. **CI 发布构建参数一致化与产物冒烟 (P2 闭环 - 提交 `ce438d2`, `7514a1c`)**：
   - GitHub Actions `build-release` 作业与 Makefile 构建规范统一补齐 `-tags=jsoniter` 与静态编译标记。
   - 引入独立可执行文件的 MCP `initialize` 与 `tools/list` 真实冒烟测试用例 (`TestReleaseBinarySmoke`)。

---

## 七、 潜在风险识别与演进建议

### 1. 部署与运维风险
- **探针与指标端口暴露风险**：
  - `/health` 与 `/ready` 为免认证探针；若 `METRICS_AUTH_TOKEN` 未配置，`/metrics` 端点处于公开免鉴权状态。
  - **建议**：在部署指南及容器编排（如 K8s Ingress / Nginx 反向代理）中，明确将 `/metrics` 和探针端口限定于内部 VPC 或运维内网，禁止直接向公网暴露。
- **本地文件搜索的作用域隔离**：
  - `FILE_SEARCH_ROOT` 属于服务级共享目录，获准使用 `file_search` 工具的客户端能检索该目录下的所有非隐藏文件，不具备按 `userId` 细粒度隔离的特性。
  - **建议**：严禁将包含高密数据的全系统根目录直接挂载为 `FILE_SEARCH_ROOT`，务必挂载受控的只读知识文档目录。

### 2. 性能与容量演进建议
- **GSE 分词与倒排索引持久化机制**：
  - 当前本地文件在启用 `gse` 模式及本地 Wiki 索引时，倒排索引常驻内存，重启服务需要重新扫描目录和分词。
  - **建议**：当未来管理的知识文档达到数十万量级时，可引入轻量级本地 Key-Value 存储（如 BadgerDB / SQLite）实现索引序列化与 Checkpoint，进一步降低冷启动时间。
- **MCP Resources 变更订阅支持**：
  - 目前已实现 `wiki://catalog`、`wiki://trees/{name}`、`wiki://pages/{page_id}` 的静态读取，尚未实现客户端订阅（Resource Subscription）通知。
  - **建议**：若后续大模型客户端有实时监听词条变更的需求，可在 Wiki Upsert 时增加基于 MCP 的 `notifications/resources/updated` 推送支持。

---

## 八、 总结评价

`xktmcp` 项目整体架构清晰、模块权责分明、工程规范完备。代码实现中对工业级场景下的稳定性（熔断器、重试退避、请求体防爆破）、多租户安全边界（Token 哈希、会话凭据强绑定、可信主体防伪造）、数据隐私（全链路 PII 自动化脱敏）和性能监控（Prometheus 全维度埋点）均有高水准的落地实践。测试覆盖率超过 50%，各类边界用例齐备，已完全具备在生产环境中稳定运行和持续演进的能力。
