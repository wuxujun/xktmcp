# Task 4 report: bounded fail-closed payload inspection and principal binding

## Changed files

- `internal/auth/auth.go`
- `internal/auth/auth_test.go`

## RED

Initial test compilation before the required body-limit constant existed:

```text
$ go test ./internal/auth -run 'TestTenantLarge|TestMCPPayload|TestTenant.*Payload|TestTenant.*Principal' -v
# github.com/wuxujun/xktmcp/internal/auth [github.com/wuxujun/xktmcp/internal/auth.test]
internal/auth/auth_test.go:509:96: undefined: maxMCPRequestBodyBytes
FAIL    github.com/wuxujun/xktmcp/internal/auth [build failed]
FAIL
```

After adding only the specified constant, the behavioral RED command and output were:

```text
$ go test ./internal/auth -run 'TestTenantLarge|TestMCPPayload|TestTenant.*Payload|TestTenant.*Principal' -v
=== RUN   TestTenantUserIDIsStoredAsPrincipal
--- PASS: TestTenantUserIDIsStoredAsPrincipal (0.00s)
=== RUN   TestTenantLargeToolPayloadReachesHandlerIntact
    auth_test.go:503: status=200 body preserved=false
--- FAIL: TestTenantLargeToolPayloadReachesHandlerIntact (0.01s)
=== RUN   TestMCPPayloadOverLimitReturns413
    auth_test.go:515: status=200 called=true, want 413 false
--- FAIL: TestMCPPayloadOverLimitReturns413 (0.01s)
=== RUN   TestTenantPayloadAndPrincipalPolicy
=== RUN   TestTenantPayloadAndPrincipalPolicy/malformed
    auth_test.go:557: status=200 called=true user=""
=== RUN   TestTenantPayloadAndPrincipalPolicy/missing_tool
    auth_test.go:557: status=200 called=true user=""
=== RUN   TestTenantPayloadAndPrincipalPolicy/inject_principal
    auth_test.go:557: status=200 called=true user=""
=== RUN   TestTenantPayloadAndPrincipalPolicy/matching_principal
=== RUN   TestTenantPayloadAndPrincipalPolicy/body_conflict
    auth_test.go:557: status=200 called=true user="user-b"
=== RUN   TestTenantPayloadAndPrincipalPolicy/route_conflict
    auth_test.go:557: status=200 called=true user=""
--- FAIL: TestTenantPayloadAndPrincipalPolicy (0.01s)
    --- FAIL: TestTenantPayloadAndPrincipalPolicy/malformed (0.00s)
    --- FAIL: TestTenantPayloadAndPrincipalPolicy/missing_tool (0.00s)
    --- FAIL: TestTenantPayloadAndPrincipalPolicy/inject_principal (0.00s)
    --- PASS: TestTenantPayloadAndPrincipalPolicy/matching_principal (0.00s)
    --- FAIL: TestTenantPayloadAndPrincipalPolicy/body_conflict (0.00s)
    --- FAIL: TestTenantPayloadAndPrincipalPolicy/route_conflict (0.00s)
=== RUN   TestTenantWithoutPrincipalPreservesPayloadBytes
--- PASS: TestTenantWithoutPrincipalPreservesPayloadBytes (0.00s)
FAIL
FAIL    github.com/wuxujun/xktmcp/internal/auth    2.006s
FAIL
```

The first invocation initially needed sandbox escalation solely to permit writes to Go's shared build cache.

## GREEN

Focused regression command:

```text
$ go test ./internal/auth -run 'TestTenantLarge|TestMCPPayload|TestTenant.*Payload|TestTenant.*Principal' -v
--- PASS: TestTenantUserIDIsStoredAsPrincipal
--- PASS: TestTenantLargeToolPayloadReachesHandlerIntact
--- PASS: TestMCPPayloadOverLimitReturns413
--- PASS: TestTenantPayloadAndPrincipalPolicy
    --- PASS: malformed
    --- PASS: missing_tool
    --- PASS: inject_principal
    --- PASS: matching_principal
    --- PASS: body_conflict
    --- PASS: route_conflict
--- PASS: TestTenantWithoutPrincipalPreservesPayloadBytes
PASS
ok      github.com/wuxujun/xktmcp/internal/auth    2.274s
```

Complete Auth package:

```text
$ go test ./internal/auth -v
PASS
ok      github.com/wuxujun/xktmcp/internal/auth    1.898s
```

Repository suite:

