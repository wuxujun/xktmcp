## xktmcp

基于MCP协议的学生相关数据接口服务。

### 运行

```bash
# 运行
go run ./cmd/server/main.go -transport=http -port=8081

# 查看日志
go run ./cmd/server/main.go -transport=http -port=8081 -debug
```

请求与响应内容日志默认关闭，避免把敏感业务数据直接写入日志。可通过环境变量开启：

```bash
LOG_HTTP_PAYLOADS=true LOG_HTTP_PAYLOAD_MAX_BYTES=1048576 go run ./cmd/server/main.go -transport=http -port=8081
```

- `LOG_HTTP_PAYLOADS`：是否记录所有 HTTP 请求 Body 与响应结果，默认 `false`。
- `LOG_HTTP_PAYLOAD_MAX_BYTES`：单个请求或响应最多记录的字节数，默认 1 MiB；设为 `0` 表示完整记录且不截断。
- 对应命令行参数为 `-log-http-payloads` 与 `-log-http-payload-max-bytes`，命令行参数优先。
- 开启后使用 `category=http`、`direction=request|response`、`request_body`、`response_body` 等结构化字段。生产环境仅应在受控排障期间开启。
- 请求元信息日志始终包含安全化的 `request_headers`，并单独提供 `mcp_protocol_version`、`mcp_session_id`、`mcp_method`；认证、Cookie 和 API Key 类 Header 仅记录为 `[REDACTED]`。

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

### Authentication configuration (English)

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

默认读取 `config/wiki.json`，也可以通过 `-wiki-config=/path/to/wiki.json` 指定。配置文件不存在时默认使用 HTTP。Local 模式下，读取覆盖所有 `content_dirs`；新建、覆盖和追加只允许写入 `write_dir`（默认 `wiki/topics`），每次成功写入都会追加根目录 `log.md` 并立即刷新本地索引。反向链接同时识别相对 Markdown 链接和 `[[wiki link]]`。

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
