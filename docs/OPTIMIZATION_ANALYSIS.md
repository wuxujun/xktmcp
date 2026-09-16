# xktmcp 可待优化功能分析

> 分析时间：2026-09-13  
> 范围：`github.com/wuxujun/xktmcp`（全量代码审查）

---

## 优先级说明

| 级别 | 标准 |
|------|------|
| 🔴 高 | 影响稳定性、安全性或显著性能问题 |
| 🟡 中 | 功能完善、可观测性提升、开发体验改进 |
| 🟢 低 | 代码质量、可扩展性、锦上添花 |

---

## 一、缓存层优化

### 🔴 1.1 写入后无主动缓存失效

**文件：** [`internal/tools/cache.go`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/tools/cache.go)

**问题：** `wiki_upsert_page` 写入 Wiki 词条后，`wiki_search` / `wiki_get_page` / `wiki_list_tree` 的缓存条目不会被主动失效。用户写入后立刻搜索可能读到旧数据，直到 TTL（2~10 分钟）自然过期。

```
write wiki_upsert → 缓存旧快照未失效
  ↓ 2分钟内搜索 → 返回写入前的旧结果  ← 数据不一致
```

**改进方向：** 在 `WikiUpsertPageHandler` 成功后，调用 `sharedCache.DeletePrefix("wiki:")` 主动失效 Wiki 命名空间下的全部缓存。

---

### 🟡 1.2 所有工具共享单一缓存实例（LRU 竞争）

