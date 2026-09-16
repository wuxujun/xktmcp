## xktmcp

基于 MCP 协议的学生、教职工、RAG、Wiki 与本地文件搜索服务，支持 stdio、SSE 和 Streamable HTTP。

### 运行

```bash
# 运行
go run ./cmd/server/main.go -transport=http -port=8081

# 开启受控 HTTP 请求/响应内容日志（默认关闭）
go run ./cmd/server/main.go -transport=http -port=8081 -log-http-payloads
```

请求与响应内容日志默认关闭，避免把敏感业务数据直接写入日志。可通过环境变量开启：

```bash
LOG_HTTP_PAYLOADS=true LOG_HTTP_PAYLOAD_MAX_BYTES=1048576 go run ./cmd/server/main.go -transport=http -port=8081
```

HTTP/SSE 模式提供免认证运维探针：`/health` 为存活检查，进程可响应时返回 `200`；`/ready` 为就绪检查，工具与认证器初始化完成时返回 `200`，否则返回 `503`。

- `LOG_HTTP_PAYLOADS`：是否记录所有 HTTP 请求 Body 与响应结果，默认 `false`。
- `LOG_HTTP_PAYLOAD_MAX_BYTES`：单个请求或响应最多记录的字节数，默认 1 MiB；设为 `0` 表示完整记录且不截断。
- 对应命令行参数为 `-log-http-payloads` 与 `-log-http-payload-max-bytes`，命令行参数优先。
- 开启后使用 `category=http`、`direction=request|response`、`request_body`、`response_body` 等结构化字段。生产环境仅应在受控排障期间开启。
- 请求元信息日志始终包含安全化的 `request_headers`，并单独提供 `mcp_protocol_version`、`mcp_session_id`、`mcp_method`；认证、Cookie 和 API Key 类 Header 仅记录为 `[REDACTED]`。

### 独立本地文件搜索

`file_search` 搜索服务器本地目录，使用独立的文件服务。通过 `FILE_SEARCH_ROOT` 指定搜索根目录；未配置时不注册此工具，已有工具的默认行为保持不变。

只运行文件搜索服务：

```bash
FILE_SEARCH_ROOT=/srv/searchable-files MCP_ENABLED_TOOLS=file_search go run ./cmd/server/main.go
```

此模式不需要 `API_TOKEN`、`BASE_URL` 或 Wiki 配置。与其他工具一起运行时，设置 `FILE_SEARCH_ROOT` 并将文件工具加入现有 `MCP_ENABLED_TOOLS` 列表；未设置工具列表时会随现有工具一起注册。显式启用但未配置目录、或目录无法打开时，启动会报错。

调用示例：

```json
{
  "name": "file_search",
  "arguments": {"query": "部署方案", "search_in": "all", "limit": 20}
}
```

- `query`：必填，不超过 256 个字符，按不区分大小写的连续文本匹配，支持中文。
- `search_in`：`all`（默认，标题和正文）、`title`（标题或文件名）、`content`（正文）。Markdown 标题取首个代码块外的一级标题，未找到时使用文件名。
- `limit`：默认 20，最大 100。综合搜索时标题命中优先，同类结果按相对路径排序。
- 返回 `items` 数组，每项包含 `path`（相对根目录的路径）、`title`、`snippet`（命中位置附近最多 240 字符及省略号）、`matched_fields`、`size_bytes`。无结果时返回空数组。
- 普通文件支持文件名搜索；正文读取支持 `.md`、`.markdown`、`.txt`、`.text`、`.csv`、`.json`、`.yaml`、`.yml`、`.xml`、`.html`、`.htm` 的 UTF-8 文本。PDF、Word、Excel 等格式仅支持文件名搜索。
- 递归搜索根目录，跳过隐藏文件及目录、符号链接、非普通文件、超过 2 MiB 的文件；文本文件包含 NUL 字节或无效 UTF-8 时跳过。正文搜索包含文件的原始文本，不解析 HTML 标签等格式。
- 每次调用重新扫描，无索引和结果缓存，文件变更在下一次查询可见。扫描成本随目录大小增长，读取错误会使查询失败；不返回部分成功结果。

此外还提供两个细分工具：`get_file_info`（文件大小、修改时间、类型等元数据）和 `read_file_preview`（`start_line`/`end_line` 行区间预览，最多 200 行）。它们与 `file_search` 共用同一 `FILE_SEARCH_ROOT` 和路径安全边界；文件名和正文检索统一使用 `file_search` 的 `search_in` 参数。

**访问范围**：根目录由服务器管理员配置，调用参数不能指定或扩大目录范围。该目录是共享搜索目录，对所有获准调用 `file_search` 的用户可见，不按 `userId` 隔离。仅将需要共享的文件放入该目录，并通过现有认证及 `allowed_tools` 控制调用权限；文本与结构化结果沿用手机号、身份证号脱敏。

### 认证配置

