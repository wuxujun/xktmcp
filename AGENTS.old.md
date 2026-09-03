# Repository Guidelines

## Project Structure & Module Organization

This repository is a Go MCP server module (`github.com/wuxujun/xktmcp`). The entry point is `cmd/server/main.go`, which wires transport, authentication, logging, and graceful shutdown. Core code lives under `internal/`: `client` contains upstream HTTP clients and retry helpers, `service` handles validation/orchestration, `tools` defines MCP tools and schemas, `model` stores DTOs, and support packages include `auth`, `logger`, `metrics`, `pii`, and `trace`. Tests are colocated with their packages as `*_test.go`.

## Build, Test, and Development Commands

- `go run ./cmd/server/main.go` starts the server with the default stdio transport.
- `go run ./cmd/server/main.go -transport=http -port=8081` runs Streamable HTTP on `/mcp`; add `-debug` for verbose logging.
- `go run ./cmd/server/main.go -transport=sse -port=8081` runs SSE endpoints.
- `go test ./...` runs the full test suite.
- `go test ./internal/auth -run TestName -v` runs one package/test while debugging.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags="-s -w" -o mcp-server ./cmd/server/main.go` builds the Linux release binary.

## Coding Style & Naming Conventions

Use standard Go formatting (`gofmt`) and idiomatic package-level organization. Keep package names short and lowercase. Export only API needed across packages; prefer unexported helpers inside each `internal` package. Follow existing naming patterns: `StudentAPI`, `RagAPI`, `*Service`, `*Tool`, and `*Handler`. Use structured logger helpers from `internal/logger` instead of direct `log.Printf`.

## Testing Guidelines

Use Go's standard `testing` package. Place tests beside the code under test and name files `*_test.go`; use descriptive test names such as `TestBearerMiddlewareRejectsMissingToken`. Add focused tests for auth, schema behavior, redaction, caching, metrics, and client error handling when those areas change. Run `go test ./...` before opening a PR.

## Commit & Pull Request Guidelines

Recent commits use short numbered summaries, often in Chinese, such as `1. 优化 logger 路径解析逻辑` or `1. fix log 2. 熔断器 3.Cache`. Keep commits concise and action-oriented; split unrelated work when practical. Pull requests should describe the behavioral change, list tests run, note configuration or security implications, and include screenshots only for user-visible output.

## Security & Configuration Tips

Do not commit `.env`, tokens, or generated logs. Required and optional runtime configuration includes `API_TOKEN`, `BASE_URL`, `AUTH_TOKEN`, `AUTH_REMOTE_VERIFY_URL`, `AUTH_IP_ALLOWLIST`, and `RAG_SEMANTIC_REWRITE`. Treat `/metrics` and `/health` exposure as deployment concerns and protect them with network controls where needed.