```text
$ go test ./...
ok      github.com/wuxujun/xktmcp/cmd/server       1.004s
ok      github.com/wuxujun/xktmcp/internal/auth    1.567s
ok      github.com/wuxujun/xktmcp/internal/client  (cached)
ok      github.com/wuxujun/xktmcp/internal/logger  (cached)
ok      github.com/wuxujun/xktmcp/internal/metrics (cached)
?       github.com/wuxujun/xktmcp/internal/model   [no test files]
ok      github.com/wuxujun/xktmcp/internal/pii     (cached)
ok      github.com/wuxujun/xktmcp/internal/prompts (cached)
?       github.com/wuxujun/xktmcp/internal/server  [no test files]
ok      github.com/wuxujun/xktmcp/internal/service (cached)
ok      github.com/wuxujun/xktmcp/internal/tools   (cached)
ok      github.com/wuxujun/xktmcp/internal/trace   (cached)
ok      github.com/wuxujun/xktmcp/internal/wiki    (cached)
```

`git diff --check` completed with no output.

## Implementation and self-review

- Middleware reads a POST body once, limits it to 4 MiB, returns a generic 413 before authentication/handler processing when exceeded, and restores exact accepted bytes.
- Tenant and principal-protected POSTs use a JSON-RPC object representation backed by `map[string]json.RawMessage`; only principal insertion re-marshals fields, so unknown top-level, params, and arguments members are retained.
- Tenant `tools/call` requests fail closed on malformed JSON, invalid/missing `params`, missing tool name, or a tool absent from the tenant ACL. ACL denials remain 401; malformed payloads are generic 400.
- A non-empty trusted tenant/remote principal must match both routed context and supplied `arguments.userId`; conflicts receive generic 403. Matching/absent input is bound to the trusted principal.
- Local-token and IP-allowlist paths preserve accepted POST bodies without parsing them. Token and identity values are masked in rejection logs; responses use only generic HTTP status text.
- Preserved existing session middleware and did not edit server wiring or common tool wrappers.
- Scope check: only `internal/auth/auth.go` and `internal/auth/auth_test.go` are modified in the task worktree. The report is stored outside that worktree as requested.

## Intended commit subject

`fix: bind principals with bounded MCP payload inspection`

## Concerns

None identified. The shared Go build cache requires elevated sandbox permission for test execution in this environment.

## Fix round 1: preserve routed identity during remote authentication

### Root cause and implementation

Remote verification correctly returned principal `user-a`, but the remote branch then called `trace.WithUserID(ctx, userID)`. That overwrote the routing identity previously derived from `/mcp?userId=user-b`, so `serveAuthenticated` observed `user-a` as both routed and authenticated identities and incorrectly allowed the request.

The remote branch now retains the legacy private `ctxKeyUserID` and session-map behavior, leaves `trace.UserIDFromContext` untouched, and records the remote result using `trace.WithAuthenticatedUserID(ctx, userID)`. The common principal conflict check therefore receives the original routed identity and rejects the mismatch.

Added `TestRemotePrincipalRejectsRoutedURLUserConflict`, which uses a real remote verification test server returning `{"userid":"user-a"}`, an HTTP request to `/mcp?userId=user-b`, and the same URL-derived routing context used by server wiring. It asserts generic HTTP 403 and that the downstream handler is not called.

### RED

```text
$ go test ./internal/auth -run 'TestRemotePrincipalRejectsRoutedURLUserConflict' -v
=== RUN   TestRemotePrincipalRejectsRoutedURLUserConflict
    auth_test.go:584: status=200 called=true, want 403 false
--- FAIL: TestRemotePrincipalRejectsRoutedURLUserConflict (0.03s)
FAIL
FAIL    github.com/wuxujun/xktmcp/internal/auth    2.059s
FAIL
```

### GREEN

Focused conflict command:

```text
$ go test ./internal/auth -run 'TestTenant.*Payload|TestRemotePrincipalRejectsRoutedURLUserConflict' -v
=== RUN   TestTenantLargeToolPayloadReachesHandlerIntact
--- PASS: TestTenantLargeToolPayloadReachesHandlerIntact (0.02s)
=== RUN   TestTenantPayloadAndPrincipalPolicy
--- PASS: TestTenantPayloadAndPrincipalPolicy (0.00s)
    --- PASS: malformed (0.00s)
    --- PASS: missing_tool (0.00s)
    --- PASS: inject_principal (0.00s)
    --- PASS: matching_principal (0.00s)
    --- PASS: body_conflict (0.00s)
    --- PASS: route_conflict (0.00s)
=== RUN   TestRemotePrincipalRejectsRoutedURLUserConflict
--- PASS: TestRemotePrincipalRejectsRoutedURLUserConflict (0.02s)
=== RUN   TestTenantWithoutPrincipalPreservesPayloadBytes
--- PASS: TestTenantWithoutPrincipalPreservesPayloadBytes (0.00s)
PASS
ok      github.com/wuxujun/xktmcp/internal/auth    2.193s
```

Complete Auth package:

```text
$ go test ./internal/auth -v
PASS
ok      github.com/wuxujun/xktmcp/internal/auth    0.960s
```

`git diff --check` completed successfully with no output. The task worktree remains limited to `internal/auth/auth.go` and `internal/auth/auth_test.go`; no Git index writes were attempted.
