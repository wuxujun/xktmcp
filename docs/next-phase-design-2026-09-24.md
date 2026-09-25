# xktmcp 下一阶段规划与设计方案

状态：P0 已完成；P1 文档已交付、目标环境验收待执行；P2 发布代码 CI 已通过 · 日期：2026-09-24 · 基线提交：`b37d09a`

本方案依据当前[项目分析报告](../PROJECT_ANALYSIS.md)、[发布前审查](release-readiness-audit-20260916.md)、`internal/server/register.go` 和现有 CI 配置制定。P0 的设计与验收结果、P1 的文档交付及 P2 的发布验证保留在本文；目标环境验收尚未完成。前一份路线图中的熔断器、Prometheus 指标和工具前缀通配符现已存在，不再列为待开发能力。

## 目标与优先级

| 顺序 | 工作项 | 目标结果 | 交付物 |
| --- | --- | --- | --- |
| P0 | 纯文件工具组合独立启动 | 只启用任意多个文件工具时，不依赖上游 Token 或 Wiki 配置 | 注册逻辑、小范围测试、README 更新 |
| P1 | 可复现的部署说明 | 运维能按传输方式和工具组合配置凭据、目录与网络入口 | `docs` 部署指南与发布检查清单 |
| P2 | 发布验证对齐 | 现有 CI 构建参数与实际发布命令一致，并验证产物可用 | CI 小幅调整、一次发布门禁记录 |

不在本阶段改变 MCP 工具名称或请求/响应格式，不增加新业务工具、存储后端或认证方式，也不改变现有认证默认值。Wiki Resources 订阅、向量检索、Redis 等方向需要单独的需求和收益数据再设计。

## P0：纯文件工具组合的依赖隔离

### 实施前现状与原因

`RegisterAll` 先注册文件工具，仅在 `MCP_ENABLED_TOOLS` 解析后恰好只有一个文件工具时提前返回。`file_*` 会展开为三个工具，因此无法走该路径；后续加载默认 HTTP Wiki 配置，并通过 `LoadConfigFromEnv` 要求 `API_TOKEN`。纯文件工具组合因此被无关的上游配置阻断。单个文件工具已能独立启动，表明所需依赖隔离边界已经存在。

### 实施方案与结果

在 `internal/server/register.go` 中，把“已启用文件工具且集合长度为 1”的判断改为“白名单非空，且其中每个工具均属于文件工具”。判断应基于 `parseEnabledTools` 展开后的集合，因此 `file_*`、逗号列表和重复项具有相同结果。流程保持为：解析白名单 → 按现有逻辑注册并验证文件工具 → 若只包含文件工具则返回 → 否则继续装配熔断器、Wiki 与上游工具。

建议使用包内私有辅助函数表达该条件，不改变 `knownToolNames`、工具注册函数或环境变量格式。白名单未设置时返回“否”，保持默认全量注册和 `API_TOKEN` 要求。包含任一学生、教职工、RAG 或 Wiki 工具时返回“否”；混合场景继续遵循原有 Wiki 模式和上游依赖规则。错误的 `FILE_SEARCH_ROOT` 仍在提前返回之前报错。纯文件模式继续使用网络传输原有的 MCP 认证要求；本项仅移除无关的上游配置依赖。

| 配置场景 | 预期 |
| --- | --- |
| `file_search`、单个元数据工具，`API_TOKEN` 为空 | 注册成功；保持当前行为 |
| `file_search,file_get_info` 或 `file_*`，`API_TOKEN` 为空 | 注册成功，只暴露所选文件工具 |
| 文件工具与学生/RAG 工具混用，`API_TOKEN` 为空 | 保持启动失败 |
| 未设置 `MCP_ENABLED_TOOLS`，`API_TOKEN` 为空 | 保持启动失败 |
| 文件工具与本地 Wiki 工具混用，Wiki 配置为 `local` | 保持本地 Wiki 规则，不额外要求上游 Token |

已在 `internal/server/file_search_test.go` 加入组合、`file_*`、混合工具和目录错误测试，并更新 README 与项目分析报告。验证通过：`go test ./internal/server -run 'TestRegisterAll|TestFileSearch|TestFileTools|TestParseEnabledTools' -count=1`、`go test ./internal/server -count=1`、`go test ./cmd/server -count=1`。

