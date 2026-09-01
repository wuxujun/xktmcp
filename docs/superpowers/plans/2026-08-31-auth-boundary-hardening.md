# Authentication Boundary Hardening Implementation Plan

[中文版](2026-08-31-auth-boundary-hardening.zh-CN.md)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind authenticated principals to MCP tool calls, enforce bounded fail-closed payload inspection, bound remote authentication memory, and reject invalid authentication configuration without breaking shared-token routing compatibility.

**Architecture:** HTTP authentication produces an optional trusted principal and binds it into `tools/call` arguments before the MCP SDK detaches the request context. The Trace package independently resolves trusted and routing identities as defense in depth, while Auth owns a bounded TTL/LRU verification cache and validates configuration during construction.

**Tech Stack:** Go 1.25, `net/http`, `encoding/json`, `container/list`, MCP Go SDK v1.7.0, Go `testing` and `httptest`.

**Spec:** `docs/superpowers/specs/2026-08-30-auth-boundary-hardening-design.md`

## Global Constraints

- Keep shared `AUTH_TOKEN`, IP allowlist, and stdio routing-only `userId` behavior backward compatible.
- Only remote verification `userid` and `AUTH_TENANTS.user_id` are trusted principals.
- Reject trusted-principal conflicts; never let an envelope or URL `userId` override a trusted principal.
- The inspected MCP POST body limit is exactly 4 MiB (`4 << 20` bytes).
- Tenant ACL parsing is fail-closed; JSON-RPC batch support is out of scope.
- Remote verification cache defaults to 4096 entries, positive TTL 5 minutes, and negative TTL 30 seconds.
- Do not add new third-party dependencies.
- Do not include the user's unrelated README/Wiki/log working-tree changes in task commits.
- Follow strict RED-GREEN-REFACTOR: no production change before its focused failing test.

---

### Task 1: Separate trusted principals from routing identities

**Files:**
- Modify: `internal/trace/trace.go:12-79`
- Test: `internal/trace/trace_test.go:61-73`

**Interfaces:**
- Produces: `var ErrUserIDConflict error`
- Produces: `func WithAuthenticatedUserID(context.Context, string) context.Context`
- Produces: `func AuthenticatedUserIDFromContext(context.Context) string`
- Produces: `func ResolveUserID(context.Context, string) (string, error)`
- Changes: `func EffectiveUserID(context.Context, string) string` to prefer the trusted principal.

- [ ] **Step 1: Replace the existing precedence test with failing trusted-principal tests**

Add literal, table-driven expectations:

```go
func TestResolveUserID(t *testing.T) {
	trusted := WithAuthenticatedUserID(WithUserID(context.Background(), "trusted-user"), "trusted-user")
	got, err := ResolveUserID(trusted, " trusted-user ")
	if err != nil || got != "trusted-user" {
		t.Fatalf("ResolveUserID() = %q, %v; want trusted-user, nil", got, err)
	}

	conflict := WithAuthenticatedUserID(WithUserID(context.Background(), "url-user"), "trusted-user")
	if _, err := ResolveUserID(conflict, ""); !errors.Is(err, ErrUserIDConflict) {
		t.Fatalf("ResolveUserID conflict error = %v, want ErrUserIDConflict", err)
	}

	fallback := WithUserID(context.Background(), "url-user")
	got, err = ResolveUserID(fallback, " argument-user ")
	if err != nil || got != "argument-user" {
		t.Fatalf("ResolveUserID fallback = %q, %v; want argument-user, nil", got, err)
	}
}

func TestEffectiveUserIDPrefersAuthenticatedPrincipal(t *testing.T) {
	ctx := WithAuthenticatedUserID(WithUserID(context.Background(), "url-user"), "trusted-user")
	if got := EffectiveUserID(ctx, "argument-user"); got != "trusted-user" {
		t.Fatalf("EffectiveUserID() = %q, want trusted-user", got)
	}
}
```

Import `errors` in the test.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/trace -run 'TestResolveUserID|TestEffectiveUserIDPrefersAuthenticatedPrincipal' -v`

Expected: build failure because the authenticated-principal API and `ErrUserIDConflict` do not exist.

- [ ] **Step 3: Implement the minimal trusted-principal context API**

Use a separate private context key and conflict validation:

```go
var ErrUserIDConflict = errors.New("authenticated userId conflicts with requested userId")

type authenticatedUserIDKey struct{}

func WithAuthenticatedUserID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, authenticatedUserIDKey{}, strings.TrimSpace(uid))
}

func AuthenticatedUserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	uid, _ := ctx.Value(authenticatedUserIDKey{}).(string)
	return strings.TrimSpace(uid)
}

