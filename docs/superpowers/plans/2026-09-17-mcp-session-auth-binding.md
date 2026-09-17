# MCP Session Authentication Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind every stateful MCP session to the authenticated credential that created it, while preserving tenant Resource ACLs and stateless/stdio compatibility.

**Architecture:** Authentication derives an opaque SHA-256 identity and stores it in request context. Transport-aware middleware binds that identity to Streamable HTTP or SSE session IDs before those IDs reach clients, rejects mismatches before the MCP SDK, and removes bindings with the SDK session lifecycle. Resource handlers retain their corresponding `allowed_tools` checks as defense in depth.

**Tech Stack:** Go 1.25, `net/http`, `crypto/sha256`, `crypto/subtle`, MCP Go SDK v1.7.0, standard `testing` and `httptest`.

**Spec:** `docs/superpowers/specs/2026-09-17-mcp-session-auth-binding-design.md`

## Global Constraints

- Do not add dependencies, configuration fields, environment variables, or public wire fields.
- Never store or log plaintext Bearer Tokens, identity digests, Wiki content, or PII.
- Stateful sessions reject credential changes with HTTP 403; Token rotation requires reconnecting.
- Protocol `2026-07-28` remains stateless and must not create a binding.
- stdio remains unchanged.
- Preserve unrelated and pre-existing working-tree changes; stage only named files.
- Use TDD for each behavior change and `gofmt -w` only on changed Go files.

---

### Task 1: Opaque Authentication Identity and Resource ACL Context

**Files:**
- Modify: `internal/auth/auth.go`
- Modify: `internal/auth/auth_test.go`
- Modify: `internal/server/wiki_resources.go`
- Modify: `internal/server/wiki_resources_test.go`

**Interfaces:**
- Produces: `auth.SessionIdentity` with `Equal(auth.SessionIdentity) bool`.
- Produces: `auth.SessionIdentityFromContext(context.Context) (auth.SessionIdentity, bool)`.
- Preserves: `auth.WithTenantAllowedTools` and `auth.TenantToolAllowed`.

- [ ] **Step 1: Write failing authentication identity tests**

Add tests that pass real local-token, tenant-token, and IP-allowlist requests through `Authenticator.Middleware`, then inspect the context in the next handler:

```go
func captureSessionIdentity(t *testing.T, a *Authenticator, req *http.Request) (SessionIdentity, bool) {
	t.Helper()
	var identity SessionIdentity
	var ok bool
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok = SessionIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", rr.Code)
	}
	return identity, ok
}
```

Assert that the same credential produces equal identities, different credentials do not, and every successful network authentication produces an identity. Keep the existing tenant Resource tests that assert Catalog/Tree/Page map to `wiki_search`/`wiki_list_tree`/`wiki_get_page`.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/tmp/xktmcp-go-cache go test ./internal/auth ./internal/server \
  -run 'TestSessionIdentity|TestTenantToolAccessPropagatesToResourceRequests|TestWikiResourcesRequireCorrespondingTenantTool' \
  -count=1
```

Expected: build failure because `SessionIdentity` and `SessionIdentityFromContext` do not exist.

- [ ] **Step 3: Implement the opaque identity**

Add this non-exportable representation to `internal/auth/auth.go`:

```go
type SessionIdentity struct {
	digest [sha256.Size]byte
}

func (id SessionIdentity) Equal(other SessionIdentity) bool {
	return subtle.ConstantTimeCompare(id.digest[:], other.digest[:]) == 1
}

type sessionIdentityKey struct{}

func newSessionIdentity(mode, credential string) SessionIdentity {
	return SessionIdentity{digest: sha256.Sum256([]byte(mode + "\x00" + credential))}
}

func SessionIdentityFromContext(ctx context.Context) (SessionIdentity, bool) {
	if ctx == nil {
		return SessionIdentity{}, false
	}
	id, ok := ctx.Value(sessionIdentityKey{}).(SessionIdentity)
	return id, ok
}
```

Extend `authenticationDecision` with `sessionIdentity SessionIdentity` and `hasSessionIdentity bool`. Derive `newSessionIdentity("bearer", token)` for every successful Bearer path and `newSessionIdentity("ip", srcIP.String())` for the allowlist path. At the start of `serveAuthenticated`, attach it with `context.WithValue` before attaching tenant tools and trusted principal.

Keep Resource checks before backend access:

```go
if !auth.TenantToolAllowed(ctx, "wiki_search") { // Catalog
	return nil, mcp.ResourceNotFoundError(uri)
}
// Tree uses wiki_list_tree; Page uses wiki_get_page.
```

- [ ] **Step 4: Verify GREEN and format**

```bash
gofmt -w internal/auth/auth.go internal/auth/auth_test.go internal/server/wiki_resources.go internal/server/wiki_resources_test.go
GOCACHE=/tmp/xktmcp-go-cache go test ./internal/auth ./internal/server \
  -run 'TestSessionIdentity|TestTenantToolAccessPropagatesToResourceRequests|TestWikiResourcesRequireCorrespondingTenantTool' \
  -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go internal/server/wiki_resources.go internal/server/wiki_resources_test.go
