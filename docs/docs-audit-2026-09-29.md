# docs 目录 Markdown 核查总结（2026-09-29）

## 1. 核查范围

本次核查覆盖 `docs/` 下 37 个 Markdown 文件，包括顶层报告、部署/发布文档、Wiki Resources 设计与兼容性记录，以及 `docs/superpowers/{plans,specs}` 下的实施计划和设计稿。

核查依据为当前源码与既有文档交叉校验，重点关注：工具注册数量、`/metrics` 当前安全行为、Wiki Resources 实现阶段、纯文件工具独立启动、认证/会话绑定、安全漏洞闭合状态，以及仍需优化的事项。

## 2. 当前功能实现基线

| 能力 | 当前实现状态 | 代码/文档依据 |
|---|---|---|
| MCP 传输 | stdio、SSE、Streamable HTTP 已实现；网络传输未配置认证时 fail-closed | `cmd/server/main.go`、`docs/deployment-guide.md` |
| 工具注册 | 注册表定义 14 个工具：Student 4、Staff 1、RAG 1、Wiki 5、File 3；文件工具需 `FILE_SEARCH_ROOT` | `internal/server/register.go` |
| 工具白名单 | `MCP_ENABLED_TOOLS` 支持精确名称和已知前缀，如 `student_*`、`wiki_*`、`file_*`；未知名称/前缀启动失败 | `internal/server/register.go` |
| 纯文件工具启动 | `file_*` 或任意文件工具组合可在无 `API_TOKEN`、无 Wiki 配置时独立启动；网络传输仍需 MCP 认证 | `internal/server/register.go`、`docs/release-validation-2026-09-24.md` |
| Wiki 后端 | 支持 HTTP 与 Local Markdown 双后端；Local 支持多用户映射、GSE 分词、索引刷新、Upsert、Backlinks | `internal/wiki/`、`internal/service/wiki_service.go` |
| Wiki Resources | Local 模式可选注册 Catalog、Tree、Page Resource Template；Phase 1–2 已落地；订阅通知未实现，`subscriptions_enabled=true` 会报错 | `internal/server/wiki_resources.go`、`internal/wiki/config.go` |
| Resource Link | `wiki_search` 可返回 `ResourceLink`；配置 `resources.link_base_url` 后支持 HTTPS 链接 URI，同时兼容 `wiki://page/{page_key}` | `internal/tools/wiki.go`、`internal/wiki/local_resources.go` |
| `/metrics` | 必须配置独立 `METRICS_AUTH_TOKEN`；未配置/空白返回 503，错误或缺失 Bearer 返回 401 | `cmd/server/main.go`、`README.md` |
| 认证与主体绑定 | 支持静态 Token、多租户、远程验证、IP 白名单；可信主体与请求 `userId` 冲突会拒绝；有状态 SSE/legacy HTTP 会话绑定建连凭据 | `internal/auth/`、`cmd/server/main.go` |
| 安全漏洞清单 | `docs/security-vulnerability-analysis.md` 中 High/Medium/Low/Info 项均已标记闭合 | `docs/security-vulnerability-analysis.md` |
| 可观测性 | 工具调用、缓存、上游请求耗时/重试、熔断状态、Wiki 索引指标已实现；日志包含 PII 脱敏 | `internal/metrics/`、`internal/logger/`、`internal/pii/` |

## 3. 本次已同步修正的文档

| 文件 | 修正内容 |
|---|---|
| `docs/PROJECT_SUMMARY.md` | 更新 `/metrics` 鉴权行为、工具总数 10→14、补充 3 个文件工具、修正 Wiki Upsert 后端描述、明确 Resources 订阅尚未实现、补充指标范围。 |
| `docs/OPTIMIZATION_ANALYSIS.md` | 将 `/metrics` 从“可选鉴权/未配置兼容”修正为“必须配置独立 Token；未配置返回 503”，并在优先级表中标记已完成。 |
| `docs/SESSION_SUMMARY_2026-09-15.md` | 修正 `/metrics` 进展描述，避免继续表述为未配置时兼容免认证。 |
| `docs/release-readiness-audit-20260916.md` | 将“`/metrics` 默认可免认证访问”改为“必须配置独立 Token”，保留运维端点网络隔离建议。 |
| `docs/next-phase-design-2026-09-24.md` | 修正部署指南段落中 `/metrics` 的当前行为。 |
| `docs/project-analysis-260902.md` | 工具清单 11→14，补充本地文件域工具和纯文件工具启动边界。 |
| `docs/wiki-resources-capability-design-260901.md` | 状态由“待评审”改为 Phase 1–2 已实现、Phase 3 订阅未实现；验收标准中的工具数量同步为 14。 |
| `docs/superpowers/specs/2026-09-17-mcp-session-auth-binding-design.md` | 状态由“待实施”改为已实施并通过发布前复审。 |