func ResolveUserID(ctx context.Context, explicit string) (string, error) {
	principal := AuthenticatedUserIDFromContext(ctx)
	explicit = strings.TrimSpace(explicit)
	routed := strings.TrimSpace(UserIDFromContext(ctx))
	if principal != "" {
		if (explicit != "" && explicit != principal) || (routed != "" && routed != principal) {
			return "", ErrUserIDConflict
		}
		return principal, nil
	}
	if explicit != "" {
		return explicit, nil
	}
	return routed, nil
}

func EffectiveUserID(ctx context.Context, explicit string) string {
	if principal := AuthenticatedUserIDFromContext(ctx); principal != "" {
		return principal
	}
	if uid := strings.TrimSpace(explicit); uid != "" {
		return uid
	}
	return strings.TrimSpace(UserIDFromContext(ctx))
}
```

- [ ] **Step 4: Run Trace tests and verify GREEN**

Run: `go test ./internal/trace -v`

Expected: all Trace tests pass.

- [ ] **Step 5: Commit the isolated identity primitive**

```bash
git add internal/trace/trace.go internal/trace/trace_test.go
git commit -m "fix: separate authenticated and routing identities"
```

### Task 2: Add a bounded TTL/LRU verification cache

**Files:**
- Create: `internal/auth/cache.go`
- Create: `internal/auth/cache_test.go`

**Interfaces:**
- Consumes: existing `cacheEntry` from `internal/auth/auth.go`.
- Produces: `func newVerificationCache(maxEntries int, now func() time.Time) *verificationCache`
- Produces: `func (*verificationCache) Get(string) (cacheEntry, bool)`
- Produces: `func (*verificationCache) Set(string, cacheEntry)`
- Produces: `func (*verificationCache) Len() int`

- [ ] **Step 1: Write failing cache behavior tests**

Create tests that catch missing expiration and missing capacity enforcement:

```go
func TestVerificationCacheExpiresEntries(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newVerificationCache(2, func() time.Time { return now })
	cache.Set("expired", cacheEntry{ok: true, exp: now.Add(time.Second)})
	now = now.Add(2 * time.Second)
	if _, ok := cache.Get("expired"); ok {
		t.Fatal("expired verification result remained readable")
	}
	if cache.Len() != 0 {
		t.Fatalf("cache Len() = %d, want 0 after expiration", cache.Len())
	}
}

func TestVerificationCacheEvictsLeastRecentlyUsed(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newVerificationCache(2, func() time.Time { return now })
	entry := cacheEntry{ok: true, exp: now.Add(time.Hour)}
	cache.Set("a", entry)
	cache.Set("b", entry)
	_, _ = cache.Get("a")
	cache.Set("c", entry)
	if _, ok := cache.Get("b"); ok {
		t.Fatal("least recently used entry b was not evicted")
	}
	if cache.Len() != 2 {
		t.Fatalf("cache Len() = %d, want 2", cache.Len())
	}
}
```

- [ ] **Step 2: Run the new tests and verify RED**

Run: `go test ./internal/auth -run TestVerificationCache -v`

Expected: build failure because `newVerificationCache` does not exist.

- [ ] **Step 3: Implement the minimal concurrent LRU cache**

Use `sync.Mutex`, `map[string]*list.Element`, and a list whose front is most recently used. Each element stores the key and `cacheEntry`. `Get` must delete expired entries; `Set` must first delete all expired entries, then update or insert, and evict from the back until `Len() <= maxEntries`.

```go
type verificationCacheItem struct {
	key   string
	entry cacheEntry
}

type verificationCache struct {
	mu         sync.Mutex
	items      map[string]*list.Element
	lru        *list.List
	maxEntries int
	now        func() time.Time
}

func newVerificationCache(maxEntries int, now func() time.Time) *verificationCache {
	if maxEntries <= 0 {
		panic("auth: verification cache capacity must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &verificationCache{
		items:      make(map[string]*list.Element),
		lru:        list.New(),
		maxEntries: maxEntries,
		now:        now,
	}
}

func (c *verificationCache) Get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	item := el.Value.(*verificationCacheItem)
	if !item.entry.exp.IsZero() && !c.now().Before(item.entry.exp) {
		c.remove(el)
		return cacheEntry{}, false
	}
	c.lru.MoveToFront(el)
	return item.entry, true
}

func (c *verificationCache) Set(key string, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for _, el := range c.items {
		item := el.Value.(*verificationCacheItem)
		if !item.entry.exp.IsZero() && !now.Before(item.entry.exp) {
			c.remove(el)
		}
	}
	if el, ok := c.items[key]; ok {
		el.Value.(*verificationCacheItem).entry = entry
		c.lru.MoveToFront(el)
		return
	}
	c.items[key] = c.lru.PushFront(&verificationCacheItem{key: key, entry: entry})
	for c.lru.Len() > c.maxEntries {
		c.remove(c.lru.Back())
	}
}