兼容性：只扩大纯文件工具白名单可成功启动的组合；默认全量注册、混合上游工具、认证和外部工具协议均不变。若回退该改动，受影响的仅是新增可用的纯文件组合。

## P1：部署配置与操作手册

### 实施前现状

版本库只提供 `config/wiki.example.json`；本地 Wiki 的实际 `config/wiki.json` 需要在部署环境注入。配置文件缺失时会回退到 HTTP Wiki。`README.md` 已说明部分安全边界，但缺少把工具白名单、传输认证、目录挂载和探针网络控制组合起来的部署检查步骤。

### 文档设计与交付

已新增 [部署指南](deployment-guide.md)，采用不依赖特定编排平台的配置矩阵：默认 stdio、HTTP/SSE 访问上游、纯文件工具、仅本地 Wiki、混合本地 Wiki 与文件工具。每一项列出必需环境变量、实际注册范围和启动失败条件。配置示例只使用占位符，不写真实 Token、用户目录或生产 URL。

指南明确以下操作边界：

1. `API_TOKEN` 是上游调用凭据；HTTP/SSE 还需独立的 MCP 认证配置。`AUTH_TENANTS` 的可信 `user_id` 可作为本地 Wiki 用户隔离主体，共享 Token、IP 白名单和 stdio 中的 `userId` 不能作为该安全边界。
2. 本地 Wiki 配置需与内容目录一同部署。多租户目录隔离启用 `require_user_mapping=true` 时，映射必须覆盖目标用户；文件工具的 `FILE_SEARCH_ROOT` 是共享可见目录。
3. `/health`、`/ready` 默认免认证；`/metrics` 可使用 `METRICS_AUTH_TOKEN`，否则需由网络层限制访问。`/ready` 仅表示初始化完成，不代表上游持续健康。
4. 有状态 SSE 和旧版 Streamable HTTP 在多实例部署中需要会话粘性；轮换 Bearer Token 后客户端须新建会话。HTTP 请求体与日志内容开关沿用现有上限和默认值。

指南附有发布检查清单：配置文件存在且指向预期目录、工具白名单与权限一致、探针与指标端点处于预期网络范围、日志内容记录关闭或受控、会话路由满足传输方式、回滚时保留前一版配置。验收方式是在隔离环境中按矩阵逐项验证启动结果、`tools/list` 和关键探针；不要求连接真实上游来验证纯本地模式。

兼容性：本阶段只新增文档和示例，不改变配置默认值。部署平台尚未指定，平台专属清单与代理参数应在选定环境后补充。

## P2：发布验证与 CI 对齐

### 实施前现状

`.github/workflows/ci.yml` 已运行 `go vet`、漏洞扫描、构建、带竞态检测的测试、lint 和 Linux amd64 单文件发布构建。因此无需再创建一套重复门禁。当前 CI 发布构建未带仓库指南和既有发布审查使用的 `-tags=jsoniter`；这会使 CI 产物与预期发布命令不完全一致。此前审查文档提出“接入 CI”属于历史建议，不代表当前缺失。

### 设计、实施与验收

已核对 `AGENTS.md`、README 和既有发布审查，三者均以 `jsoniter` 为发布构建约定。现有 `build-release` 步骤已补齐该 tag，保留 `-trimpath`、静态 Linux 构建和版本注入；测试和 lint 步骤未改动。构建后新增使用实际产物进行 MCP 初始化和 `tools/list` 的 stdio 冒烟步骤。

本地构建和原生产物冒烟已通过；发布代码提交 `ce438d2` 的 [CI 运行](https://github.com/wuxujun/xktmcp/actions/runs/36130605383)也已通过，Linux 产物完成 MCP 初始化及 `tools/list` 冒烟。具体证据见[发布构建验证记录](release-validation-2026-09-24.md)。真实上游联调仍应使用显式凭据与独立环境，不放入普通 PR 门禁。回滚时使用上一版已验证产物和对应配置。

## 执行顺序与完成标准

P0 已完成，P1 指南与检查清单已交付，P2 的构建与发布代码 CI 已通过。目标环境的启动、`tools/list` 和探针验收仍需在部署时完成。各阶段分别评审，避免部署文档和 CI 改动混在一个变更中。本阶段的代码与 CI 条件已满足；部署条件以目标环境验收为准。