## 4. 各 Markdown 文件核查结论

| 文件 | 类型 | 当前结论 |
|---|---|---|
| `docs/260714.md` | 历史项目分析 | 反映 2026-07-14 时点，仅 6 个早期工具；作为历史归档保留，不作为当前能力说明。 |
| `docs/260728.md` | 历史改动总结 | UserID 透传与学生/员工/RAG 调整已落地；未覆盖后续 Wiki/File/Resources，归档保留。 |
| `docs/CODE_AUDIT.md` | 历史代码审计 | 列出的 URL 转义、Body 上限、远程认证配置、RAG 参数等主要问题在后续报告中已闭合或过时；保留审计轨迹。 |
| `docs/CODE_REVIEW.md` | 历史代码审查 | `userId` 审计、SSE 注入、缓存隔离等问题后续已修复；保留原始审查记录。 |
| `docs/FEATURE_ROADMAP.md` | 早期路线图 | Prometheus、熔断器、限流等多项已实现；仍可作为业务扩展想法池，但不代表当前缺口。 |
| `docs/OPTIMIZATION_ANALYSIS.md` | 优化清单 | 已同步 `/metrics` 状态；剩余有效优化见第 5 节。 |
| `docs/PROJECT_ANALYSIS.md` | 当前分析报告 | 与当前源码总体一致：14 工具、Resources 订阅未实现、`/metrics` 503 行为正确。 |
| `docs/PROJECT_SUMMARY.md` | 当前项目总览 | 已更新核心过时点；可作为当前摘要继续维护。 |
| `docs/SESSION_SUMMARY_2026-09-15.md` | 会话总结 | 已修正 `/metrics` 行为；其余作为阶段总结保留。 |
| `docs/deployment-guide.md` | 部署指南 | 与当前配置边界一致，尤其是纯文件工具、Local Wiki、`/metrics` Token 要求。 |
| `docs/next-phase-design-2026-09-24.md` | 下一阶段方案 | P0/P1/P2 事项已落地并记录；已修正 `/metrics` 说明。 |
| `docs/project-analysis-260902.md` | 项目分析 | 已补充文件工具并同步工具数量；Resources 订阅待实施表述仍正确。 |
| `docs/project-analysis-report.md` | 全面分析报告 | 与当前状态基本一致；待优化建议仍有效，包括探针网络保护、文件共享目录边界、Resources 订阅。 |
| `docs/r260813.md` | 历史交付报告 | 早期 Student/RAG/Staff 状态，归档保留。 |
| `docs/r260818.md` | 历史演进报告 | Wiki HTTP/Local 与 11 工具阶段记录，归档保留；后续文件工具未纳入属时间差。 |
| `docs/r260829.md` | 历史检查总结 | P0/P1 多数已闭合；“其他待优化”中部分仍可参考。 |
| `docs/release-readiness-audit-20260916.md` | 发布前审查 | 已修正 `/metrics` 行为；本地 Wiki 配置注入、会话粘性等风险仍有效。 |
| `docs/release-validation-2026-09-24.md` | 发布验证记录 | 与 CI/发布构建状态一致，目标环境验收待执行仍有效。 |
| `docs/security-vulnerability-analysis.md` | 安全审计 | 当前状态为全量闭合；无须修改。 |
| `docs/session-summary-20260919.md` | 会话总结 | 记录 CI/发布准备上下文，归档保留。 |
| `docs/v260818.md` | MCP 2026-07-28 升级分析 | 协议兼容改造已落地，作为升级记录保留。 |
| `docs/wiki-progress-report-260901.md` | Wiki 进度报告 | Phase 1–2 Resources 与 VS Code 联调状态基本一致；Claude Desktop/Cursor 未验证仍有效。 |
| `docs/wiki-resources-capability-design-260901.md` | Wiki Resources 设计 | 已更新为 Phase 1–2 已实现、Phase 3 订阅未实现。 |
| `docs/wiki-resources-client-compatibility-260901.md` | 客户端兼容性 | VS Code 已验证；Claude Desktop/Cursor 未验证仍是待补项。 |
| `docs/wiki-search-session-summary.md` | Wiki 搜索总结 | GSE、候选桶、真实语料测试记录有效；并行全量测试超时属当时环境记录。 |
| `docs/wiki-session-summary-20260913.md` | Wiki 性能总结 | 419 篇语料性能与 zh/zh_s 对比有效；后续推送事项为流程建议。 |
| `docs/wiki-url-fix.md` | Wiki URL 方案片段 | 已由 ResourceLink/link_base_url 方向吸收；建议后续补标题或归档说明。 |
| `docs/superpowers/plans/2026-08-31-auth-boundary-hardening.md` | 实施计划 | 认证边界加固已实施，计划作为可追溯记录保留。 |
| `docs/superpowers/plans/2026-08-31-auth-boundary-hardening.zh-CN.md` | 实施计划 | 同上；含未勾选步骤但实际代码与后续审计显示已闭合。 |
| `docs/superpowers/plans/2026-09-01-wiki-backlinks-index.md` | 实施计划 | Backlinks 候选桶/索引优化已实现，保留计划。 |
| `docs/superpowers/plans/2026-09-01-wiki-resources-phase-1-2.md` | 实施计划 | Phase 1–2 已实现；订阅明确不在本计划范围。 |
| `docs/superpowers/plans/2026-09-17-mcp-session-auth-binding.md` | 实施计划 | 会话认证绑定已实现并通过复审，保留计划。 |
| `docs/superpowers/specs/2026-08-30-auth-boundary-hardening-design.md` | 设计规格 | 对应功能已实现，保留设计依据。 |
| `docs/superpowers/specs/2026-08-30-auth-boundary-hardening-design.zh-CN.md` | 设计规格 | 同上。 |
| `docs/superpowers/specs/2026-09-01-circuit-breaker-config-design.md` | 设计规格 | 熔断配置化与指标已实现，保留设计依据。 |
| `docs/superpowers/specs/2026-09-01-wiki-backlinks-index-design.md` | 设计规格 | Backlinks 索引设计已实现，保留设计依据。 |
| `docs/superpowers/specs/2026-09-17-mcp-session-auth-binding-design.md` | 设计规格 | 已更新状态为已实施。 |

