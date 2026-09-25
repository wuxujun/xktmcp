# xktmcp 部署指南

本文面向部署和验收人员，覆盖当前代码的 stdio、Streamable HTTP、SSE、远程接口、本地 Wiki 与文件工具组合。功能细节见 [README](../README.md)，设计背景见[下一阶段方案](next-phase-design-2026-09-24.md)。示例中的路径、主机和令牌均为占位值，需在目标环境替换；不要把实际凭据写入版本库。本文提供验收步骤，不代表已在目标环境执行。

## 1. 启动前确认

- 默认执行 `go run ./cmd/server/main.go` 使用 stdio；网络模式通过 `-transport=http` 或 `-transport=sse` 选择，默认端口为 `8080`。HTTP MCP 入口是 `/mcp`，SSE 入口是 `/sse` 和 `/messages/`。
- `API_TOKEN` 用于调用上游业务接口，不能代替 MCP 客户端认证。`BASE_URL` 未设置时为 `https://yk.xkt.com`；发布时应明确核对目标上游地址。`TIMEOUT_SECONDS` 可选，必须为正整数。
- HTTP/SSE 必须启用一种 MCP 认证方式，例如 `AUTH_TOKEN`、可用的 `AUTH_TENANTS`、有效的远程验证，或受信任的 IP 白名单。下方命令使用 `AUTH_TOKEN` 作示例。stdio 不要求网络认证。
- `MCP_ENABLED_TOOLS` 为空时注册默认工具集，仍要求 `API_TOKEN`；显式白名单可以使用逗号分隔的名称或 `file_*`、`wiki_*` 等已知前缀。`wiki_*` 包含写入工具 `wiki_upsert_page`，只读部署应使用明确的只读名单。
- 默认 Wiki 配置路径是 `config/wiki.json`，也可用 `-wiki-config=/path/to/wiki.json` 指定。文件不存在时回退到远程 HTTP Wiki；本地模式必须确认配置文件与内容目录已部署，不能仅凭 `MCP_ENABLED_TOOLS=wiki_*` 切换后端。

服务默认使用 `server.log` 写日志，部署账号需对指定 `-logfile` 路径有写权限。网络模式监听 `:<port>`；服务本身使用普通 HTTP，外部访问时应由可信入口提供 TLS 和网络访问控制。

## 2. 配置矩阵

| 运行方式 | 必需配置 | 注册范围与启动检查 |
| --- | --- | --- |
| 默认 stdio，访问上游 | `API_TOKEN`；按需要设置 `BASE_URL` | 默认学生、教职工、RAG、远程 Wiki 工具；若配置 `FILE_SEARCH_ROOT`，还注册文件工具。缺少 `API_TOKEN` 时启动失败。 |
| HTTP 或 SSE，访问上游 | 上述上游配置，加一种 MCP 认证方式 | 工具范围同白名单设置；未配置有效认证器时拒绝启动。HTTP `/mcp`、SSE `/sse` 与 `/messages/`。 |
| 仅文件工具 | `FILE_SEARCH_ROOT`，`MCP_ENABLED_TOOLS='file_*'` 或文件工具子集 | 无需 `API_TOKEN`、`BASE_URL` 或 Wiki 配置；目录缺失、不可打开或不合规时启动失败。网络传输仍需 MCP 认证。 |
| 仅本地 Wiki | 指向 `mode=local` 配置的 `-wiki-config`，只包含 Wiki 工具的白名单，已存在的本地根目录 | 无需 `API_TOKEN`；配置缺失会回退到远程模式，可能因缺少 `API_TOKEN` 启动失败。网络传输仍需 MCP 认证。 |
| 本地 Wiki + 文件工具 | 上述本地 Wiki 配置、`FILE_SEARCH_ROOT`，且白名单只含 Wiki/文件工具 | 无需 `API_TOKEN`；两个目录各自验证。加入学生、教职工或 RAG 工具后仍需上游令牌。 |