通过 `AUTH_TENANTS` 配置多租户 Bearer Token。推荐保存 Token 的 SHA-256 十六进制摘要，而不是明文 Token：

```json
[
  {
    "name": "wiki-user-a",
    "token_hash": "<64-character-sha256-hex>",
    "user_id": "user-a",
    "allowed_tools": ["wiki_search", "wiki_get_page"],
    "rate_rps": 5,
    "rate_burst": 10
  }
]
```

`user_id` 是可选的租户可信主体；配置后，它会成为该租户经过认证的用户身份。远程 Token 验证响应中的 `userid` 同样是可信主体。存在可信主体时，请求中的 `userId` 必须与之匹配（缺失时会注入可信主体）；冲突会返回 HTTP 403。

共享 `AUTH_TOKEN`、IP 白名单和 stdio 模式中的 `userId` 仅是路由元数据，并不代表经过认证的用户身份。不要把这类 `userId` 用作授权或安全边界。

HTTP MCP POST 请求体最大为 4 MiB；超过该限制会返回 HTTP 413。远程 Token 验证缓存通过 `AUTH_REMOTE_CACHE_MAX_ENTRIES` 配置，默认最多 4096 条；该值必须为正整数。

可通过 `MCP_ENABLED_TOOLS` 使用逗号分隔的工具白名单限制注册范围；未设置时注册全部工具，未知工具名会导致启动失败。

熔断策略可通过 `UPSTREAM_CB_FAILURE_THRESHOLD`、`UPSTREAM_CB_COOLDOWN_SECONDS`、`UPSTREAM_CB_HALF_OPEN_PROBES` 配置，默认分别为 `5`、`10`、`1`；必须为正整数。

### Authentication configuration (English)

`xktmcp` provides student, staff, RAG, and Wiki MCP tools over stdio, SSE, and Streamable HTTP. The `-debug` flag is not supported; use `-log-http-payloads` or the equivalent environment variables for controlled payload diagnostics.

HTTP/SSE transports expose unauthenticated operational probes: `/health` is the liveness check and returns `200` when the process responds; `/ready` is the readiness check and returns `200` after tools and authentication initialize, otherwise `503`.

Configure tenant-specific Bearer tokens with `AUTH_TENANTS`. Prefer storing a SHA-256 hexadecimal token digest rather than a plaintext token:

```json
[
  {
    "name": "wiki-user-a",
    "token_hash": "<64-character-sha256-hex>",
    "user_id": "user-a",
    "allowed_tools": ["wiki_search", "wiki_get_page"],
    "rate_rps": 5,
    "rate_burst": 10
  }
]
```

The optional tenant `user_id` is a trusted authenticated principal. A `userid` returned by remote token verification is also a trusted principal. When a trusted principal exists, the request `userId` must match it; a missing `userId` is injected from the trusted principal, and a conflict returns HTTP 403.

The `userId` used with a shared `AUTH_TOKEN`, IP allowlist, or stdio transport is routing metadata only, not an authenticated user identity. Do not use it for authorization or as a security boundary.

HTTP MCP POST bodies are limited to 4 MiB; larger bodies receive HTTP 413. Configure the remote-token verification cache with `AUTH_REMOTE_CACHE_MAX_ENTRIES`; it defaults to 4096 entries and must be a positive integer.

Use `MCP_ENABLED_TOOLS` with a comma-separated allowlist to limit which MCP tools are registered. When unset, all tools are registered; an unknown tool name fails startup.

Configure the circuit-breaker policy with `UPSTREAM_CB_FAILURE_THRESHOLD`, `UPSTREAM_CB_COOLDOWN_SECONDS`, and `UPSTREAM_CB_HALF_OPEN_PROBES`. Defaults are `5`, `10`, and `1`; every value must be a positive integer.

Streamable HTTP 会按 MCP 协议版本选择传输方式：`2025-11-25` 及更早版本使用有状态会话并返回 `application/json`，`2026-07-28` 使用无会话模式并返回 `text/event-stream`。客户端请求仍需声明 `Accept: application/json, text/event-stream`；legacy 客户端应通过 `initialize` 协商版本，并在后续请求携带响应中的 `Mcp-Session-Id` 和 `Mcp-Protocol-Version`。

```bash
# 打包 (使用 -trimpath 移除编译时的绝对文件路径)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags="-s -w" -o mcp-server ./cmd/server/main.go
```

### Wiki 搜索后端

全部 Wiki 工具（`wiki_search`、`wiki_get_page`、`wiki_list_tree`、`wiki_upsert_page`、`wiki_get_backlinks`）支持远程 HTTP 和本地 llm-wiki Markdown 两种后端。复制示例配置：

```bash
cp config/wiki.example.json config/wiki.json
```

HTTP 模式继续调用 `BASE_URL/api/ai/wiki/search`：

```json
{
  "mode": "http"
}
```

本地模式搜索 llm-wiki 编译后的文章目录；`root` 相对路径以配置文件目录为基准：

