# Authentication Boundary Hardening Design

Date: 2026-08-30

## 1. Objective

Harden the first-stage authentication boundary without breaking existing n8n deployments that use a shared `AUTH_TOKEN`, an IP allowlist, or stdio and provide `userId` as an envelope field.

This stage addresses five related defects:

1. A request-supplied `userId` can currently override a remotely authenticated identity.
2. Tenant ACL inspection silently truncates request bodies at 64 KiB and skips authorization when parsing fails.
3. The remote verification cache is unbounded and does not remove expired entries proactively.
4. The stateful session-to-user map has no integrated lifecycle cleanup and cannot solve identity propagation for stateless HTTP.
5. An explicitly configured but invalid remote verification endpoint is treated as an enabled authentication method.

POST retry safety and RAG response-contract fixes are intentionally outside this stage.

## 2. Compatibility and Trust Model

The service will distinguish a trusted authenticated principal from an untrusted routing `userId`.

Trusted principals come from:

- A successful remote Token verification response containing `userid`.
- An `AUTH_TENANTS` entry with an explicit `user_id`.

Routing-only identities come from:

- The MCP tool envelope field `userId`.
- The HTTP query parameter `userId`.

Authentication through a shared `AUTH_TOKEN`, an IP allowlist, or stdio does not prove an individual user identity. These modes may continue using a routing-only `userId` for backward compatibility, but that value must not be described or treated as a security boundary.

When a trusted principal exists:

- It is authoritative for tool execution, cache isolation, Wiki routing, and audit logging.
- A matching request `userId` is accepted.
- A conflicting request `userId` is rejected with HTTP 403 or an MCP tool error at the non-HTTP defense layer.
- An omitted request `userId` is populated from the trusted principal before the MCP SDK processes the call.

## 3. Chosen Architecture

### 3.1 HTTP principal binding

Authentication middleware will bind the trusted principal directly into a `tools/call` request body after authentication and before handing the request to the MCP SDK.

This approach is selected because the SDK detaches HTTP request contexts and modern stateless Streamable HTTP does not provide a stable `Mcp-Session-Id`. Binding the principal into the arguments works consistently for stateful Streamable HTTP, stateless Streamable HTTP, and SSE message POSTs.

The request flow will be:

1. For a POST with a body, read at most 4 MiB plus one detection byte.
2. Return HTTP 413 when the payload exceeds 4 MiB.
3. Restore the exact body bytes after inspection so downstream MCP processing receives the complete request.
4. Authenticate the request and produce an authentication decision containing an optional trusted principal and optional tenant.
5. Parse legal JSON-RPC payloads needed for tenant ACL or principal binding.
6. For tenant `tools/call` requests, require a reliably extracted tool name and enforce `allowed_tools` fail-closed.
7. If a trusted principal exists, reject a conflicting argument `userId`; otherwise set `arguments.userId` to that principal.
8. Marshal the modified envelope, restore it as the request body, and call the MCP handler.

Valid MCP methods other than `tools/call`, including initialization, notifications, and ping traffic, remain allowed after authentication. Malformed JSON is rejected with HTTP 400 when payload inspection is required.

JSON-RPC batch payload support is not introduced in this stage. A tenant batch payload that cannot be authorized deterministically is rejected rather than allowed.

### 3.2 Defense-in-depth identity resolution

The `trace` package will maintain separate context values for:

- Trusted authenticated principal.
- Routing-only user ID.

It will expose a resolver that returns the effective user ID or an identity-conflict error. The resolution order is:

1. Trusted authenticated principal.
2. Explicit tool argument `userId`.
3. Routing-only context value from the URL.

When a trusted principal is present, both untrusted sources must either be empty or equal to it.

The common tool wrapper will resolve identity before invoking a Handler. This ensures direct Handler tests, future transports, and any path that bypasses HTTP principal binding still reject conflicting identities. Audit logging will use the resolved identity instead of the raw envelope value.

Handlers may continue using a convenience `EffectiveUserID` helper, but that helper must prefer the trusted principal. The wrapper is responsible for returning a structured MCP error on conflicts.

### 3.3 Removal of session identity state

The `sessionUserID` map, `MCPMiddleware` session lookup, and unused `CleanSession` mechanism will be removed after HTTP principal binding is covered by tests.

This eliminates the unbounded session map and avoids relying on lifecycle hooks that are not consistently available for all transports. It also removes the first-request and stateless-session gaps inherent in the previous approach.

## 4. Request Size and ACL Rules

The maximum inspected MCP request body is 4 MiB. This is deliberately larger than the current local Wiki default file size of 2 MiB, leaving room for the JSON-RPC envelope and escaped Markdown content.

Behavior is fixed as follows:

| Condition | Result |
| --- | --- |
| Body exceeds 4 MiB | HTTP 413 |
| Inspected JSON-RPC is malformed | HTTP 400 |
| Tenant `tools/call` has no reliable tool name | Reject; no ACL bypass |
| Tenant tool is not allowed | HTTP 401, retaining existing authentication-denial behavior |
| Trusted principal conflicts with request `userId` | HTTP 403 |
| Authentication credentials are invalid | HTTP 401 |
| Valid non-tool MCP method | Continue normally |