git commit -m "fix: 绑定租户资源权限到认证上下文"
```

---

### Task 2: Binding Store and Streamable HTTP Middleware

**Files:**
- Create: `cmd/server/session_binding.go`
- Create: `cmd/server/session_binding_test.go`

**Interfaces:**
- Consumes: `auth.SessionIdentity` from Task 1.
- Produces: `newSessionBindings() *sessionBindings`.
- Produces: `streamableSessionBindingMiddleware(http.Handler, *sessionBindings) http.Handler`.

- [ ] **Step 1: Write failing store and middleware tests**

Build test identities by authenticating real requests; do not add a production test constructor. Add tests proving:

- first bind succeeds;
- the same identity matches;
- a different identity and a missing binding fail;
- concurrent first binds never overwrite an existing identity;
- initialization response header `Mcp-Session-Id: session-1` binds before being written;
- matching GET/POST/DELETE reaches the handler;
- mismatched or missing identity returns 403 without reaching it;
- accepted DELETE removes the binding;
- a response without a session header creates no binding.

```go
func TestSessionBindingsRejectDifferentIdentity(t *testing.T) {
	bindings := newSessionBindings()
	idA := authenticatedSessionIdentity(t, "token-a")
	idB := authenticatedSessionIdentity(t, "token-b")
	if !bindings.bind(streamableSessionTransport, "session-1", idA) {
		t.Fatal("initial bind failed")
	}
	if !bindings.matches(streamableSessionTransport, "session-1", idA) {
		t.Fatal("same identity did not match")
	}
	if bindings.matches(streamableSessionTransport, "session-1", idB) {
		t.Fatal("different identity matched")
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server \
  -run 'TestSessionBindings|TestStreamableSessionBindingMiddleware' -count=1
```

Expected: build failure because the store and middleware do not exist.

- [ ] **Step 3: Implement the concurrent binding store**

```go
type sessionTransport string

const (
	streamableSessionTransport sessionTransport = "streamable"
	sseSessionTransport        sessionTransport = "sse"
)

type sessionBindingKey struct {
	transport sessionTransport
	sessionID string
}

type sessionBindings struct {
	mu    sync.Mutex
	items map[sessionBindingKey]auth.SessionIdentity
}
```

Implement `bind`, `matches`, and `delete`. `bind` must atomically insert only when absent and otherwise call `SessionIdentity.Equal`. Never return map contents or log identities.

- [ ] **Step 4: Implement Streamable header capture**

Add `streamableBindingWriter` with `Header`, `WriteHeader`, `Write`, `Flush`, and `Unwrap`. Before forwarding the first header:

1. Read `Mcp-Session-Id`.
2. If absent, forward normally.
3. If identity is absent or binding fails, remove the session header, send 403 on the underlying writer, mark the wrapper blocked, and discard later SDK body writes.
4. Otherwise bind, then forward.

Implement request validation:

```go
func streamableSessionBindingMiddleware(next http.Handler, bindings *sessionBindings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, hasIdentity := auth.SessionIdentityFromContext(r.Context())
		sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
		if sessionID != "" && (!hasIdentity || !bindings.matches(streamableSessionTransport, sessionID, identity)) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodDelete && sessionID != "" {
			defer bindings.delete(streamableSessionTransport, sessionID)
		}
		next.ServeHTTP(newStreamableBindingWriter(w, bindings, identity, hasIdentity), r)
	})
}
```

`Flush` must process headers before calling `http.NewResponseController(w.ResponseWriter).Flush()`; `Unwrap` returns the underlying writer.

- [ ] **Step 5: Verify GREEN and race safety**

```bash
gofmt -w cmd/server/session_binding.go cmd/server/session_binding_test.go
GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server \
  -run 'TestSessionBindings|TestStreamableSessionBindingMiddleware' -count=1
GOCACHE=/tmp/xktmcp-go-cache go test -race ./cmd/server \
  -run 'TestSessionBindings|TestStreamableSessionBindingMiddleware' -count=1
