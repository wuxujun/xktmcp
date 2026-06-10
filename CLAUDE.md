# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`xktmcp` is a Go-based MCP (Model Context Protocol) server that exposes student-related data APIs and an enterprise RAG knowledge-base search to LLM clients. It wraps an upstream HTTP backend (default `https://yk.xkt.com`, configurable via `BASE_URL`) and exposes five MCP tools: `student_search`, `student_order`, `student_exam`, `student_get`, and `rag_search`.

Module path: `github.com/wuxujun/xktmcp` (Go 1.25).

## Commands

```bash
# Run (stdio — default, no auth)
go run ./cmd/server/main.go

# Run as HTTP (Streamable HTTP at /mcp) or SSE (at /sse, /messages/)
go run ./cmd/server/main.go -transport=http -port=8081
go run ./cmd/server/main.go -transport=sse  -port=8081

# Build a Linux release binary (using -trimpath to remove compile-time absolute filesystem paths)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags="-s -w" -o mcp-server ./cmd/server/main.go

# Tests
go test ./...
go test ./internal/auth -run TestName -v   # single test
```

`.env` is auto-loaded by `godotenv`. Required/optional environment:

- `API_TOKEN` (**required**) — Bearer token sent to the upstream API. Server refuses to start without it (fail-closed).
- `BASE_URL` (default `https://yk.xkt.com`), `TIMEOUT_SECONDS` (default 10).
- `AUTH_TOKEN` or `-auth-token` flag — local Bearer required by **http/sse** transports. stdio is unauthenticated by design.
- `AUTH_REMOTE_VERIFY_URL` + `AUTH_REMOTE_ALLOWED_HOSTS` — optional remote token verification fallback. The verify URL's host must appear in the comma-separated allowlist or the fallback is disabled (SSRF guard).
- `AUTH_IP_ALLOWLIST` — optional comma-separated CIDR allowlist (e.g. `160.79.104.0/21`). Requests whose source IP falls in any listed network are **allowed without a Bearer token** (IP passes ⇒ Authorization is ignored). Invalid CIDR ⇒ fail-closed (server refuses to start).
- `AUTH_TRUST_FORWARDED_HEADER` (default `false`) — controls the **source IP used for the `AUTH_IP_ALLOWLIST` decision**. When `false` (default, safe) only the TCP `RemoteAddr` is trusted, so spoofed `X-Forwarded-For`/`X-Real-IP` headers can't bypass auth. Set `true` **only** when behind a trusted reverse proxy that rewrites those headers; otherwise any client could forge its source IP. (Note: the `ClientIP` used for log auditing still reads forwarded headers regardless — the security path is separate.)
- `http`/`sse` transports refuse to start unless **at least one** of `AUTH_TOKEN`/`AUTH_REMOTE_VERIFY_URL`/`AUTH_IP_ALLOWLIST` is configured.
- `RAG_SEMANTIC_REWRITE` (default `true`) — when `rag_search` is called with `rewrite: true`, attempt LLM query rewriting via MCP sampling. Set to `false`/`0`/`no`/`off` to always use the local rule-based rewriter (e.g. when the connected client — such as n8n — doesn't implement `sampling/createMessage`). Read lazily on first call (after `.env` load), not at package init.

Health probe: `GET /health` (unauthenticated) on http/sse transports.

Metrics: `GET /metrics` (unauthenticated, Prometheus text format) on http/sse transports. Exposes `xkt_tool_calls_total{tool,status}` and `xkt_tool_duration_seconds{tool}` plus default `go_*`/`process_*` collectors. Every tool call is instrumented centrally in `register.go`'s `addTool` wrapper (also emits a per-call summary log with `trace_id`, `status`, `latency`). Protect via network isolation/reverse proxy if needed — it's open like `/health`.

Observability: each tool call gets a request-level `trace_id` (reused from n8n's `toolCallId`/`sessionId` when present, else generated). It's propagated via `context` and auto-attached to logs emitted through the logger's `*Ctx` helpers (`InfofCtx`/`ToolfCtx`/`APIfCtx`/`ErrorfCtx`), so a single call's tool → upstream-API → cache logs all share one `trace_id`.

PII audit & redaction (`internal/pii`): the `addTool` wrapper writes a structured **audit log** per call (`category:"audit"`) recording `querier` (the n8n userId), `tool`, masked `subject` (the queried name/id/phone), `status`, `latency_ms`, `trace_id` — answering "who queried whom". Redaction has two scopes:

- **Responses** (LLM-facing): tool handlers run `pii.RedactJSON` over results, masking phone (`1[3-9]\d{9}`) and ID-card (15/18-digit) patterns while **keeping** `id`/`smp_id` identifiers and names (the query chain `student_search → student_order` and answer quality depend on them). Cached results are stored already-redacted.
- **Logs/audit**: handler entry logs no longer dump `%+v` of args (which leaked the raw query); they log `querier` + `pii.MaskSubject(query)`. `MaskSubject` masks phone/idcard, and partial-masks bare identifiers/names (rune-safe). Regex-based redaction may over-mask non-PII numbers that happen to be 11/15/18 digits — a deliberate safety bias.

## Architecture

Layered, dependency-injected from `cmd/server/main.go` → `internal/server/register.go`:

```
cmd/server/main.go         transport selection (stdio | sse | http), auth wiring, lumberjack log rotation, graceful shutdown
└── internal/server        RegisterAll: builds Config from env → StudentAPI/RagAPI → *Service → registers MCP tools
    ├── internal/client    HTTP clients for upstream API (student_client.go, rag_client.go); shared retry+error helpers in client.go
    ├── internal/service   Thin validation/orchestration layer over clients (e.g. trim+empty checks)
    ├── internal/tools     MCP tool definitions, JSON schemas, handlers; talks only to *Service
    ├── internal/model     Upstream response DTOs
    ├── internal/auth      Bearer middleware for http/sse (see below)
    ├── internal/metrics   Prometheus collectors + /metrics handler (per-tool calls/errors/latency)
    ├── internal/trace     Request-level trace id (ctx propagation; reuses n8n toolCallId/sessionId)
    ├── internal/pii       PII redaction (phone/idcard masking) + identifier partial-masking
    └── internal/logger    slog wrappers; *Ctx variants auto-inject trace_id from context
```

When adding a new tool, follow the existing flow: define args struct embedding `CommonArgs` → `*Tool()` returning `mcp.Tool` with `publicSchema[Args](envelopeFields)` → `*Handler(svc)` closure → register in `internal/server/register.go`.

### Schema sanitization (important, non-obvious)

`internal/tools/schema.go` exists because the upstream orchestrator (n8n) injects envelope fields (`sessionId`, `action`, `chatInput`, `toolCallId`, `userId`) into every tool call. `publicSchema[T](envelopeFields)`:

1. Removes those fields from the **public** schema shown to the LLM (so the model doesn't try to fill them).
2. Keeps them on the Go struct so they still deserialize (e.g. `rag_search` reads `userId` from `CommonArgs`).
3. Sets `AdditionalProperties = nil` to disable go-sdk's default `additionalProperties: false`, otherwise n8n's extra fields would fail schema validation.

Do not add envelope fields to a tool's user-facing description and do not remove them from `CommonArgs`.

### Auth (`internal/auth/auth.go`)

Network transports require Bearer tokens via the `Authorization` header only (no `?token=` query param — keeps tokens out of logs/proxies). Local comparison uses `crypto/subtle.ConstantTimeCompare`. Optional remote verification path has: positive/negative result caching, host allowlist (SSRF defense), and a token-bucket rate limiter. Tokens are masked (`xx…yy`) in all logs.

The middleware checks an **IP allowlist first** (`AUTH_IP_ALLOWLIST`): a request whose source IP is in a trusted CIDR is allowed without any token. The IP used for this decision comes from `securityClientIP`, which defaults to the real TCP `RemoteAddr` and only honors `X-Forwarded-For`/`X-Real-IP` when `AUTH_TRUST_FORWARDED_HEADER=true`. This is deliberately separate from the log-only `ClientIP` (which always reads forwarded headers) — forwarded headers are spoofable, so they must not drive a security decision unless a trusted proxy is in front.

### HTTP server timeouts (intentional)

`runServer` sets only `ReadHeaderTimeout` (Slowloris defense) and `IdleTimeout`. **No `ReadTimeout`/`WriteTimeout`** — SSE and Streamable HTTP are long-lived streams and would be cut off mid-flight. Don't "fix" this by adding them.

### RAG query rewriting

`rag_search` with `rewrite: true` calls back into the MCP **sampling** API (`session.CreateMessage`) to ask the connected LLM to rewrite the query into retrieval-friendly form. On any failure it falls back to a local string-replace rewriter (`rewriteQuery`). Both paths are kept; do not delete the local fallback.

### Logging

Init happens once in `main` with `io.MultiWriter(os.Stderr, lumberjack)` writing to `server.log` (100MB × 7 backups × 7 days, gzip, with a background goroutine for daily midnight rotation using local timezone). The system uses the Go 1.21+ standard `log/slog` package to write structured JSON logs. Use the `logger.Infof / Errorf / Toolf / APIf` helpers rather than `log.Printf` directly — these helpers wrap `slog` with correct caller depth lookup so the exact calling file name and line number are preserved, and standard Go library logs are automatically redirected to JSON output.