The middleware must read the request body only once and must never replace it with a truncated prefix.

## 5. Tenant Principal Configuration

`TenantConfig` gains:

```go
UserID string `json:"user_id"`
```

`user_id` is optional. When present, it becomes the trusted principal for that tenant. When absent, the tenant keeps its existing Token validation, rate limiting, and tool ACL behavior but does not establish a user-level principal.

No implicit mapping from tenant `name` to `user_id` will be added because display names and application user identifiers are different concepts.

## 6. Bounded Remote Verification Cache

The authentication package will own a dedicated concurrent TTL/LRU cache rather than importing the Tools cache.

Properties:

- Default maximum: 4096 entries.
- Configurable through `AUTH_REMOTE_CACHE_MAX_ENTRIES`.
- Existing positive TTL default: 5 minutes.
- Existing negative TTL default: 30 seconds.
- `Get` removes an entry immediately when it is expired.
- `Set` updates LRU order, removes expired entries, and evicts least-recently-used entries until the maximum is respected.
- No janitor goroutine is required, so the cache has no shutdown lifecycle.
- The configured maximum must be positive; invalid values cause startup failure.

This cache bounds memory even under a sustained stream of unique invalid Tokens while retaining the existing remote verification rate limiter.

## 7. Authentication Configuration Validation

`auth.New` will return `(*Authenticator, error)` so explicitly invalid security configuration cannot be silently disabled.

Construction fails when:

- `AUTH_REMOTE_VERIFY_URL` is present but is not a valid HTTP(S) URL.
- Its host is absent from `AUTH_REMOTE_ALLOWED_HOSTS`.
- Remote cache capacity is non-positive or cannot be parsed by server configuration assembly.
- `AUTH_TENANTS` was configured but produces no tenant with a usable Token or Token hash.

`Authenticator.Enabled()` will report the authentication methods that were actually constructed:

- Non-empty local Token.
- At least one valid tenant.
- Valid remote verification configuration.
- At least one allowed CIDR.

The server entry point will log the constructor error and refuse to start. Error messages must not contain raw Tokens.

## 8. Error and Audit Behavior

- Oversized payloads return 413 with a generic message.
- Malformed inspected payloads return 400 with a generic message.
- Principal conflicts return 403 and create a structured security/audit log containing only masked identities, request path, and authentication mode.
- Invalid credentials continue returning 401 with `WWW-Authenticate`.
- Token values and Authorization headers remain masked.
- Tool-level conflict errors use `CallToolResult.IsError=true` and do not invoke the business Handler.

## 9. Files and Responsibilities

Expected production changes:

- `internal/trace/trace.go`: separate trusted principal from routing identity and resolve conflicts.
- `internal/auth/auth.go`: authentication decision, bounded payload inspection, tenant principal binding, fail-closed ACL, bounded remote cache, and constructor validation.
- `cmd/server/main.go`: parse remote cache capacity, handle `auth.New` errors, and remove obsolete MCP session middleware registration.
- `internal/server/register.go`: resolve identity once in the common tool wrapper and audit the resolved principal.
- `README.md`: document `AUTH_TENANTS.user_id`, 4 MiB request behavior, remote cache capacity, and the distinction between routing and authenticated identity.

Expected test changes:

- `internal/trace/trace_test.go`: trusted-principal precedence and conflict cases.
- `internal/auth/auth_test.go`: large body preservation, 413 behavior, malformed fail-closed behavior, ACL enforcement, principal binding/conflict, cache bounds/TTL, constructor failures, and tenant principal behavior.
- `cmd/server/main_test.go`: configuration parsing and HTTP integration behavior as needed.
- `internal/server/register_test.go`: common wrapper conflict rejection and resolved audit/Handler identity if a focused server test is required.

## 10. Test Strategy

Implementation will follow strict red-green-refactor cycles. Each production change must be preceded by a focused failing test whose expected outcome is derived independently.

Required regression cases:

1. A 100 KiB Wiki-style `tools/call` payload reaches the downstream Handler intact for an allowed tenant.
2. A payload larger than 4 MiB returns 413 and never reaches the Handler.
3. Malformed or uninspectable tenant tool calls cannot bypass ACL.
4. A remote principal is inserted when `arguments.userId` is absent.
5. A matching request identity succeeds; a conflicting identity returns 403.
6. Shared Token, IP allowlist, and no-principal tenant flows preserve routing-only `userId` compatibility.
7. Stateless HTTP tool calls receive the bound principal without a session map.
8. Authentication cache never exceeds its configured capacity and expired entries are removed.
9. Invalid remote verification configuration prevents authenticator construction.
10. Tenant `user_id` is applied as the trusted principal.

Final verification commands:

```bash
go test ./internal/trace ./internal/auth ./internal/server ./cmd/server
go test ./...
go test -race ./...
go vet ./...
```

Tests using `httptest.NewServer` require permission to bind a local loopback port in the execution environment.

## 11. Out of Scope

- POST retry Body recreation and write idempotency.
- RAG `top_k`, `min_score`, `include_sources`, and `include_chunks` behavior.
- Full Wiki authorization policy beyond binding a trusted principal to the existing configured user mapping.
- JSON-RPC batch request support.
- General tool-selection or local-Wiki-only startup modes.