func (c *verificationCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *verificationCache) remove(el *list.Element) {
	delete(c.items, el.Value.(*verificationCacheItem).key)
	c.lru.Remove(el)
}
```

Validated runtime configuration must never reach the constructor panic branch.

- [ ] **Step 4: Run cache and Auth package tests and verify GREEN**

Run: `go test ./internal/auth -run TestVerificationCache -v`

Expected: both new tests pass.

- [ ] **Step 5: Commit the cache in isolation**

```bash
git add internal/auth/cache.go internal/auth/cache_test.go
git commit -m "fix: bound remote authentication cache"
```

### Task 3: Validate authenticator construction and tenant principals

**Files:**
- Modify: `internal/auth/auth.go:32-228,462-494`
- Modify: `internal/auth/auth_test.go:16-443`

**Interfaces:**
- Consumes: `newVerificationCache` from Task 2.
- Adds: `TenantConfig.UserID string` with JSON key `user_id`.
- Adds: `Config.RemoteCacheMaxEntries int` and `Config.TenantsConfigured bool`.
- Changes: `func New(Config) (*Authenticator, error)`.
- Changes: `Authenticator.cache` from `sync.Map` to `*verificationCache`.

- [ ] **Step 1: Add a test helper and failing constructor-validation tests**

Add this helper, then mechanically replace existing successful `New(...)` calls with `mustAuthenticator(t, ...)`:

```go
func mustAuthenticator(t *testing.T, cfg Config) *Authenticator {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return a
}
```

Add focused failures:

```go
func TestNewRejectsDisallowedRemoteHost(t *testing.T) {
	_, err := New(Config{
		RemoteVerifyURL: "https://evil.example.com/check",
		AllowedHosts:    []string{"yk.xkt.com"},
	})
	if err == nil {
		t.Fatal("New accepted a remote verification host outside the allowlist")
	}
}

func TestNewRejectsConfiguredTenantsWithoutUsableToken(t *testing.T) {
	_, err := New(Config{
		TenantsConfigured: true,
		Tenants:           []TenantConfig{{Name: "broken"}},
	})
	if err == nil {
		t.Fatal("New accepted AUTH_TENANTS without a usable token")
	}
}

func TestTenantUserIDIsStoredAsPrincipal(t *testing.T) {
	a := mustAuthenticator(t, Config{Tenants: []TenantConfig{{
		Name: "tenant-a", Token: "secret", UserID: "user-a", AllowedTools: []string{"*"},
	}}})
	tenant := a.tenantsByToken[hashToken("secret")]
	if tenant == nil || tenant.Config.UserID != "user-a" {
		t.Fatalf("tenant principal = %#v, want user-a", tenant)
	}
}
```

- [ ] **Step 2: Run constructor tests and verify RED**

Run: `go test ./internal/auth -run 'TestNewRejects|TestTenantUserID' -v`

Expected: build failures for the new `New` signature and missing config fields.

- [ ] **Step 3: Implement validated construction**

Add `UserID`, `RemoteCacheMaxEntries`, and `TenantsConfigured`. In `New`:

```go
const defaultRemoteCacheMaxEntries = 4096

