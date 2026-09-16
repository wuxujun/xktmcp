# xktmcp 优化会话总结

> 整理时间：2026-09-15
> 范围：读取并核验 `docs/OPTIMIZATION_ANALYSIS.md` 后完成的优化工作

## 已完成

### Wiki 缓存一致性

- `wiki_upsert_page` 成功后清理完整 `wiki:` 缓存命名空间。
- 覆盖共享默认 Wiki 下不同用户的旧缓存，避免写入后继续读到旧快照。
- 增加跨用户缓存失效回归测试；非 Wiki 缓存不受影响。

### 上游 HTTP 可观测性与重试

- 新增 `xkt_upstream_request_duration_seconds`，按 API、HTTP 方法、状态码记录每次上游尝试耗时。
- 新增 `xkt_upstream_retries_total`，记录 API 重试次数。
- 重试退避改为 Full Jitter，等待时间为 `[0, backoff)`，保留 context 取消能力。

### 熔断器和 Wiki 索引指标

- 新增 `xkt_circuit_breaker_state`：`0=closed`、`1=half-open`、`2=open`。
- 新增 Wiki 索引文档数、最后成功刷新时间、刷新耗时指标。
- Wiki 指标使用固定 `local` 标签，避免目录路径形成高基数标签。

### 认证与配置

- `/metrics` 支持可选 `METRICS_AUTH_TOKEN` Bearer Token 鉴权；未配置时保持兼容。
- 支持 `AUTH_REMOTE_CACHE_POSITIVE_TTL` 和 `AUTH_REMOTE_CACHE_NEGATIVE_TTL`，使用 Go duration 格式；默认仍为 5 分钟和 30 秒。

### 工具配置和测试隔离

- `MCP_ENABLED_TOOLS` 支持 `wiki_*`、`student_*` 等已知工具前缀；未知前缀仍拒绝启动。
- `TestLiveWikiSearchPort8081` 只有在 `MCP_RUN_LIVE_TESTS=true` 时运行，避免默认测试依赖本地服务。

## 验证结果

以下定向测试均通过：

```sh
go test ./internal/tools -run 'TestWiki(UpsertPageHandler|SearchHandlerCache)' -count=1
go test ./internal/client ./internal/metrics -count=1
go test ./internal/wiki ./internal/server -count=1
go test ./cmd/server -count=1
go test ./... -p 1 -count=1 -timeout=120s
```

`git diff --check` 通过。

并行执行 `go test ./...` 曾出现超过 100 秒无输出的情况；串行执行 `-p 1` 已完整通过。后续如需恢复并行测试，应单独定位包间资源竞争或测试生命周期问题。

## 未完成事项

### 需要接口设计后再做

- Wiki `if_match` 乐观锁：需要扩展 `WikiUpsertPageArgs`、service、backend、router 和远程 API 的公开接口，并定义版本不匹配及远程不支持时的错误语义。
- 租户感知的 `tools/list`：当前 HTTP 层已校验 `tools/call` 的 `allowed_tools`，但工具列表仍返回全部已注册工具；需要把租户权限安全地传入列表处理流程。

### 需要数据或专项基准后再做

- IDF 搜索评分：会改变排序和公开分数，应先准备真实查询集、相关性指标及平滑公式。
- atomic 快照替换：当前索引重建主要在写锁外，需先测量刷新并发下的锁等待和 P99。
- 正文按需读取：正文仍用于短语匹配、反向链接、追加写入和分词复用，需先做堆剖析并重新设计索引数据。
- 独立缓存实例：先观察缓存命中率、淘汰率和容量压力，再决定是否拆分。
- `student_batch_get`：先确认真实调用量、客户端并发模式和上游批量接口能力。

### 可选的低优先级工作

- 缓存大小 Gauge 与淘汰计数。
- `/health`/`/ready` 深度诊断能力；保持存活探针和就绪探针语义分离。
- 进一步补充 `/metrics` 部署文档及 Prometheus Bearer Token 抓取示例。

## 推荐后续顺序

1. 先审查并提交当前改动，保留可回滚检查点。
2. 如果存在多人协作编辑需求，再设计并实现 Wiki `if_match`。
3. 如果存在多租户 Agent 使用场景，再设计 `tools/list` 权限过滤。
4. 根据生产指标决定是否开展搜索评分、缓存拆分和内存优化。

## 当前工作区注意事项

`docs/PROJECT_SUMMARY.md` 是已有的未跟踪文件，本次未修改；提交时应确认是否属于本次工作范围。
