# 已完成功能归档（2026-09-29）

> 归档目的：集中记录 `docs/` 中已经完成、闭合或只保留追溯价值的功能与加固项，避免历史文档被误读为当前待办。当前运行配置与部署说明仍以 `../PROJECT_ANALYSIS.md`、`../PROJECT_SUMMARY.md`、`../deployment-guide.md` 为准。

## 1. 平台与传输能力

| 功能 | 完成状态 | 主要来源 |
|---|---|---|
| stdio、SSE、Streamable HTTP 三种 MCP 传输 | 已实现 | `../PROJECT_ANALYSIS.md`、`../PROJECT_SUMMARY.md`、`../v260818.md` |
| MCP 2026-07-28 协议兼容与 legacy/modern 路由 | 已实现 | `../v260818.md`、`../r260818.md` |
| 网络传输未配置认证时 fail-closed | 已实现 | `../deployment-guide.md`、`../PROJECT_ANALYSIS.md` |
| 有状态 SSE / legacy Streamable HTTP 会话绑定建连凭据 | 已实现 | `../release-readiness-audit-20260916.md`、`../superpowers/specs/2026-09-17-mcp-session-auth-binding-design.md` |
| 纯文件工具发布二进制冒烟验证 | 已完成 CI / 本地验证；目标环境仍需验收 | `../release-validation-2026-09-24.md` |

## 2. 工具与业务能力

| 功能 | 完成状态 | 主要来源 |
|---|---|---|
| Student 工具：`student_search`、`student_get`、`student_order`、`student_exam` | 已实现 | `../260714.md`、`../260728.md`、`../PROJECT_SUMMARY.md` |
| Staff 工具：`staff_search` | 已实现 | `../260714.md`、`../CODE_REVIEW.md`、`../PROJECT_SUMMARY.md` |
| RAG 工具：`rag_search` 参数契约、语义改写与结果构建 | 已实现；改写结果以 `MainQuery` 暴露 | `../CODE_AUDIT.md`、`../OPTIMIZATION_ANALYSIS.md` |
| Wiki 5 个工具：Search、GetPage、ListTree、Upsert、Backlinks | 已实现，支持 HTTP / Local 后端 | `../r260818.md`、`../wiki-progress-report-260901.md` |
| 本地文件 3 个工具：Search、Info、Preview | 已实现；配置 `FILE_SEARCH_ROOT` 后注册 | `../PROJECT_SUMMARY.md`、`../release-validation-2026-09-24.md` |
| `MCP_ENABLED_TOOLS` 精确名称和 `*_` 前缀白名单 | 已实现，未知名称/前缀启动失败 | `../OPTIMIZATION_ANALYSIS.md`、`../deployment-guide.md` |
| 纯文件工具组合无上游依赖启动 | 已实现 | `../next-phase-design-2026-09-24.md`、`../release-validation-2026-09-24.md` |

## 3. Wiki Local 与 Resources

| 功能 | 完成状态 | 主要来源 |
|---|---|---|
| Wiki HTTP / Local 双后端切换 | 已实现 | `../r260818.md`、`../wiki-progress-report-260901.md` |
| Local Markdown 搜索、页面读取、目录树、Upsert、Backlinks | 已实现 | `../r260818.md`、`../wiki-progress-report-260901.md` |
| GSE 中文分词、`zh` / `zh_s` 字典配置 | 已实现并完成真实语料对比 | `../wiki-search-session-summary.md`、`../wiki-session-summary-20260913.md` |
| Backlinks 候选桶 / 索引优化 | 已实现，真实语料阶段耗时显著下降 | `../wiki-search-session-summary.md`、`../superpowers/plans/2026-09-01-wiki-backlinks-index.md` |
| Wiki Resources Catalog、Tree、Page Template（Phase 1–2） | 已实现，默认关闭，仅 Local 模式可启用 | `../wiki-resources-capability-design-260901.md`、`../superpowers/plans/2026-09-01-wiki-resources-phase-1-2.md` |
| Resource ACL 复用租户 `allowed_tools` | 已实现 | `../release-readiness-audit-20260916.md`、`../superpowers/specs/2026-09-17-mcp-session-auth-binding-design.md` |
| ResourceLink / `resources.link_base_url` | 已实现 | `../wiki-url-fix.md`、`../PROJECT_SUMMARY.md` |

> 未归档为“已完成”的部分：Resources 订阅通知 Phase 3 尚未实现，`subscriptions_enabled=true` 当前会返回配置错误。

## 4. 认证与多租户安全