func New(cfg Config) (*Authenticator, error) {
	if cfg.PositiveTTL <= 0 {
		cfg.PositiveTTL = 5 * time.Minute
	}
	if cfg.NegativeTTL <= 0 {
		cfg.NegativeTTL = 30 * time.Second
	}
	if cfg.RemoteRateRPS <= 0 {
		cfg.RemoteRateRPS = 5
	}
	if cfg.RemoteRateBurst <= 0 {
		cfg.RemoteRateBurst = 10
	}
	if cfg.RemoteTimeout <= 0 {
		cfg.RemoteTimeout = 3 * time.Second
	}
	if cfg.RemoteCacheMaxEntries == 0 {
		cfg.RemoteCacheMaxEntries = defaultRemoteCacheMaxEntries
	}
	if cfg.RemoteCacheMaxEntries < 0 {
		return nil, fmt.Errorf("remote auth cache capacity must be positive")
	}
	if cfg.RemoteVerifyURL != "" && !hostAllowed(cfg.RemoteVerifyURL, cfg.AllowedHosts) {
		return nil, fmt.Errorf("remote verification URL is invalid or its host is not allowed")
	}

	a := &Authenticator{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.RemoteTimeout},
		cache:      newVerificationCache(cfg.RemoteCacheMaxEntries, time.Now),
		remoteOK:   cfg.RemoteVerifyURL != "",
		bucket:     float64(cfg.RemoteRateBurst),
		lastRef:    time.Now(),
		tenantsByToken: make(map[string]*Tenant),
	}
	for _, tc := range cfg.Tenants {
		var hashKey string
		switch {
		case strings.TrimSpace(tc.TokenHash) != "":
			hashKey = strings.ToLower(strings.TrimSpace(tc.TokenHash))
			decoded, err := hex.DecodeString(hashKey)
			if err != nil || len(decoded) != sha256.Size {
				continue
			}
		case tc.Token != "":
			hashKey = hashToken(tc.Token)
		default:
			continue
		}
		var limiter *tenantLimiter
		if tc.RateRPS > 0 {
			burst := tc.RateBurst
			if burst <= 0 {
				burst = 10
			}
			limiter = newTenantLimiter(burst)
		}
		stored := tc
		stored.Token = ""
		stored.UserID = strings.TrimSpace(stored.UserID)
		a.tenantsByToken[hashKey] = &Tenant{Config: stored, Limiter: limiter}
	}
	if cfg.TenantsConfigured && len(a.tenantsByToken) == 0 {
		return nil, fmt.Errorf("AUTH_TENANTS contains no usable token")
	}
	return a, nil
}
```

Keep zero as the programmatic “use default” value; the Server task will reject an explicitly configured environment value of zero. Change `Enabled()` to inspect constructed state:

```go
func (a *Authenticator) Enabled() bool {
	return a.cfg.LocalToken != "" || len(a.tenantsByToken) > 0 || a.remoteOK || len(a.cfg.AllowedCIDRs) > 0
}
```

Replace `sync.Map.Load/Store` in `verifyRemote` with `cache.Get/Set`.

- [ ] **Step 4: Run all Auth tests and verify GREEN**

Run: `go test ./internal/auth -v`

Expected: all existing and new Auth tests pass.

- [ ] **Step 5: Commit validated authentication construction**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go
git commit -m "fix: validate authentication configuration"
```

### Task 4: Enforce bounded fail-closed payload inspection and principal binding

**Files:**
- Modify: `internal/auth/auth.go:245-335,456-610`
- Modify: `internal/auth/auth_test.go`

**Interfaces:**
- Produces: `const maxMCPRequestBodyBytes int64 = 4 << 20`
- Produces: private `authenticationDecision{mode string, principal string, tenant *Tenant}`.
- Produces: private parsed JSON-RPC representation that preserves unknown top-level, `params`, and `arguments` fields using `map[string]json.RawMessage`.
- Removes: `extractToolName` after its callers are migrated.

- [ ] **Step 1: Write failing body-boundary and preservation tests**

Use a downstream Handler that reads the real Body:

```go
func TestTenantLargeToolPayloadReachesHandlerIntact(t *testing.T) {
	a := mustAuthenticator(t, Config{Tenants: []TenantConfig{{
		Name: "writer", Token: "secret", AllowedTools: []string{"wiki_upsert_page"},
	}}})
	content := strings.Repeat("文档内容", 20_000)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wiki_upsert_page","arguments":{"content":%q}}}`, content)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	var received string
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		received = string(data)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || received != body {
		t.Fatalf("status=%d body preserved=%t", rr.Code, received == body)
	}
}

