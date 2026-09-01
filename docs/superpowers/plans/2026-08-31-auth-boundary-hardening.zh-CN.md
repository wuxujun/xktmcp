# 认证边界加固实施计划

[English](2026-08-31-auth-boundary-hardening.md)

> **供智能体执行者使用：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans` 子技能逐项执行本计划。所有步骤使用复选框（`- [ ]`）跟踪。

**目标：** 将可信认证主体绑定到 MCP 工具调用，对 Payload 实施有界且 fail-closed 的检查，限制远程认证缓存内存，并拒绝无效认证配置，同时保持共享 Token 路由兼容性。

**架构：** HTTP 认证生成可选可信主体，并在 MCP SDK 分离请求 Context 前把主体绑定到 `tools/call` 参数。Trace 包独立解析可信身份与路由身份，形成纵深防御；Auth 包拥有有界 TTL/LRU 验证缓存，并在构造阶段校验配置。

**技术栈：** Go 1.25、`net/http`、`encoding/json`、`container/list`、MCP Go SDK v1.7.0、Go `testing` 与 `httptest`。

**设计文档：** `docs/superpowers/specs/2026-08-30-auth-boundary-hardening-design.zh-CN.md`

## 全局约束

- 保持共享 `AUTH_TOKEN`、IP 白名单和 stdio 的仅路由 `userId` 行为向后兼容。
- 只有远程验证返回的 `userid` 和 `AUTH_TENANTS.user_id` 是可信主体。
- 拒绝可信主体冲突；信封或 URL `userId` 绝不能覆盖可信主体。
- 需要检查的 MCP POST Body 上限严格为 4 MiB（`4 << 20` 字节）。
- 租户 ACL 解析必须 fail-closed；JSON-RPC 批量请求支持不在本阶段范围内。
- 远程验证缓存默认 4096 条，正缓存 TTL 为 5 分钟，负缓存 TTL 为 30 秒。
- 不增加第三方依赖。
- 各任务提交不得包含用户现有的 README、Wiki 或日志工作区改动。
- 严格遵循红—绿—重构：聚焦的失败测试之前不得修改生产代码。

---

### 任务 1：分离可信主体与路由身份

**文件：**
- 修改：`internal/trace/trace.go:12-79`
- 测试：`internal/trace/trace_test.go:61-73`

**接口：**
- 产出：`var ErrUserIDConflict error`
- 产出：`func WithAuthenticatedUserID(context.Context, string) context.Context`
- 产出：`func AuthenticatedUserIDFromContext(context.Context) string`
- 产出：`func ResolveUserID(context.Context, string) (string, error)`
- 修改：`func EffectiveUserID(context.Context, string) string`，改为优先返回可信主体。

- [ ] **步骤 1：用可信主体测试替换原优先级测试**

加入使用字面量期望值的测试：

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

测试文件增加 `errors` import。

- [ ] **步骤 2：运行聚焦测试并确认 RED**

运行：`go test ./internal/trace -run 'TestResolveUserID|TestEffectiveUserIDPrefersAuthenticatedPrincipal' -v`

预期：由于可信主体 API 和 `ErrUserIDConflict` 尚不存在而编译失败。

- [ ] **步骤 3：实现最小可信主体 Context API**

新增独立私有 Context Key 和冲突校验：

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

- [ ] **步骤 4：运行 Trace 全部测试并确认 GREEN**

运行：`go test ./internal/trace -v`

预期：Trace 包全部测试通过。

- [ ] **步骤 5：独立提交身份原语**

```bash
git add internal/trace/trace.go internal/trace/trace_test.go
git commit -m "fix: separate authenticated and routing identities"
```

### 任务 2：增加有界 TTL/LRU 验证缓存

**文件：**
- 新建：`internal/auth/cache.go`
- 新建：`internal/auth/cache_test.go`

**接口：**
- 使用：`internal/auth/auth.go` 中现有 `cacheEntry`。
- 产出：`func newVerificationCache(maxEntries int, now func() time.Time) *verificationCache`
- 产出：`func (*verificationCache) Get(string) (cacheEntry, bool)`
- 产出：`func (*verificationCache) Set(string, cacheEntry)`
- 产出：`func (*verificationCache) Len() int`

- [ ] **步骤 1：编写缓存过期与 LRU 淘汰失败测试**

使用可注入时钟，分别证明过期条目在 `Get` 后被物理删除，以及容量为 2 时访问 `a` 后插入 `c` 会淘汰 `b`：

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

- [ ] **步骤 2：运行新测试并确认 RED**

运行：`go test ./internal/auth -run TestVerificationCache -v`

预期：`newVerificationCache` 不存在导致编译失败。

- [ ] **步骤 3：实现最小并发 LRU 缓存**

使用 `sync.Mutex`、`map[string]*list.Element` 和 `container/list`；链表头表示最近使用：

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

经过校验的运行配置不得进入构造器的 panic 分支。

- [ ] **步骤 4：运行缓存测试并确认 GREEN**

运行：`go test ./internal/auth -run TestVerificationCache -v`

预期：新增测试全部通过。

- [ ] **步骤 5：独立提交缓存**

```bash
git add internal/auth/cache.go internal/auth/cache_test.go
git commit -m "fix: bound remote authentication cache"
```

### 任务 3：校验 Authenticator 构造并支持租户主体

**文件：**
- 修改：`internal/auth/auth.go:32-228,462-494`
- 修改：`internal/auth/auth_test.go:16-443`

**接口：**
- 使用任务 2 的 `newVerificationCache`。
- 新增 `TenantConfig.UserID string`，JSON Key 为 `user_id`。
- 新增 `Config.RemoteCacheMaxEntries int` 和 `Config.TenantsConfigured bool`。
- `New` 改为 `func New(Config) (*Authenticator, error)`。
- `Authenticator.cache` 从 `sync.Map` 改为 `*verificationCache`。

- [ ] **步骤 1：增加 `mustAuthenticator` 测试 Helper 和构造失败测试**

增加 Helper，并机械替换现有成功构造调用：

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

新增构造失败与租户主体测试：

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

- [ ] **步骤 2：运行构造测试并确认 RED**

运行：`go test ./internal/auth -run 'TestNewRejects|TestTenantUserID' -v`

预期：`New` 新签名和配置字段不存在导致编译失败。

- [ ] **步骤 3：实现经过校验的构造逻辑**

增加 `UserID`、`RemoteCacheMaxEntries` 和 `TenantsConfigured`。`New` 使用以下构造骨架：

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

程序化配置为 0 时使用默认值；Server 任务负责拒绝显式环境变量值 0。`Enabled` 根据构造后的状态判断：

```go
func (a *Authenticator) Enabled() bool {
	return a.cfg.LocalToken != "" || len(a.tenantsByToken) > 0 || a.remoteOK || len(a.cfg.AllowedCIDRs) > 0
}
```

`verifyRemote` 把 `sync.Map.Load/Store` 替换为任务 2 的 `cache.Get/Set`。

- [ ] **步骤 4：运行 Auth 全部测试并确认 GREEN**

运行：`go test ./internal/auth -v`

预期：现有及新增 Auth 测试全部通过。

- [ ] **步骤 5：提交认证构造校验**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go
git commit -m "fix: validate authentication configuration"
```