```json
{
  "mode": "local",
  "local": {
    "root": "../.wiki",
    "content_dirs": ["wiki"],
    "write_dir": "wiki/topics",
    "default_category": "topics",
    "refresh_interval_seconds": 30,
    "max_file_size_bytes": 2097152
  }
}
```

默认读取 `config/wiki.json`，也可以通过 `-wiki-config=/path/to/wiki.json` 指定。配置文件不存在时默认使用 HTTP。Local 模式下，读取覆盖所有 `content_dirs`；新建、覆盖和追加只允许写入 `write_dir`（默认 `wiki/topics`），每次成功写入都会追加根目录 `log.md` 并立即刷新本地索引。反向链接同时识别相对 Markdown 链接和 `[[wiki link]]`，并在索引刷新阶段预计算；只有写入成功并完成索引刷新后，新增或变更的反向链接才会对查询可见。

Local 模式的 `local.tokenizer` 默认为 `"builtin"`；设置为 `"gse"` 可启用内置词典的中文搜索分词，使查询和索引按 GSE 词项匹配，同时保留现有的完整标题短语匹配。启用后会增加索引刷新耗时和进程内存占用，未配置时保持原有搜索行为。

`local.gse_dictionary` 仅在 `tokenizer` 为 `"gse"` 时生效，默认为 `"zh"`（简体与繁体）；可显式设置为 `"zh_s"`（仅简体）以降低词典内存占用，但可能改变繁体及部分术语的分词、召回和排序。仅支持这两个内置词典，不接受文件路径。`local.users` 中每个租户可独立配置，未设置时使用 `"zh"`，不继承根配置的词典选择。同类型词典按需加载并在进程内共享；同时使用两种类型会保留两份词典，内存占用可能更高。修改后需重启服务生效，旧版本不识别新增字段，回退时应移除该字段。

Local 模式可以按 `userId` 显式映射不同目录。映射的每个用户拥有独立的搜索索引、目录树、页面、反向链接及写入目录；服务不会把 `userId` 直接拼接成文件路径：

```json
{
  "mode": "local",
  "local": {
    "root": "../.wiki-default",
    "content_dirs": ["wiki"],
    "write_dir": "wiki/topics",
    "require_user_mapping": true,
    "users": {
      "user-a": {
        "root": "../.wiki-user-a",
        "content_dirs": ["wiki"],
        "write_dir": "wiki/topics"
      },
      "user-b": {
        "root": "../.wiki-user-b",
        "content_dirs": ["wiki"],
        "write_dir": "wiki/topics"
      }
    }
  }
}
```

`require_user_mapping=true` 时，缺少 `userId` 或未配置的用户会返回错误，避免意外读取默认库；为 `false`（默认）时，未映射用户继续使用顶层 `local.root`，兼容原有单目录配置。

### Wiki Resources（本地模式）

Wiki Resources 仅支持本地 Markdown 模式，默认关闭。设置 `resources.enabled=true` 后，服务注册两个固定资源（Catalog、Tree）和一个 Page 资源模板；HTTP Wiki 模式或默认禁用配置不会注册这些资源。共享多租户服务只会通过当前调用者对应的 Catalog 暴露页面元数据，不会把其他租户的页面信息放入静态资源或共享目录。

阶段 1–2 尚未实现 Resources 订阅（`subscriptions_enabled`）；该字段必须保持 `false`，订阅通知不会被注册。

设置 `resources.link_base_url` 后，`wiki_search` 的 ResourceLink、Catalog 页面条目和页面 Resource Template 使用 `{link_base_url}/{Base64URL(page_id)}`；`resources/read` 接受该 HTTPS URI，并继续兼容 `wiki://page/{page_key}`。静态 `wiki://catalog` 与 `wiki://tree` 保持不变。该配置必须是无凭据、query 和 fragment 的绝对 HTTPS URL；目标 Wiki 网站负责解码 `page_key` 并执行登录、租户隔离与权限校验。

### Wiki Resources (local mode)

Wiki Resources are available only in local Markdown mode and are disabled by default. Set `resources.enabled=true` to register two fixed resources (Catalog and Tree) plus one Page resource template. HTTP Wiki mode and default-disabled configurations register no Resources. On a shared multi-tenant server, page metadata is exposed only through the caller-specific Catalog; static resources and shared catalogs do not contain another tenant's pages.

Resources subscriptions are not implemented in phases 1–2 (`subscriptions_enabled`); keep this field `false`. No subscription notifications are registered.

When `resources.link_base_url` is set, `wiki_search` ResourceLink values, Catalog page entries, and the page Resource Template use `{link_base_url}/{Base64URL(page_id)}`. `resources/read` accepts that HTTPS URI and remains compatible with `wiki://page/{page_key}`. Static `wiki://catalog` and `wiki://tree` URIs stay unchanged. The configured value must be an absolute HTTPS URL without credentials, query, or fragment. The target Wiki site is responsible for decoding `page_key` and enforcing authentication, tenant isolation, and authorization.