```

Expected: both PASS with no race report.

- [ ] **Step 6: Commit**

```bash
git add cmd/server/session_binding.go cmd/server/session_binding_test.go
git commit -m "fix: 绑定 Streamable MCP 会话认证身份"
```

---

### Task 3: SSE Endpoint Binding Middleware

**Files:**
- Modify: `cmd/server/session_binding.go`
- Modify: `cmd/server/session_binding_test.go`

**Interfaces:**
- Consumes: Task 2 binding store and `sseSessionTransport`.
- Produces: `sseSessionBindingMiddleware(http.Handler, *sessionBindings) http.Handler`.

- [ ] **Step 1: Write failing SSE tests**

Use a fake handler that emits the SDK endpoint event:

```go
_, _ = io.WriteString(w, "event: endpoint\ndata: /messages/?sessionid=sse-1\n\n")
```

Test:

- an event split across two writes is buffered, bound, and forwarded once;
- the same identity may POST to `/messages/?sessionid=sse-1`;
- another or missing identity receives 403;
- missing session ID, wrong event name, and a first event over 4096 bytes fail before endpoint bytes are sent;
- `Flush` cannot expose a partial endpoint;
- returning from GET removes the binding.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server \
  -run 'TestSSESessionBindingMiddleware|TestSSEEndpointBindingWriter' -count=1
```

Expected: build failure because the SSE middleware and writer do not exist.

- [ ] **Step 3: Implement bounded first-event parsing**

Add `const maxSSEEndpointEventBytes = 4096` and `sseEndpointBindingWriter`. Buffer only until `\n\n` or `\r\n\r\n`. Normalize CRLF, require `event: endpoint`, parse the single `data:` value with `url.Parse`, and require non-empty query `sessionid`.

```go
func parseSSEEndpointSessionID(event []byte) (string, error) {
	normalized := strings.ReplaceAll(string(event), "\r\n", "\n")
	var eventName, data string
	for _, line := range strings.Split(strings.TrimSuffix(normalized, "\n\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if eventName != "endpoint" || data == "" {
		return "", errors.New("invalid SSE endpoint event")
	}
	u, err := url.Parse(data)
	if err != nil || strings.TrimSpace(u.Query().Get("sessionid")) == "" {
		return "", errors.New("SSE endpoint event has no session ID")
	}
	return strings.TrimSpace(u.Query().Get("sessionid")), nil
}
```

Bind before forwarding buffered bytes. On parse/bind failure, clear SSE headers, send HTTP 500, mark blocked, and discard later SDK writes. After readiness, forward all writes directly. Implement `Flush` and `Unwrap` without flushing partial endpoint data.

- [ ] **Step 4: Implement SSE request checks and cleanup**

```go
func sseSessionBindingMiddleware(next http.Handler, bindings *sessionBindings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, hasIdentity := auth.SessionIdentityFromContext(r.Context())
		if r.Method == http.MethodPost {
			sessionID := strings.TrimSpace(r.URL.Query().Get("sessionid"))
			if sessionID == "" || !hasIdentity || !bindings.matches(sseSessionTransport, sessionID, identity) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		writer := newSSEEndpointBindingWriter(w, bindings, identity, hasIdentity)
		next.ServeHTTP(writer, r)
		if writer.sessionID != "" {
			bindings.delete(sseSessionTransport, writer.sessionID)
		}
	})
}
```

- [ ] **Step 5: Verify GREEN and race safety**

```bash
gofmt -w cmd/server/session_binding.go cmd/server/session_binding_test.go
GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server \
  -run 'TestSSESessionBindingMiddleware|TestSSEEndpointBindingWriter' -count=1
GOCACHE=/tmp/xktmcp-go-cache go test -race ./cmd/server \
  -run 'TestSSESessionBindingMiddleware|TestSSEEndpointBindingWriter' -count=1
```

Expected: both PASS with no race report.

- [ ] **Step 6: Commit**

```bash
git add cmd/server/session_binding.go cmd/server/session_binding_test.go
git commit -m "fix: 绑定 SSE MCP 会话认证身份"
```

---