## 5. 仍建议推进的优化点

| 优先级 | 优化点 | 当前判断 |
|---|---|---|
| P1 | `tools/list` 按租户 `allowed_tools` 过滤 | 当前 `tools/call` 已在认证层约束，列表仍可能展示租户不可调用工具；属于 Agent 体验与纵深防御优化。 |
| P1 | Wiki `update/append` 乐观锁 / `if_match` | 单实例写入已串行，`create` 冲突已拒绝；多人协作编辑同页仍可能 last-write-wins。 |
| P1 | 目标环境发布验收 | CI 与本地发布门禁已通过，但目标环境配置、探针、会话粘性、Wiki 配置注入仍需按部署指南验收。 |
| P2 | Wiki Resources 订阅通知 | Phase 1–2 只读能力已实现；仅在目标客户端明确消费订阅时实施 Phase 3。 |
| P2 | 客户端兼容性矩阵补齐 | VS Code 已验证；Claude Desktop、Cursor 仍缺具名版本实测。 |
| P2 | 缓存容量/淘汰观测与是否拆分缓存 | 已有共享 LRU 与命中指标；是否拆分 Student/RAG/Wiki 缓存仍需生产数据支持。 |
| P2 | 搜索相关性评估 | IDF/BM25/业务词典扩展需真实查询集和相关性指标，避免无基线改动影响排序。 |
| P2 | 深度健康/诊断端点 | `/health`/`ready` 当前语义清晰；如需要上游/Wiki 持续健康，应新增诊断或扩展 readiness 策略，避免探针误触发重启。 |
| P3 | 文档治理 | 已建立 `docs/archive/` 归档索引与已完成功能归档；后续仍建议在历史文档顶部统一加“历史归档，不代表当前状态”提示，降低误读风险。 |

## 6. 结论

当前源码实现与主要当前文档（README、部署指南、项目分析、安全审计、发布验证）已基本一致。主要过时点集中在历史报告和个别总览文档对 `/metrics`、工具数量、Resources 订阅状态的描述；本次已同步修正当前仍会被读者引用的文件，并新增 `docs/archive/completed-features-2026-09-29.md` 作为已完成功能归档。后续工作不建议继续扩大文档修改范围，应优先围绕租户工具列表可见性、协作编辑冲突控制、目标环境验收与客户端兼容性补测推进。