| 功能 / 加固项 | 完成状态 | 主要来源 |
|---|---|---|
| 静态 Bearer Token、`AUTH_TENANTS`、远程验证、IP 白名单 | 已实现 | `../PROJECT_ANALYSIS.md`、`../deployment-guide.md` |
| 远程认证目标主机白名单与 SSRF 防护 | 已实现 | `../CODE_AUDIT.md`、`../security-vulnerability-analysis.md` |
| 远程认证有界缓存和正/负 TTL 配置 | 已实现 | `../OPTIMIZATION_ANALYSIS.md`、`../superpowers/specs/2026-08-30-auth-boundary-hardening-design.md` |
| 远程认证单 IP + 全局双层限流 | 已实现 | `../security-vulnerability-analysis.md` |
| 认证主体与请求 `userId` 冲突拒绝 | 已实现 | `../CODE_REVIEW.md`、`../superpowers/plans/2026-08-31-auth-boundary-hardening.zh-CN.md` |
| 多用户 Local Wiki IDOR / 主体伪造防护 | 已修复闭合 | `../security-vulnerability-analysis.md` |
| 会话绑定 LRU 容量上限与空闲清理 | 已修复闭合 | `../security-vulnerability-analysis.md` |
| 租户工具 ACL 对 `tools/call` 生效 | 已实现 | `../r260829.md`、`../release-readiness-audit-20260916.md` |

## 5. 可观测性、弹性与运维

| 功能 | 完成状态 | 主要来源 |
|---|---|---|
| Prometheus 工具调用、缓存命中、耗时指标 | 已实现 | `../PROJECT_SUMMARY.md` |
| 上游 HTTP 请求耗时与重试指标 | 已实现 | `../OPTIMIZATION_ANALYSIS.md`、`../SESSION_SUMMARY_2026-09-15.md` |
| 熔断器状态 Gauge 与状态转换指标 | 已实现 | `../OPTIMIZATION_ANALYSIS.md`、`../superpowers/specs/2026-09-01-circuit-breaker-config-design.md` |
| Wiki 索引文档数、最后刷新时间、刷新耗时指标 | 已实现 | `../OPTIMIZATION_ANALYSIS.md` |
| `/metrics` 独立 `METRICS_AUTH_TOKEN` 强制鉴权 | 已实现；未配置返回 503 | `../security-vulnerability-analysis.md`、`../deployment-guide.md` |
| 上游重试 Full Jitter 且可被 context 取消 | 已实现 | `../OPTIMIZATION_ANALYSIS.md` |
| `/health`、`/ready` 探针 | 已实现；仍需部署层网络限制 | `../deployment-guide.md` |

## 6. PII、日志与文件安全

| 功能 / 加固项 | 完成状态 | 主要来源 |
|---|---|---|
| 手机号、身份证等 PII 脱敏 | 已实现 | `../PROJECT_SUMMARY.md` |
| HTTP payload 调试日志脱敏 | 已修复闭合 | `../security-vulnerability-analysis.md` |
| 工具共享缓存键 Base64 编码防冒号碰撞 | 已修复闭合 | `../security-vulnerability-analysis.md` |
| 文件服务路径清洗与 `..` 防护 | 已修复闭合 | `../security-vulnerability-analysis.md` |
| GSE 文件搜索索引全文常驻内存优化 | 已优化闭合 | `../security-vulnerability-analysis.md` |

## 7. 已闭合审计项索引

以下历史审计/检查中列出的主要高优先级问题已闭合，可作为追溯记录保留：

- `../CODE_REVIEW.md`：`userId` fallback 审计、Staff 缓存隔离、SSE/HTTP 注入一致性等后续已修复。
- `../CODE_AUDIT.md`：URL 转义、Body 上限、远程认证配置、RAG 参数等问题后续已修复或过时。
- `../r260829.md`：P0/P1 认证边界、ACL、远程认证缓存、POST 重试、RAG 参数等主要问题已闭合。
- `../security-vulnerability-analysis.md`：High / Medium / Low / Info 风险项当前均为 Closed。

## 8. 未归档为完成的事项

以下仍按待优化或待验收处理，不应视为已完成功能：

| 事项 | 当前状态 |
|---|---|
| `tools/list` 按租户 `allowed_tools` 过滤 | 待优化；`tools/call` 已受控，但列表仍可能展示不可调用工具。 |
| Wiki `update/append` 乐观锁 / `if_match` | 待优化；多人协作编辑仍可能 last-write-wins。 |
| Wiki Resources 订阅通知 Phase 3 | 未实现；需目标客户端明确需求后再做。 |
| Claude Desktop / Cursor Resources 兼容性实测 | 待补充具名版本验证。 |
| 目标环境发布验收 | 待执行；CI 和本地发布门禁不能替代生产/目标环境验收。 |
| 搜索相关性、缓存拆分、深度诊断端点 | 需生产数据或专项基准后再决策。 |

## 9. 维护规则

1. 新功能完成后，如已有设计/计划文档，请在本归档中新增一行功能索引，不直接删除原文档。
2. 仍含待办、风险或验收步骤的文档，不归入“已完成”结论；只标明“部分完成”。
3. 当前行为以源码、README、部署指南和项目分析报告为准；历史报告只用于追溯当时背景。