纯文件模式会跳过 Wiki 配置加载。其它模式即使白名单不包含 Wiki 工具，注册流程也会读取 Wiki 配置；不要在这些模式下留下损坏的配置文件。

## 3. 启动示例

以下示例从仓库根目录执行；实际发布二进制时，将 `go run ./cmd/server/main.go` 替换为相应可执行文件。令牌占位值仅用于展示参数位置。

```bash
# 默认 stdio，访问指定上游
BASE_URL=https://api.example.com API_TOKEN=replace-with-upstream-token go run ./cmd/server/main.go

# Streamable HTTP，客户端连接 /mcp 并提供 Bearer Token
BASE_URL=https://api.example.com API_TOKEN=replace-with-upstream-token AUTH_TOKEN=replace-with-mcp-token go run ./cmd/server/main.go -transport=http -port=8081

# SSE，客户端连接 /sse
BASE_URL=https://api.example.com API_TOKEN=replace-with-upstream-token AUTH_TOKEN=replace-with-mcp-token go run ./cmd/server/main.go -transport=sse -port=8081

# 纯文件工具；也可将 file_* 换成 file_search,file_get_info 等子集
FILE_SEARCH_ROOT=/srv/shared-files MCP_ENABLED_TOOLS='file_*' go run ./cmd/server/main.go
```

纯文件工具若通过 HTTP 提供，同样需要 MCP 认证：

```bash
FILE_SEARCH_ROOT=/srv/shared-files MCP_ENABLED_TOOLS='file_*' AUTH_TOKEN=replace-with-mcp-token go run ./cmd/server/main.go -transport=http -port=8081
```

本地 Wiki 配置示例保存为部署环境中的 `/etc/xktmcp/wiki.json`；事先创建 `/srv/xktmcp-wiki/wiki`，并确保服务账号可读取。`root` 相对路径会相对配置文件所在目录解析，示例使用绝对路径避免歧义。

```json
{
  "mode": "local",
  "local": {
    "root": "/srv/xktmcp-wiki",
    "content_dirs": ["wiki"],
    "write_dir": "wiki/topics"
  }
}
```

只读 Wiki 和 Wiki + 文件的启动示例：

```bash
MCP_ENABLED_TOOLS='wiki_search,wiki_get_page,wiki_list_tree,wiki_get_backlinks' go run ./cmd/server/main.go -wiki-config=/etc/xktmcp/wiki.json

FILE_SEARCH_ROOT=/srv/shared-files MCP_ENABLED_TOOLS='file_*,wiki_search,wiki_get_page,wiki_list_tree,wiki_get_backlinks' go run ./cmd/server/main.go -wiki-config=/etc/xktmcp/wiki.json
```

这两个示例是 stdio 模式；若改为 HTTP/SSE，仍需配置 MCP 认证。Wiki Resources 默认关闭；需使用时在本地 Wiki 配置中显式启用，并确认租户 `allowed_tools` 与资源读取权限对应。多租户本地 Wiki 应按 [README](../README.md) 配置 `local.users` 与 `require_user_mapping=true`，为每个用户提供独立目录。

## 4. 身份、目录与网络边界