func TestMCPPayloadOverLimitReturns413(t *testing.T) {
	a := mustAuthenticator(t, Config{LocalToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", int(maxMCPRequestBodyBytes)+1)))
	req.Header.Set("Authorization", "Bearer secret")
	called := false
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("status=%d called=%t, want 413 false", rr.Code, called)
	}
}
```

- [ ] **Step 2: Write failing ACL and principal-binding tests**

Add a table-driven test covering malformed tenant JSON, missing tool name, matching principal, absent principal injection, conflicting Body `userId`, conflicting URL routing ID, and a no-principal tenant preserving the exact Body. Use these concrete cases with a fresh Authenticator and downstream capture Handler for each case:

```go
func TestTenantPayloadAndPrincipalPolicy(t *testing.T) {
	tests := []struct {
		name, body, routedUser, principal string
		wantStatus                       int
		wantCalled                       bool
		wantUser                         string
	}{
		{"malformed", `{`, "", "user-a", http.StatusBadRequest, false, ""},
		{"missing tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`, "", "user-a", http.StatusUnauthorized, false, ""},
		{"inject principal", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wiki_search","arguments":{"query":"x"}}}`, "", "user-a", http.StatusOK, true, "user-a"},
		{"matching principal", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wiki_search","arguments":{"userId":"user-a"}}}`, "user-a", "user-a", http.StatusOK, true, "user-a"},
		{"body conflict", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wiki_search","arguments":{"userId":"user-b"}}}`, "", "user-a", http.StatusForbidden, false, ""},
		{"route conflict", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wiki_search","arguments":{}}}`, "user-b", "user-a", http.StatusForbidden, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := mustAuthenticator(t, Config{Tenants: []TenantConfig{{Name: "tenant-a", Token: "secret", UserID: tt.principal, AllowedTools: []string{"wiki_search"}}}})
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer secret")
			if tt.routedUser != "" {
				req = req.WithContext(trace.WithUserID(req.Context(), tt.routedUser))
			}
			called, gotUser := false, ""
			rr := httptest.NewRecorder()
			a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				var envelope struct { Params struct { Arguments struct { UserID string `json:"userId"` } `json:"arguments"` } `json:"params"` }
				_ = json.NewDecoder(r.Body).Decode(&envelope)
				gotUser = envelope.Params.Arguments.UserID
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rr, req)
			if rr.Code != tt.wantStatus || called != tt.wantCalled || gotUser != tt.wantUser {
				t.Fatalf("status=%d called=%t user=%q", rr.Code, called, gotUser)
			}
		})
	}
}
```

Add a separate no-principal tenant test using a Body with an unknown top-level field and assert byte-for-byte preservation at the downstream Handler.

- [ ] **Step 3: Run focused middleware tests and verify RED**

Run: `go test ./internal/auth -run 'TestTenantLarge|TestMCPPayload|TestTenant.*Payload|TestTenant.*Principal' -v`

Expected: the existing 64 KiB implementation truncates the large request; over-limit and conflict expectations fail.

- [ ] **Step 4: Implement one-read bounded payload handling**

At the start of `Middleware`, call this helper exactly once. It reads `maxMCPRequestBodyBytes+1` bytes, returns 413 when oversized, and immediately restores the exact bytes:

```go
const maxMCPRequestBodyBytes int64 = 4 << 20

func (a *Authenticator) readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost || r.Body == nil {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPRequestBodyBytes+1))
	if err != nil {
		a.reject(w, r, http.StatusBadRequest, fmt.Sprintf("read MCP payload: %v", err))
		return nil, false
	}
	if int64(len(body)) > maxMCPRequestBodyBytes {
		a.reject(w, r, http.StatusRequestEntityTooLarge, "MCP payload exceeds 4 MiB")
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}
```

Replace every successful authentication branch with `a.serveAuthenticated(w, r, next, body, decision)`: IP allowlist and local shared Token use an empty decision, tenant auth supplies both `tenant` and its configured `UserID`, and remote auth supplies its returned `userID` as `principal`.

Do not parse before authentication unless needed. Preserve objects and extension fields with these exact helpers:

```go
type authenticationDecision struct {
	mode      string
	principal string
	tenant    *Tenant
}

type inspectedRPC struct {
	method        string
	toolName      string
	requestedUser string
	envelope      map[string]json.RawMessage
	params        map[string]json.RawMessage
	arguments     map[string]json.RawMessage
}

func decodeObject(raw []byte, field string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("invalid JSON-RPC %s", field)
	}
	return object, nil
}

func decodeOptionalString(object map[string]json.RawMessage, key string) (string, error) {
	raw, ok := object[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("invalid JSON-RPC %s", key)
	}
	return strings.TrimSpace(value), nil
}

func inspectRPC(body []byte) (*inspectedRPC, error) {
	envelope, err := decodeObject(body, "envelope")
	if err != nil {
		return nil, err
	}
	method, err := decodeOptionalString(envelope, "method")
	if err != nil || method == "" {
		return nil, fmt.Errorf("invalid JSON-RPC method")
	}
	inspected := &inspectedRPC{method: method, envelope: envelope}
	if method != "tools/call" {
		return inspected, nil
	}
	params, err := decodeObject(envelope["params"], "params")
	if err != nil {
		return nil, err
	}
	inspected.params = params
	inspected.toolName, err = decodeOptionalString(params, "name")
	if err != nil {
		return nil, err
	}
	arguments := make(map[string]json.RawMessage)
	if raw, ok := params["arguments"]; ok {
		arguments, err = decodeObject(raw, "arguments")
		if err != nil {
			return nil, err
		}
	}
	inspected.arguments = arguments
	inspected.requestedUser, err = decodeOptionalString(arguments, "userId")
	return inspected, err
}

func (rpc *inspectedRPC) bindPrincipal(principal string) ([]byte, error) {
	userID, err := json.Marshal(principal)
	if err != nil {
		return nil, err
	}
	rpc.arguments["userId"] = userID
	arguments, err := json.Marshal(rpc.arguments)
	if err != nil {
		return nil, err
	}
	rpc.params["arguments"] = arguments
	params, err := json.Marshal(rpc.params)
	if err != nil {
		return nil, err
	}
	rpc.envelope["params"] = params
	return json.Marshal(rpc.envelope)
}

func (a *Authenticator) serveAuthenticated(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	body []byte,
	decision authenticationDecision,
) {
	if r.Method != http.MethodPost || len(body) == 0 {
		next.ServeHTTP(w, r)
		return
	}
	if decision.tenant == nil && decision.principal == "" {
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
		return
	}
	rpc, err := inspectRPC(body)
	if err != nil {
		a.reject(w, r, http.StatusBadRequest, "invalid MCP request payload")
		return
	}
	if decision.tenant != nil && rpc.method == "tools/call" {
		if rpc.toolName == "" || !isToolAllowed(rpc.toolName, decision.tenant.Config.AllowedTools) {
			a.deny(w, r, ClientIP(r), "tenant tool access denied")
			return
		}
	}
	if decision.principal != "" && rpc.method == "tools/call" {
		routed := strings.TrimSpace(trace.UserIDFromContext(r.Context()))
		if (routed != "" && routed != decision.principal) ||
			(rpc.requestedUser != "" && rpc.requestedUser != decision.principal) {
			a.reject(w, r, http.StatusForbidden, fmt.Sprintf(
				"authenticated userId conflict principal=%s routed=%s requested=%s",
				mask(decision.principal), mask(routed), mask(rpc.requestedUser),
			))
			return
		}
		body, err = rpc.bindPrincipal(decision.principal)
		if err != nil {
			a.reject(w, r, http.StatusBadRequest, "invalid MCP request payload")
			return
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	next.ServeHTTP(w, r)
}
```

The parsed representation must retain maps for the whole envelope, `params`, and `arguments`; marshal only when inserting the principal. This prevents dropping `id`, `_meta`, or future extension fields.

- [ ] **Step 5: Implement status-specific rejection without Token leakage**

Keep `deny` for 401. Add this helper for generic 400/403/413 responses. Callers must mask all identity values before placing them in `reason`; the conflict branch above does so, and the Authorization value is masked here:

```go
func (a *Authenticator) reject(w http.ResponseWriter, r *http.Request, status int, reason string) {
	logger.Errorf("[Auth] 请求拒绝: %s %s from %s, token=%s (%s)",
		r.Method, r.URL.Path, ClientIP(r), mask(r.Header.Get("Authorization")), reason)
	http.Error(w, http.StatusText(status), status)
}
```

- [ ] **Step 6: Run Auth tests and verify GREEN**

Run: `go test ./internal/auth -v`

Expected: all Auth tests pass, including exact large-Body preservation and fail-closed cases.

- [ ] **Step 7: Commit the HTTP boundary fix**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go
git commit -m "fix: bind principals with bounded MCP payload inspection"
```

### Task 5: Wire validated configuration and remove session identity state

**Files:**
- Modify: `cmd/server/main.go:29-101,226-272`
- Modify: `cmd/server/main_test.go`
- Modify: `internal/auth/auth.go:138-159,321-364`
- Modify: `internal/auth/auth_test.go`

**Interfaces:**
- Changes: `func buildAuthConfig(string) (auth.Config, error)`.
- Produces: `func envPositiveInt(string, int) (int, error)`.
- Removes: `Authenticator.sessionUserID`, `MCPMiddleware`, and `CleanSession`.

- [ ] **Step 1: Write failing environment parsing tests**

```go
func TestBuildAuthConfigParsesRemoteCacheCapacity(t *testing.T) {
	t.Setenv("AUTH_REMOTE_CACHE_MAX_ENTRIES", "123")
	cfg, err := buildAuthConfig("token")
	if err != nil || cfg.RemoteCacheMaxEntries != 123 {
		t.Fatalf("capacity=%d err=%v, want 123 nil", cfg.RemoteCacheMaxEntries, err)
	}
}

func TestBuildAuthConfigRejectsNonPositiveRemoteCacheCapacity(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AUTH_REMOTE_CACHE_MAX_ENTRIES", value)
			if _, err := buildAuthConfig("token"); err == nil {
				t.Fatalf("buildAuthConfig accepted %q", value)
			}
		})
	}
}

func TestBuildAuthConfigMarksTenantsConfigured(t *testing.T) {
	t.Setenv("AUTH_TENANTS", `[]`)
	cfg, err := buildAuthConfig("token")
	if err != nil || !cfg.TenantsConfigured {
		t.Fatalf("TenantsConfigured=%t err=%v, want true nil", cfg.TenantsConfigured, err)
	}
}
```

- [ ] **Step 2: Run focused Server config tests and verify RED**

Run: `go test ./cmd/server -run 'TestBuildAuthConfig' -v`

Expected: build failures because `buildAuthConfig` still returns one value and capacity fields do not exist.

- [ ] **Step 3: Implement error-returning config assembly**

Change CIDR and tenant parse failures from `os.Exit` inside `buildAuthConfig` to wrapped errors. Parse the capacity with:

```go
func envPositiveInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}
```

Set `TenantsConfigured` from the presence of a non-empty `AUTH_TENANTS` environment string.

- [ ] **Step 4: Update main construction and remove the obsolete session middleware**

Use explicit startup errors:

```go
authCfg, err := buildAuthConfig(localToken)
if err != nil {
	logger.Errorf("认证配置非法: %v", err)
	os.Exit(1)
}
authenticator, err := auth.New(authCfg)
if err != nil {
	logger.Errorf("认证器初始化失败: %v", err)
	os.Exit(1)
}
```

Delete `s.AddReceivingMiddleware(authenticator.MCPMiddleware())`. Then remove the session map, MCP import, `MCPMiddleware`, and `CleanSession` from Auth. Remove or replace tests that asserted session propagation; retain HTTP principal-binding coverage as the replacement behavior.

- [ ] **Step 5: Run Server and Auth tests and verify GREEN**

Run: `go test ./cmd/server ./internal/auth -v`

Expected: both packages pass and no session identity API remains referenced.

- [ ] **Step 6: Commit the Server wiring change**

```bash
git add cmd/server/main.go cmd/server/main_test.go internal/auth/auth.go internal/auth/auth_test.go
git commit -m "fix: fail startup on invalid authentication config"
```

### Task 6: Enforce identity resolution in the common tool wrapper

**Files:**
- Modify: `internal/server/register.go:91-132`
- Create: `internal/server/register_test.go`

**Interfaces:**
- Consumes: `trace.ResolveUserID` and `trace.WithAuthenticatedUserID` from Task 1.
- Produces: private generic `wrapToolHandler[In auditable](string, handler) handler` for focused testing.
- Changes: `addTool` delegates to `wrapToolHandler`.

- [ ] **Step 1: Write a failing wrapper conflict test**

Create a minimal test argument implementing `auditable`, and assert real Handler behavior rather than a mock call:

```go
type testAuditArgs struct{ UserID string }
func (a testAuditArgs) CorrelationID() string { return "test-trace" }
func (a testAuditArgs) Querier() string       { return a.UserID }
func (a testAuditArgs) AuditSubject() string  { return "subject" }

func TestWrapToolHandlerRejectsAuthenticatedUserConflict(t *testing.T) {
	called := false
	handler := wrapToolHandler("test_tool", func(context.Context, *mcp.CallToolRequest, testAuditArgs) (*mcp.CallToolResult, any, error) {
		called = true
		return &mcp.CallToolResult{}, nil, nil
	})
	ctx := trace.WithAuthenticatedUserID(context.Background(), "trusted-user")
	result, _, err := handler(ctx, nil, testAuditArgs{UserID: "other-user"})
	if err != nil || result == nil || !result.IsError || called {
		t.Fatalf("result=%#v err=%v called=%t; want MCP error, nil, false", result, err, called)
	}
}
```

- [ ] **Step 2: Write a failing resolved-identity Handler test**

Have the real wrapped Handler return `trace.EffectiveUserID(ctx, in.UserID)` as structured output. With a matching trusted principal, assert the literal returned identity is `trusted-user`:

```go
func TestWrapToolHandlerUsesResolvedIdentity(t *testing.T) {
	handler := wrapToolHandler("test_tool", func(ctx context.Context, _ *mcp.CallToolRequest, in testAuditArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, trace.EffectiveUserID(ctx, in.UserID), nil
	})
	ctx := trace.WithAuthenticatedUserID(context.Background(), "trusted-user")
	result, out, err := handler(ctx, nil, testAuditArgs{UserID: "trusted-user"})
	if err != nil || result == nil || result.IsError || out != "trusted-user" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}
```

- [ ] **Step 3: Run Server tests and verify RED**

Run: `go test ./internal/server -run TestWrapToolHandler -v`

Expected: build failure because `wrapToolHandler` does not exist.

- [ ] **Step 4: Extract and implement the instrumented wrapper**

Define the shared Handler type, then extract the existing instrumentation into this exact wrapper. It resolves identity before invoking business logic, skips the Handler on conflict, and records both conflict and success through the same metric/audit path:

```go
type toolHandler[In any] func(
	context.Context,
	*mcp.CallToolRequest,
	In,
) (*mcp.CallToolResult, any, error)

func wrapToolHandler[In auditable](name string, h toolHandler[In]) toolHandler[In] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		ctx, _ = trace.EnsureID(ctx, in.CorrelationID())
		querier, identityErr := trace.ResolveUserID(ctx, in.Querier())
		start := time.Now()
		var res *mcp.CallToolResult
		var out any
		var err error
		if identityErr != nil {
			querier = trace.AuthenticatedUserIDFromContext(ctx)
			res = &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "authenticated user identity conflict"}},
				IsError: true,
			}
		} else {
			res, out, err = h(ctx, req, in)
		}
		elapsed := time.Since(start)
		status := metrics.StatusOK
		if err != nil || (res != nil && res.IsError) {
			status = metrics.StatusError
		}
		metrics.ObserveToolCall(name, status, elapsed)
		logger.AuditCtx(ctx, map[string]any{
			"tool": name, "querier": querier,
			"subject": pii.MaskSubject(in.AuditSubject()),
			"status": status, "latency_ms": elapsed.Milliseconds(),
		})
		logger.ToolfCtx(ctx, name, "调用完成 status=%s latency=%dms", status, elapsed.Milliseconds())
		return res, out, err
	}
}

func addTool[In auditable](s *mcp.Server, tool *mcp.Tool, h toolHandler[In]) {
	mcp.AddTool(s, tool, wrapToolHandler(tool.Name, h))
}
```

- [ ] **Step 5: Run Server and affected Tool tests and verify GREEN**

Run: `go test ./internal/server ./internal/tools ./internal/trace -v`

Expected: all packages pass.

- [ ] **Step 6: Commit the defense-in-depth wrapper**

```bash
git add internal/server/register_test.go
git add -p internal/server/register.go
git diff --cached --check
git diff --cached -- internal/server/register.go internal/server/register_test.go
git commit -m "fix: reject tool identity conflicts centrally"
```

For `git add -p`, stage only the `toolHandler`/`wrapToolHandler`/`addTool` instrumentation hunk. Reject the pre-existing Wiki backend hunk. Inspect the cached diff before committing.

### Task 7: Document configuration and perform full verification

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-30-auth-boundary-hardening-design.md` only if implementation reveals an approved-design discrepancy.
- Modify: `docs/superpowers/specs/2026-08-30-auth-boundary-hardening-design.zh-CN.md` in lockstep with any English spec correction.

**Interfaces:**
- Documents: `AUTH_TENANTS[].user_id`.
- Documents: `AUTH_REMOTE_CACHE_MAX_ENTRIES` default 4096.
- Documents: 4 MiB MCP POST limit and HTTP 413 behavior.
- Documents: trusted-principal versus routing-only `userId` semantics.

- [ ] **Step 1: Update README with exact runtime behavior**

Add a concise authentication section with this configuration shape:

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

State explicitly that shared Token/IP/stdio `userId` is routing metadata, while remote `userid` and tenant `user_id` are authoritative and conflicts return 403. Document the 4 MiB request limit and cache capacity variable.

- [ ] **Step 2: Format all changed Go files**

Run:

```bash
gofmt -w internal/trace/trace.go internal/trace/trace_test.go \
  internal/auth/auth.go internal/auth/auth_test.go internal/auth/cache.go internal/auth/cache_test.go \
  internal/server/register.go internal/server/register_test.go cmd/server/main.go cmd/server/main_test.go
```

Expected: command exits 0.

- [ ] **Step 3: Run focused verification**

Run: `go test ./internal/trace ./internal/auth ./internal/server ./cmd/server -v`

Expected: all focused packages pass.

- [ ] **Step 4: Run the full repository test suite**

Run: `go test ./...`

Expected: all packages pass.

- [ ] **Step 5: Run the race detector**

Run: `go test -race ./...`

Expected: all packages pass with no race reports.

- [ ] **Step 6: Run static analysis and build checks**

Run:

```bash
go vet ./...
go build ./cmd/server
git diff --check
```

Expected: all commands exit 0 and `git diff --check` produces no output.

- [ ] **Step 7: Verify scope and remove generated artifacts from the change set**

Run: `git status --short`

Expected: task changes are limited to the files named in this plan; pre-existing Wiki, report, and compressed-log changes remain untouched and unstaged. An ignored `server` build binary may exist locally but must not be staged.

- [ ] **Step 8: Commit documentation and final integration**

```bash
git add -p README.md
git diff --cached --check
git diff --cached -- README.md
git commit -m "docs: document trusted authentication principals"
```

Stage only the new authentication documentation hunk and reject every pre-existing README hunk. If Git cannot separate the hunks cleanly, leave README unstaged and report that documentation update in the handoff instead of mixing user work into the commit.

Record the exact test, race, vet, and build commands in the final handoff.