### 任务 4：实施有界 fail-closed Payload 检查和主体绑定

**文件：**
- 修改：`internal/auth/auth.go:245-335,456-610`
- 修改：`internal/auth/auth_test.go`

**接口：**
- 新增：`const maxMCPRequestBodyBytes int64 = 4 << 20`
- 新增私有 `authenticationDecision{mode string, principal string, tenant *Tenant}`。
- 新增能够通过 `map[string]json.RawMessage` 保留顶层、`params` 和 `arguments` 未知字段的私有 JSON-RPC 表示。
- 调用迁移后删除 `extractToolName`。

- [ ] **步骤 1：编写大 Body 完整传递和 413 失败测试**

使用真实下游 Handler 读取 Body：

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

100 KiB Wiki 风格 Payload 必须字节一致地到达 Handler；超过 `4 << 20` 一个字节必须返回 413 且 Handler 不得执行。

- [ ] **步骤 2：编写 ACL 和主体绑定失败测试**

使用表驱动测试覆盖非法租户 JSON、缺少工具名、可信主体注入、相同身份、Body 身份冲突和 URL 身份冲突。每个 Case 都创建全新的 Authenticator 和下游捕获 Handler：

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

另加一个无 principal 租户测试，在 Body 中放入未知顶层字段，并在下游 Handler 断言字节完全一致。

- [ ] **步骤 3：运行中间件聚焦测试并确认 RED**

运行：`go test ./internal/auth -run 'TestTenantLarge|TestMCPPayload|TestTenant.*Payload|TestTenant.*Principal' -v`

预期：现有 64 KiB 实现会截断大请求，超限和冲突断言失败。