**文件：** [`internal/tools/cache.go#L191`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/tools/cache.go#L189-L191)

**问题：** `student_*`、`rag_search`、`wiki_*` 共用 `sharedCache`（上限 1024 条）。热门 RAG 查询可能把低频但有效的 Wiki 缓存项挤出，造成 Wiki 频繁回源。

**改进方向：** 按工具组分配独立 `MemoryCache` 实例，各自配置容量上限。

```go
var (
    studentCache = NewMemoryCacheWithOptions(512, time.Minute)
    ragCache     = NewMemoryCacheWithOptions(256, time.Minute)
    wikiCache    = NewMemoryCacheWithOptions(512, time.Minute)
)
```

---

### 🟢 1.3 缺少缓存大小 Gauge 指标

**问题：** 只有命中/未命中计数，没有当前条目数 Gauge，无法观测缓存容量接近上限的趋势。

**改进方向：** 在 `Set` / `removeElement` 时同步更新 `xkt_cache_size` Gauge。

---

## 二、可观测性（Metrics）

### 🔴 2.1 缺少上游 HTTP 请求维度指标

**文件：** [`internal/metrics/metrics.go`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/metrics/metrics.go)

**问题：** 当前指标只覆盖 MCP 工具层，**缺少上游 HTTP 调用**的独立指标。工具调用慢时无法区分是 MCP 层处理慢还是上游 API 响应慢。

**改进方向：** 在 `doRequestWithRetry` 增加 Histogram：

```go
upstreamDuration = promauto.NewHistogramVec(..., []string{"api", "method", "status_code"})
upstreamRetries  = promauto.NewCounterVec(...,   []string{"api"})
```

---

### 🟡 2.2 熔断器状态无 Gauge 指标

**文件：** [`internal/client/breaker.go`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/client/breaker.go)

**问题：** 只有 `xkt_circuit_breaker_transitions_total`（转换次数），没有**当前状态 Gauge**，告警规则无法直接判断某 API 是否处于 Open 状态。

**改进方向：** 增加 `xkt_circuit_breaker_state`（Gauge，0=closed/1=half-open/2=open），在 `setState` 时同步更新。

---

### 🟡 2.3 Wiki 索引无 Prometheus 指标

**问题：** `LocalSearcher` 的文档数、最后刷新时间、刷新耗时均未上报，难以发现「索引长时间未刷新」或「文档数突降」等异常。

**改进方向：** 新增指标：
```
xkt_wiki_index_documents_total    Gauge     当前索引文档数
xkt_wiki_index_last_refresh_unix  Gauge     最后刷新时间戳
xkt_wiki_index_refresh_duration   Histogram 刷新耗时
```

---

## 三、Wiki 本地搜索算法

### 🟡 3.1 评分缺乏 IDF 加权

**文件：** [`internal/wiki/local_search.go`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/wiki/local_search.go)

**问题：** `scoreDocument` 基于词频（TF）打分，缺少 **IDF（逆文档频率）** 加权。高频通用词（如「系统」「管理」）在所有文档中得分差异小，搜索排序精度下降。

**改进方向：** `refresh` 时预计算各词 IDF（`log(总文档数 / 含该词文档数)`），在 `scoreDocument` 中乘以 IDF 权重（TF-IDF）。

---

### 🟡 3.2 索引刷新写锁短暂阻塞搜索

**文件：** [`internal/wiki/local_search.go#L170-L200`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/wiki/local_search.go#L170-L200)

**问题：** 新快照写入需要 `mu.Lock()`，会短暂阻塞所有搜索请求。文档量大时刷新耗时增加，搜索 P99 延迟会出现毛刺。

**改进方向：** 用 `atomic.Pointer[snapshot]` 做无锁原子替换，彻底消除写锁等待：

```go
type snapshot struct {
    documents []localDocument
    termIndex map[string][]int
}
var snap atomic.Pointer[snapshot]
```

---

### 🟢 3.3 全文内容常驻内存，内存压力大

**问题：** 每个 `localDocument` 都保存完整 `content` 字段（全文）。1000 篇文章 × 平均 10KB ≈ 10MB+ 常驻内存，文档量增长后压力显著。

**改进方向：** 搜索索引只保留分词结果；`wiki_get_page` 时按需从文件系统读取正文，而非常驻内存。

---

## 四、安全与认证

### 🔴 4.1 多租户工具权限校验应下沉到工具层

**文件：** [`internal/server/register.go#L185-L223`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/server/register.go#L185-L223)

**问题：** MCP `tools/list` 会返回所有已注册工具，无论租户是否有权调用。AI Agent 无法感知自己可以调用哪些工具，可能尝试调用后才收到拒绝，影响 Agent 决策质量。

**改进方向：** 在 `wrapToolHandler` 中从 context 取出已认证租户信息，校验工具名是否在 `allowed_tools` 内，不在则返回 MCP 级错误（`IsError=true`）；同时可在 `tools/list` 响应中只列出租户有权限的工具。

---

### 🟡 4.2 `/metrics` 端点无任何访问控制

**文件：** [`cmd/server/main.go#L142-L164`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/cmd/server/main.go#L141-L164)

**问题：** `/metrics` 完全公开，Go 运行时指标（goroutine 数、GC 信息、内存）可能泄露内部系统信息。

**改进方向：** 支持可选 `METRICS_AUTH_TOKEN` 环境变量，为 `/metrics` 添加 Bearer Token 校验。

---

### 🟢 4.3 Remote Token 吊销后缓存窗口问题

**文件：** [`internal/auth/cache.go`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/auth/cache.go)

**问题：** 远程 Token 吊销后，`PositiveTTL` 内缓存的通过结果仍可放行，存在时间窗口风险。

**改进方向：** 提供管理端点（需强鉴权）主动清除指定 Token 缓存；或默认将 `PositiveTTL` 设为较短值（如 1 分钟）。

---

## 五、工具功能扩展

### 🟡 5.1 RAG 改写查询结果不透明

**文件：** [`internal/tools/rag.go`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/tools/rag.go)

**问题：** `rewrite=true` 时，上游对用户 query 做语义改写，但改写后的实际查询词不返回给 Agent，不利于调试和质量评估。

**改进方向：** 在 `RagSearchResponse` 增加 `rewritten_query string` 字段，透传上游改写结果。

---

### 🟡 5.2 缺少批量学员查询工具

**问题：** AI Agent 需要查询多个学员时，必须多轮串行调用 `student_get`，RTT 叠加导致延迟显著。

**改进方向：** 新增 `student_batch_get` 工具，支持一次传入多个 `student_id`，服务端并发请求后合并返回。

---

### 🟢 5.3 `wiki_upsert_page` 缺乏并发冲突检测

**文件：** [`internal/tools/wiki.go#L62-L71`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/tools/wiki.go#L62-L71)

**问题：** 并发写入同一词条可能产生 last-write-wins 冲突；`mode=create` 时若词条已存在行为不明确。

**改进方向：**
- `create` 模式下若词条已存在，返回明确错误（`page_already_exists`）；
- `update` 模式支持 `if_match` 乐观锁参数，检测并发冲突。

---

## 六、开发与运维体验

### 🟡 6.1 重试无 Jitter 抖动，存在惊群风险

**文件：** [`internal/client/client.go#L68-L86`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/client/client.go#L68-L86)

**问题：** 重试使用固定指数退避（100ms→200ms），多个并发请求在同一后端故障后会**同步重试**，产生惊群效应，可能加剧上游压力。

**改进方向：** 加入随机抖动（Full Jitter）：

```go
// Full Jitter: sleep = random(0, backoff)
jitter := time.Duration(rand.Int63n(int64(backoff)))
time.Sleep(jitter)
backoff = min(backoff*2, maxBackoff)
```

---

### 🟢 6.2 `MCP_ENABLED_TOOLS` 不支持前缀通配符

**文件：** [`internal/server/register.go#L144-L164`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/internal/server/register.go#L144-L164)

**问题：** 启用所有 Wiki 工具需枚举 5 个名称，冗长且易遗漏新增工具。

**改进方向：** 支持 `wiki_*`、`student_*` 等前缀通配符展开。

---

### 🟢 6.3 健康检查无法反映上游状态

**文件：** [`cmd/server/main.go#L543-L560`](file:///Users/xujunwu/Documents/IDEAProject/xktmcp/cmd/server/main.go#L543-L560)

**问题：** `/health` 只要进程存活就返回 `ok`，不能反映上游连通性或 Wiki 索引异常。

**改进方向：** 可选深度检查（`?deep=true`），包含：熔断器状态摘要、Wiki 索引最后刷新时间是否超阈值、上游 API 轻量 ping。

---

## 七、优先级汇总

| # | 优化项 | 优先级 | 预估工作量 |
|---|--------|--------|-----------|
| 1.1 | 写入后主动失效 Wiki 缓存 | 🔴 高 | S（1~2h） |
| 2.1 | 上游 HTTP 请求维度指标 | 🔴 高 | S（2~3h） |
| 4.1 | 工具层多租户权限校验 | 🔴 高 | M（3~5h） |
| 1.2 | 工具独立缓存实例 | 🟡 中 | S（1h） |
| 2.2 | 熔断器状态 Gauge 指标 | 🟡 中 | XS（1h） |
| 2.3 | Wiki 索引 Prometheus 指标 | 🟡 中 | S（2h） |
| 3.1 | 搜索评分加 IDF 加权 | 🟡 中 | M（4~6h） |
| 3.2 | 索引刷新无锁原子替换 | 🟡 中 | M（3~4h） |
| 4.2 | `/metrics` 可选鉴权 | 🟡 中 | S（1~2h） |
| 5.2 | `student_batch_get` 批量工具 | 🟡 中 | M（4h） |
| 6.1 | 重试加 Jitter 抖动 | 🟡 中 | XS（30min） |
| 1.3 | 缓存大小 Gauge 指标 | 🟢 低 | XS（30min） |
| 3.3 | 全文内容按需读取 | 🟢 低 | M（3h） |
| 4.3 | Token 吊销窗口缩短 | 🟢 低 | XS（配置调整） |
| 5.1 | RAG 改写查询透传 | 🟢 低 | S（1h，依赖上游） |
| 5.3 | Wiki 写入冲突检测 | 🟢 低 | M（3h） |
| 6.2 | `MCP_ENABLED_TOOLS` 通配符 | 🟢 低 | S（1h） |
| 6.3 | 深度健康检查 | 🟢 低 | M（3h） |

> **建议起点：**
> 1. **1.1**（Wiki 写入缓存失效）：改动极小，5 行代码，立即消除数据不一致；
> 2. **6.1**（重试 Jitter）：30 分钟内完成，消除潜在惊群风险；
> 3. **2.1**（上游 HTTP 指标）：显著提升排障效率，建议下一个 Sprint 优先完成。

---

## 八、代码核验与修订结论（2026-09-14）

本节根据当前代码、现有测试和 Wiki 搜索基准测试复核上面的建议。原分析中的“问题”不能直接视为已确认故障；涉及性能和相关性的项目应先有生产数据或专项基准。

### 已被当前实现部分或全部覆盖的项目

- **1.1 写入后缓存失效：部分成立。** `WikiUpsertPageHandler` 成功后已经调用 `invalidateWikiCache`，并清理当前有效用户的搜索、页面、树和反向链接缓存；无用户时清理整个 Wiki 命名空间。共享默认 Wiki 在不同用户之间复用时，其他用户缓存仍可能保持旧值，因此剩余问题是“跨用户共享目录的失效范围”，不是缺少失效逻辑。
- **4.1 工具权限校验：部分成立。** HTTP 认证层已经对租户的 `tools/call` 校验 `allowed_tools`，不能据此认定当前可以越权调用；但 `tools/list` 仍返回全部已注册工具，租户感知的列表过滤仍有 Agent 体验收益。工具层再次校验属于纵深防御，需要先把租户权限放入上下文。
- **5.1 RAG 改写结果不透明：不成立。** `RagSearchResponse.MainQuery` 已返回实际提交给上游的查询词；新增同义的 `rewritten_query` 字段会扩大公开响应而不增加必要信息。
- **5.3 create 语义：部分成立。** 本地 Wiki 的 `create` 已明确拒绝同标题页面和已存在目标路径，并且单实例写入由互斥锁串行化。当前缺少的是 update/append 的客户端版本条件（如 `if_match`），因此多客户端协作时仍可能发生 last-write-wins。

### 建议保留，但调整优先级或实施条件

| 项目 | 修订判断 | 建议 |
|---|---|---|
| 1.2 独立缓存实例 | 共用 1024 条缓存属实，但尚无淘汰/命中数据证明竞争严重 | 先补容量、淘汰和命中率观测，再决定是否拆分；避免无依据地固定总容量配额 |
| 1.3 缓存大小 Gauge | 当前只有命中/未命中计数，缓存已有 `Len()` | 低优先级，可与淘汰计数一起补 |
| 2.1 上游 HTTP 指标 | 当前缺少请求耗时、状态码和重试维度 | 值得做，中优先级，能直接区分工具慢和上游慢 |
| 2.2 熔断器状态 Gauge | 当前只有状态转换计数，没有当前状态 | 值得做，中优先级 |
| 2.3 Wiki 索引指标 | 缺少刷新耗时、成功时间和文档数指标，已有 `DocumentCount()` | 值得做；“长时间未刷新”必须结合按需刷新语义判断 |
| 4.2 `/metrics` 鉴权 | 应用层默认无鉴权；是否公开还取决于反向代理和网络隔离 | 公网可达时优先；已有网络隔离时优先级降低，新增 Token 会影响抓取配置 |
| 4.3 Token 吊销窗口 | 远程验证正缓存默认 5 分钟，当前没有 TTL 环境配置 | 有即时撤销要求时处理；优先提供可配置 TTL，注意增加上游验证流量 |
| 6.1 重试 Jitter | 当前使用固定 100ms、200ms 指数退避 | 值得做，中优先级；必须继续使用可取消的 timer，不直接使用不可取消的 `time.Sleep` |
| 6.2 工具名前缀通配符 | 当前只接受已知工具的精确名称 | 低优先级，属于配置便利性；需定义未来新增工具是否自动启用 |
| 6.3 深度健康检查 | `/health` 是存活探针，已有 `/ready` 初始化检查 | 低优先级；如有需求应扩展独立 readiness/诊断语义，避免上游短暂故障触发重启 |

### 暂缓，先补证据

- **3.1 IDF 加权：** 当前评分除正文词频外，还包含标题、摘要、短语和精确匹配奖励；倒排表可支持 DF，但简单乘以 `log(N/df)` 会改变公开分数，且 `df=N` 时可能变成零。应先建立真实查询集和相关性指标，并设计平滑公式与兼容策略。
- **3.2 无锁原子替换：** 索引重建、文件读取、分词和倒排表构建均在写锁外，写锁内主要是快照发布；首个过期请求的同步重建耗时也不会因 atomic 消失。应先测量刷新并发下的锁等待和 P99，再决定是否改造。
- **3.3 正文按需读取：** 正文仍用于短语匹配、反向链接、追加写入和分词复用。仅改 `GetPage` 为读文件会破坏这些依赖或引入新的 I/O 一致性问题；应先做堆剖析，再设计索引正文、指纹和页面读取策略。
- **5.2 批量学员查询：** 缺少批量工具属实，但尚无调用量和客户端并发证据证明必须新增公开工具。应先确认实际使用模式及上游批量接口能力。

### 修订后的实施顺序

1. 共享默认 Wiki 场景的跨用户缓存失效范围；
2. 上游 HTTP 指标和重试 Jitter；
3. 熔断器状态、Wiki 索引刷新指标；
4. 按部署要求处理 `/metrics` 访问控制和 Token TTL；
5. 在获得搜索相关性、并发延迟和堆占用数据后，再评估 IDF、atomic 快照和正文剥离；
6. 有明确协作编辑需求时增加 Wiki 版本条件更新。

复核使用的定向测试均通过：`internal/tools`、`internal/client`、`internal/metrics`、`internal/auth`、`internal/server`、`cmd/server` 和 `internal/wiki` 的相关测试。已实施的改动未改变运行时配置或公共 API。

### 已实施进展（2026-09-14）

- Wiki 写入后的缓存失效已统一清理 `wiki:` 命名空间，覆盖共享默认 Wiki 的跨用户旧缓存；新增回归测试验证其他工具缓存不受影响。
- 已增加 `xkt_upstream_request_duration_seconds`（按 API、HTTP 方法和状态码）及 `xkt_upstream_retries_total` 指标。
- 已增加 `xkt_circuit_breaker_state` Gauge（0=closed、1=half-open、2=open），并在熔断器创建和状态转换时同步更新。
- 已增加 Wiki 索引文档数、最后成功刷新时间和刷新耗时指标，索引标签使用固定 `local` 值，避免把目录路径作为高基数标签。
- `/metrics` 已支持可选 `METRICS_AUTH_TOKEN`；未配置时保持兼容，配置后要求 Bearer Token。
- 已支持 `AUTH_REMOTE_CACHE_POSITIVE_TTL` 和 `AUTH_REMOTE_CACHE_NEGATIVE_TTL`，使用 Go duration 格式；未配置时保持 5 分钟和 30 秒默认值。
- `TestLiveWikiSearchPort8081` 已改为显式设置 `MCP_RUN_LIVE_TESTS=true` 才运行，避免默认全量测试依赖本地 8081 服务。
- `MCP_ENABLED_TOOLS` 已支持 `wiki_*`、`student_*` 等已知工具前缀；未匹配的前缀仍会拒绝启动，避免误启用未知工具。
- 上游重试退避已改为可被 context 取消的 Full Jitter，等待时间取 `[0, backoff)`，指数退避上限逻辑保持不变。