### Task 4: Wire Middleware and Add Cross-Token Transport Regressions

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/main_test.go`
- Modify: `cmd/server/wiki_resources_transport_test.go`

**Interfaces:**
- Consumes: both binding middleware functions.
- Extends: `(*wikiResourceAuthRoundTripper).setToken(string)`.

- [ ] **Step 1: Write failing transport tests**

Protect `token` with the round tripper's existing mutex and add:

```go
func (t *wikiResourceAuthRoundTripper) setToken(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = token
}
```

Configure:

```go
{Name: "tenant-low", Token: "token-low", UserID: "user-b", AllowedTools: []string{"wiki_search"}},
{Name: "tenant-peer", Token: "token-peer", UserID: "user-b", AllowedTools: []string{"*"}},
```

For SSE and legacy Streamable HTTP, connect with `token-a`, switch to `token-low`, call Page Resource, and require an error plus HTTP 403. Repeat with `token-peer` to prove equal permissions do not permit credential switching. Existing same-token tests must remain unchanged.

Add a modern `2026-07-28` test that sends independent authenticated stateless requests and asserts no `Mcp-Session-Id` and no 403.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server \
  -run 'TestAuthenticatedWikiResourcesTransportsIsolateTenants|TestStreamableHTTP.*2026' \
  -count=1
```

Expected: cross-token subtests fail because sessions are not yet bound.

- [ ] **Step 3: Wire middleware inside authentication**

In each network branch of `cmd/server/main.go`:

```go
// SSE
bindings := newSessionBindings()
finalHandler := authenticator.Middleware(sseSessionBindingMiddleware(sseHandler, bindings))

// Streamable HTTP
bindings := newSessionBindings()
finalHandler := authenticator.Middleware(streamableSessionBindingMiddleware(handler, bindings))
```

Apply the same composition in transport test setup. Keep `userIDMiddleware`, request logging, POST body deadlines, health, readiness, and metrics ordering unchanged.

- [ ] **Step 4: Verify GREEN**

```bash
gofmt -w cmd/server/main.go cmd/server/main_test.go cmd/server/wiki_resources_transport_test.go
GOCACHE=/tmp/xktmcp-go-cache go test ./cmd/server \
  -run 'TestAuthenticatedWikiResourcesTransportsIsolateTenants|TestStreamableHTTP.*2026|TestRequestBodyReadTimeoutMiddleware' \
  -count=1
```

Expected: PASS for switched-token rejection, same-token sessions, routed-user conflicts, request deadlines, and stateless HTTP.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go cmd/server/main_test.go cmd/server/wiki_resources_transport_test.go
git commit -m "fix: 拒绝跨凭据复用 MCP 会话"
```

---

### Task 5: Documentation, Review, and Release Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/release-readiness-audit-20260916.md`

**Interfaces:**
- Consumes: completed behavior from Tasks 1–4.
- Produces: deployer guidance and evidence-backed release status.

- [ ] **Step 1: Update bilingual README**

Document:

- Catalog/Tree/Page reuse `wiki_search`/`wiki_list_tree`/`wiki_get_page`;
- stateful SSE and legacy Streamable HTTP sessions are credential-bound;
- switching or rotating credentials returns 403 and requires reconnecting;
- stateless HTTP and stdio do not create bindings.

- [ ] **Step 2: Update the audit after focused tests pass**

Mark tenant Resource ACL and cross-credential session reuse fixed. Retain deployment configuration and `/metrics` risks. Record only commands actually run and keep working-tree status accurate.

- [ ] **Step 3: Run fresh release verification**

```bash
gofmt -w cmd/server/main.go cmd/server/session_binding.go cmd/server/session_binding_test.go \
  cmd/server/wiki_resources_transport_test.go internal/auth/auth.go internal/auth/auth_test.go \
  internal/server/wiki_resources.go internal/server/wiki_resources_test.go
GOCACHE=/tmp/xktmcp-go-cache go test -p=1 ./... -count=1 -timeout=120s
GOCACHE=/tmp/xktmcp-go-cache go test -race ./internal/auth ./internal/wiki ./internal/server ./cmd/server -count=1
GOCACHE=/tmp/xktmcp-go-cache go vet ./...
GOCACHE=/tmp/xktmcp-go-cache CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -tags=jsoniter -ldflags='-s -w' \
  -o /tmp/xktmcp-release-check ./cmd/server/main.go
git diff --check
```

Expected: all tests PASS, no races, vet and build exit 0, and the artifact is a statically linked stripped Linux x86-64 ELF.

- [ ] **Step 4: Request independent security review**

The reviewer must inspect identity non-disclosure, atomic binding, cleanup, ResponseWriter capabilities, SSE pre-forward parsing, cross-token coverage, and stateless/stdio compatibility. Fix every Critical or Important finding, then rerun Step 3.

- [ ] **Step 5: Commit documentation**

```bash
git add README.md docs/release-readiness-audit-20260916.md
git commit -m "docs: 更新 MCP 会话安全发布结论"
```

- [ ] **Step 6: Inspect final state**

```bash
git log -5 --oneline --decorate
git status --short
git diff HEAD --check
```

Expected: report every remaining modified or untracked path; claim a clean tree only if `git status --short` is empty.