- [ ] **步骤 4：实现一次读取的有界 Payload 处理**

在 `Middleware` 开始处只调用一次以下 Helper。POST Body 最多读取 `maxMCPRequestBodyBytes+1`；超限返回 413，否则立即恢复完整 Body：

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

每个认证成功分支都改为调用 `a.serveAuthenticated(w, r, next, body, decision)`：IP 白名单和本地共享 Token 使用空决策；租户认证传入 `tenant` 及其 `UserID`；远程认证把返回的 `userID` 作为 `principal`。认证决策产生后，仅在租户 ACL 或可信主体绑定需要时解析。通过保存完整信封、`params` 和 `arguments` RawMessage Map 来保留扩展字段；只有注入主体时才重新序列化。

使用以下精确私有类型和 Helper：

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
```

认证后的转发逻辑如下：

```go
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

- [ ] **步骤 5：实现不会泄漏 Token 的分状态拒绝**

401 继续使用现有 `deny`。400、403、413 使用以下 Helper；调用方写入 `reason` 前必须对全部身份字段脱敏，上述冲突分支已执行该约束，Authorization 也在 Helper 中脱敏：

```go
func (a *Authenticator) reject(w http.ResponseWriter, r *http.Request, status int, reason string) {
	logger.Errorf("[Auth] 请求拒绝: %s %s from %s, token=%s (%s)",
		r.Method, r.URL.Path, ClientIP(r), mask(r.Header.Get("Authorization")), reason)
	http.Error(w, http.StatusText(status), status)
}
```

- [ ] **步骤 6：运行 Auth 全部测试并确认 GREEN**

运行：`go test ./internal/auth -v`

预期：包括大 Body 字节一致和 fail-closed 场景在内的所有测试通过。

