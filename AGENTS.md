# Repository Guidelines

## Scope and Workflow

This repository is a Go MCP server module (`github.com/wuxujun/xktmcp`). Make the smallest safe change that satisfies the task. Do not refactor unrelated code, upgrade dependencies, rename public APIs, or change configuration defaults unless explicitly requested.

Before editing, identify the target behavior, relevant package, and focused verification command. Inspect files named by the task first. If more files are needed, explain why and inspect only the smallest relevant set.

For non-trivial work, use this order:

1. Inspect the relevant code and tests.
2. State the root cause and minimal change plan briefly.
3. Modify only the necessary files.
4. Run formatting and focused tests.
5. Report changed files, verification, and remaining risk.

Stop and ask for direction instead of broadening scope when requirements conflict, a change affects public behavior, a migration is needed, or the focused test fails twice.

## Project Structure

The entry point is `cmd/server/main.go`; it wires transport, authentication, logging, and graceful shutdown.

Core code is under `internal/`:

- `client`: upstream HTTP clients and retry helpers
- `service`: validation and orchestration
- `tools`: MCP tools and schemas
- `model`: DTOs
- `auth`, `logger`, `metrics`, `pii`, `trace`: shared support packages

Tests are colocated with source code as `*_test.go` files.

## Context and Tool Limits

Keep context small and relevant.

- Read only files relevant to the task; begin with files explicitly named by the user.
- Inspect no more than 5 files before presenting a plan, unless the task clearly requires more.
- Read no more than 250 lines per file operation; use targeted ranges, `rg -n`, or symbol searches.
- Limit search output to 20 matches. Search narrow paths before searching the repository root.
- Do not repeatedly read unchanged files or rerun commands whose result cannot change.
- Limit command output to 120 relevant lines. Filter output with `rg`, `head`, `tail`, or test/package selectors.
- Do not inspect `.git/`, `vendor/`, `node_modules/`, `dist/`, `build/`, `coverage/`, generated artifacts, or log archives unless explicitly required.
- Do not print secrets, `.env` contents, tokens, credentials, or full production logs.
- Run at most two implementation-and-retest cycles. If still failing, stop and report the evidence, hypothesis, and next diagnostic step.

Use focused commands first. Do not run full-repository tests, broad linters, Docker rebuilds, migrations, or network-dependent commands unless requested or necessary for final verification.

## Build and Test

- `go run ./cmd/server/main.go` starts the server using the default stdio transport.
- `go run ./cmd/server/main.go -transport=http -port=8081` runs Streamable HTTP on `/mcp`; add `-debug` only when debugging.
- `go run ./cmd/server/main.go -transport=sse -port=8081` runs SSE endpoints.
- `go test ./internal/<package> -run TestName -v` runs a focused test while debugging.
- `go test ./internal/<package>` runs a focused package test suite.
- `go test ./...` runs the full suite; use it before a PR or only when the task explicitly requires repository-wide verification.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter -ldflags="-s -w" -o mcp-server ./cmd/server/main.go` builds the Linux release binary.

After changing Go files, run `gofmt -w` only on changed `.go` files. Prefer the narrowest relevant test command. Do not start long-running servers unless the task requires runtime verification.

## Go Conventions

Use idiomatic Go and standard formatting.

- Keep package names short and lowercase.
- Export only APIs required across packages; prefer unexported helpers within `internal` packages.
- Follow existing names such as `StudentAPI`, `RagAPI`, `*Service`, `*Tool`, and `*Handler`.
- Preserve existing public request and response shapes unless a breaking change is requested.
- Wrap returned errors with useful operation context while preserving error inspection with `%w` where appropriate.
- Pass `context.Context` through I/O paths.
- Use structured helpers from `internal/logger`; do not add direct `log.Printf` calls.
- Avoid new dependencies when the standard library or an existing dependency is sufficient.

## Testing

Use Go's standard `testing` package and place tests beside the code under test.

- Add or update focused tests whenever behavior changes.
- Prefer table-driven tests for validation, auth, schemas, and error cases.
- Cover changed behavior in auth, schema validation, PII redaction, caching, metrics, and client error handling where applicable.
- Test observable behavior rather than private implementation details.
- Run `go test ./...` before opening a PR when feasible; otherwise report the exact focused tests run and why full-suite verification was not run.

## Security and Configuration

Do not commit or expose `.env` files, API tokens, credentials, generated logs, or PII.

Runtime configuration can include `API_TOKEN`, `BASE_URL`, `AUTH_TOKEN`, `AUTH_REMOTE_VERIFY_URL`, `AUTH_IP_ALLOWLIST`, and `RAG_SEMANTIC_REWRITE`.

Treat changes to authentication, remote verification, IP allowlists, PII redaction, `/metrics`, `/health`, outbound HTTP behavior, and logging as security-sensitive. Highlight their deployment or security impact before making behavior-changing edits. Protect `/metrics` and `/health` with deployment-level network controls where needed.

## Commits and Final Response

Keep commits concise, action-oriented, and limited to one concern. Existing style often uses short Chinese numbered summaries, for example `1. 优化 logger 路径解析逻辑`.

For a pull request, describe the behavioral change, focused tests run, configuration or security impact, and any remaining risk. Include screenshots only for user-visible output.

In the final response, provide only:

1. Changed files
2. What changed
3. Verification command and result
4. Configuration, security, or compatibility impact
5. Remaining risk or next step
