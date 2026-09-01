## Task 7 final-review fix wave

Base reviewed: `b894d1dc7f17206c79a324eaa379235e49dee150`.

### Findings addressed

1. **Unbounded pre-auth request logging for `LOG_HTTP_PAYLOAD_MAX_BYTES=0`**
   - Root cause: `requestLoggingMiddleware` passed a zero-limit POST body directly to `io.ReadAll`, so the outer logger consumed the entire payload before `auth.Middleware` could apply its 4 MiB bound.
   - Fix: POST request logging now caps its source read and capture limit at `protocolDetectionMaxBytes` (4 MiB) when logging is unlimited or configured above that boundary. It retains the existing configured behavior for positive limits at or below the boundary, and reads one extra sentinel byte so truncation remains detectable. The replay body continues to contain the captured prefix followed by the unread original stream, allowing Auth to return its normal 413 response.
   - Regression: `TestRequestLoggingMiddlewareBoundsZeroLimitPOSTBeforeAuth` composes `requestLoggingMiddleware` outside the real `auth.Middleware`, enables logging with `MaxBytes: 0`, sends a body larger than 4 MiB, and verifies: (a) 413, (b) downstream is not called, and (c) the original source was read only through the 4 MiB-plus-one-byte sentinel. The test was written first and failed on the base code with `source bytes read=4198400, want auth boundary sentinel=4194305`; it passed after the source change.

2. **Missing English authentication documentation**
   - Added `Authentication configuration (English)` to `README.md`, equivalent to the Chinese authentication section. It includes the exact tenant `user_id` JSON example; trusted tenant and remote `userid` principals; missing injection and 403 conflict behavior; routing-only semantics for shared `AUTH_TOKEN`, IP allowlist, and stdio `userId`; the 4 MiB/413 POST limit; and positive `AUTH_REMOTE_CACHE_MAX_ENTRIES` with its 4096 default.

### Verification evidence

- `gofmt -w cmd/server/main.go cmd/server/main_test.go`
- Focused red test before implementation:
  `go test ./cmd/server -run '^TestRequestLoggingMiddlewareBoundsZeroLimitPOSTBeforeAuth$' -count=1 -v`
  - Failed as expected: `source bytes read=4198400, want auth boundary sentinel=4194305`.
- Focused green tests:
  `go test ./cmd/server -run '^(TestRequestLoggingMiddlewareBoundsZeroLimitPOSTBeforeAuth|TestRequestLoggingMiddlewareLogsPayloadsWithoutTruncatingRequest|TestRequestLoggingMiddlewareOmitsPayloadsWhenDisabled)$' -count=1 -v`
  - PASS (`github.com/wuxujun/xktmcp/cmd/server`).
- Full test suite: `go test ./...` — PASS for all packages.
- Relevant race check: `go test -race ./cmd/server ./internal/auth` — PASS.
- Static analysis: `go vet ./...` — exit 0.
- Build: `go build ./cmd/server` — exit 0.
- Final whitespace/diff check: `git diff --check` — exit 0.

### Self-review

- The regression fails when the POST source-read cap is removed, so it protects the actual unbounded-read failure rather than only restating Auth's existing 413 behavior.
- The implementation leaves the original replay mechanism intact; Auth reads the replayed prefix, sees the sentinel byte, and rejects before the downstream handler.
- Positive payload limits below 4 MiB retain their existing bounded capture/read behavior, covered by the existing 4-byte truncation test.
- Changes are limited to `cmd/server/main.go`, `cmd/server/main_test.go`, and `README.md`; this report is stored outside the worktree as requested. No Git index writes or commits were attempted.