- [ ] **步骤 7：提交 HTTP 边界修复**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go
git commit -m "fix: bind principals with bounded MCP payload inspection"
```

### 任务 5：接入经过校验的配置并移除会话身份状态

**文件：**
- 修改：`cmd/server/main.go:29-101,226-272`
- 修改：`cmd/server/main_test.go`
- 修改：`internal/auth/auth.go:138-159,321-364`
- 修改：`internal/auth/auth_test.go`

**接口：**
- `buildAuthConfig` 改为 `func buildAuthConfig(string) (auth.Config, error)`。
- 新增 `func envPositiveInt(string, int) (int, error)`。
- 删除 `Authenticator.sessionUserID`、`MCPMiddleware` 和 `CleanSession`。

- [ ] **步骤 1：编写环境变量解析失败测试**

覆盖容量 `123` 正常解析，`0`、`-1`、`not-a-number` 返回错误，以及存在 `AUTH_TENANTS=[]` 时 `TenantsConfigured=true`：

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

- [ ] **步骤 2：运行 Server 配置聚焦测试并确认 RED**

运行：`go test ./cmd/server -run 'TestBuildAuthConfig' -v`

预期：`buildAuthConfig` 仍返回单值且容量字段不存在，导致编译失败。

- [ ] **步骤 3：实现返回错误的配置装配**

把 CIDR 和租户解析中的 `os.Exit` 改为带上下文的错误。实现：

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

默认容量为 4096，显式非正数或非整数返回错误；根据非空 `AUTH_TENANTS` 原始值设置 `TenantsConfigured`。

- [ ] **步骤 4：更新 main 构造并删除过时 Session 中间件**

`main` 分别处理 `buildAuthConfig` 和 `auth.New` 错误并拒绝启动：

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

删除 `s.AddReceivingMiddleware(authenticator.MCPMiddleware())`，再从 Auth 删除 Session Map、MCP import、`MCPMiddleware` 和 `CleanSession`。用 HTTP 主体绑定测试替换旧 Session 传播测试。

- [ ] **步骤 5：运行 Server 与 Auth 测试并确认 GREEN**

运行：`go test ./cmd/server ./internal/auth -v`

预期：两个包全部通过，代码中不再引用 Session 身份 API。

- [ ] **步骤 6：提交 Server 接线变更**

```bash
git add cmd/server/main.go cmd/server/main_test.go internal/auth/auth.go internal/auth/auth_test.go
git commit -m "fix: fail startup on invalid authentication config"
```

### 任务 6：在通用工具包装器中强制身份解析

**文件：**
- 修改：`internal/server/register.go:91-132`
- 新建：`internal/server/register_test.go`

**接口：**
- 使用任务 1 的 `trace.ResolveUserID` 和 `trace.WithAuthenticatedUserID`。
- 新增私有泛型 `wrapToolHandler[In auditable]` 供聚焦测试。
- `addTool` 改为委托 `wrapToolHandler`。

- [ ] **步骤 1：编写包装器身份冲突失败测试**

定义实现 `auditable` 的最小测试参数，使用真实闭包设置 `called`：

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

可信主体为 `trusted-user`、参数为 `other-user` 时，必须返回 `IsError=true`、Go error 为 nil 且业务 Handler 未执行。

- [ ] **步骤 2：编写解析后身份传给 Handler 的失败测试**

真实包装 Handler 返回 `trace.EffectiveUserID(ctx, in.UserID)` 作为结构化结果；可信主体与参数一致时，断言字面量结果为 `trusted-user`：

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

- [ ] **步骤 3：运行 Server 测试并确认 RED**

运行：`go test ./internal/server -run TestWrapToolHandler -v`

预期：`wrapToolHandler` 不存在导致编译失败。

- [ ] **步骤 4：提取并实现统一埋点包装器**

定义共享 Handler 类型，并把现有埋点提取为以下精确包装器。它在调用业务逻辑前解析身份，冲突时跳过 Handler，并让成功与冲突经过同一套指标和审计路径：

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

- [ ] **步骤 5：运行 Server、Tools 和 Trace 测试并确认 GREEN**

运行：`go test ./internal/server ./internal/tools ./internal/trace -v`

预期：全部通过。

- [ ] **步骤 6：提交纵深防御包装器**

```bash
git add internal/server/register_test.go
git add -p internal/server/register.go
git diff --cached --check
git diff --cached -- internal/server/register.go internal/server/register_test.go
git commit -m "fix: reject tool identity conflicts centrally"
```

执行 `git add -p` 时，只暂存 `toolHandler`、`wrapToolHandler` 和 `addTool` 的埋点 Hunk，拒绝已有 Wiki 后端 Hunk；提交前必须检查缓存区 Diff。

### 任务 7：记录配置并执行完整验证

**文件：**
- 修改：`README.md`
- 仅当实现发现批准设计存在偏差时，同步修改中英文设计文档。

**接口：**
- 记录 `AUTH_TENANTS[].user_id`。
- 记录 `AUTH_REMOTE_CACHE_MAX_ENTRIES` 默认 4096。
- 记录 4 MiB MCP POST 上限和 HTTP 413。
- 记录可信主体与仅路由 `userId` 语义。

- [ ] **步骤 1：使用确切运行行为更新 README**

加入租户 JSON 示例：

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

明确共享 Token/IP/stdio 的 `userId` 仅是路由元数据，远程 `userid` 和租户 `user_id` 是权威身份，冲突返回 403；记录请求上限和缓存容量变量。

- [ ] **步骤 2：格式化所有变更的 Go 文件**

运行：

```bash
gofmt -w internal/trace/trace.go internal/trace/trace_test.go \
  internal/auth/auth.go internal/auth/auth_test.go internal/auth/cache.go internal/auth/cache_test.go \
  internal/server/register.go internal/server/register_test.go cmd/server/main.go cmd/server/main_test.go
```

预期：命令退出码为 0。

- [ ] **步骤 3：运行聚焦验证**

运行：`go test ./internal/trace ./internal/auth ./internal/server ./cmd/server -v`

预期：所有聚焦包通过。

- [ ] **步骤 4：运行全仓库测试**

运行：`go test ./...`

预期：所有包通过。

- [ ] **步骤 5：运行竞态检测**

运行：`go test -race ./...`

预期：所有包通过且没有竞态报告。

- [ ] **步骤 6：运行静态检查和构建**

```bash
go vet ./...
go build ./cmd/server
git diff --check
```

预期：全部退出码为 0，`git diff --check` 无输出。

- [ ] **步骤 7：检查范围并排除生成物**

运行：`git status --short`

预期：任务变更仅限本计划列出的文件；已有 Wiki、报告和压缩日志改动保持未触碰、未暂存。忽略的 `server` 构建二进制不得暂存。

- [ ] **步骤 8：提交文档和最终集成**

```bash
git add -p README.md
git diff --cached --check
git diff --cached -- README.md
git commit -m "docs: document trusted authentication principals"
```

只暂存新增认证文档 Hunk，拒绝全部既有 README Hunk。如果 Git 无法干净拆分，README 保持未暂存，并在交付中说明文档已更新，不得把用户改动混入提交。

最终交付必须记录实际执行的测试、竞态、vet 和构建命令。