- `AUTH_TENANTS` 的租户 `user_id` 或远程验证响应中的 `userid` 是可信身份，可约束请求 `userId`。共享 `AUTH_TOKEN`、IP 白名单和 stdio 中的 `userId` 只是路由数据；不能据此建立用户目录隔离。租户 `allowed_tools` 还会约束 Wiki Resources；全局 `MCP_ENABLED_TOOLS` 控制注册范围，两者应分别检查。
- 使用远程验证时，`AUTH_REMOTE_VERIFY_URL` 的主机必须列入 `AUTH_REMOTE_ALLOWED_HOSTS`，否则启动失败。使用 `AUTH_IP_ALLOWLIST` 时填写可信 CIDR；只有请求确实经过受控代理并防止客户端伪造转发头时，才启用 `AUTH_TRUST_FORWARDED_HEADER`。
- 本地 Wiki 的 `write_dir` 必须位于允许的 `content_dirs` 内。文件工具的 `FILE_SEARCH_ROOT` 是共享目录，所有获准调用文件工具的用户可见；只挂载可共享的文件。启用 `wiki_upsert_page` 前确认写入目录、备份与权限。
- `/health`、`/ready` 默认免认证，分别反映进程存活与初始化完成；`/ready` 不持续检查上游。`/metrics` 可设置 `METRICS_AUTH_TOKEN` 使用独立 Bearer Token，未设置时无认证。网络入口应限制这些端点的访问范围。
- 有状态 SSE 和旧版 Streamable HTTP 会话需要在多实例入口保持会话粘性；切换 Bearer Token 后客户端应丢弃旧会话并重连。`2026-07-28` Streamable HTTP 为无状态模式。反向代理需允许长连接，同时设置适合部署环境的连接数与请求头读取保护。
- MCP POST 请求体上限为 4 MiB，读取期限为 30 秒。`LOG_HTTP_PAYLOADS` 默认关闭；开启后可能把业务请求与响应内容写入日志，生产环境只应在受控排障期间启用。不要在命令输出、发布记录或日志中打印实际令牌。

## 5. 隔离环境验收

1. 使用占位凭据以外的测试凭据和临时目录，在隔离环境按配置矩阵启动目标模式；确认进程未因缺少配置而退出。纯文件和本地 Wiki 场景不应需要 `API_TOKEN`。
2. 使用经过认证的 MCP 客户端完成 `initialize`、`tools/list`；核对只出现白名单内工具。只读 Wiki 场景不应出现 `wiki_upsert_page`，纯文件场景不应出现上游工具。
3. 网络模式检查 `/health` 和 `/ready` 返回 `200`。若设置 `METRICS_AUTH_TOKEN`，确认未携带指标令牌时 `/metrics` 返回 `401`；同时验证 MCP 请求缺少认证时被拒绝。
4. 验证一个失败配置：纯文件模式缺少 `FILE_SEARCH_ROOT` 应启动失败；本地 Wiki 模式使用无效配置或缺失目录也应失败。配置文件完全缺失会回退到远程模式，须单独核对并避免误连上游。
5. 记录使用的提交号、配置种类、工具清单及探针结果；不记录真实令牌、业务查询或包含个人信息的返回内容。上游模式的实际数据联调应在授权的独立环境完成。

## 6. 发布与回滚检查清单

- [ ] 确认运行方式、`MCP_ENABLED_TOOLS`、实际注册工具和租户 `allowed_tools` 与预期一致。
- [ ] 确认 `API_TOKEN` 与 MCP 认证分别注入，`BASE_URL` 指向预期上游；纯本地模式未意外依赖上游。
- [ ] 确认本地 Wiki 配置文件存在、模式为 `local`，目录已挂载；严格用户映射覆盖目标租户。文件根目录仅含允许共享的内容。
- [ ] 确认网络入口提供 TLS、访问控制和适当连接限制；`/health`、`/ready`、`/metrics` 的可见范围符合部署要求。
- [ ] 确认有状态会话的路由粘性、Token 轮换后的重连流程，以及 POST 体上限与长连接代理设置。
- [ ] 确认请求/响应内容日志按预期关闭或受控，日志路径可写且不暴露凭据。
- [ ] 记录同一提交的测试、CI 与产物验证结果，并保留前一版可用二进制及对应配置用于回滚。

回滚时恢复前一版二进制及与其匹配的配置，再执行本节的工具清单和探针检查。若回滚到尚不支持多个纯文件工具独立启动的版本，应临时使用单个文件工具白名单或按该版本要求提供上游配置；有状态客户端需要重新建立会话。
