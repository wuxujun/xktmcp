// Package auth 提供 HTTP 传输(http/sse)下的 Bearer Token 认证中间件。
//
// 安全设计要点:
//   - 令牌只从 Authorization 头读取,不再支持 URL ?token=(避免凭证落入日志/代理/浏览器历史)。
//   - 本地令牌比对使用 crypto/subtle 常量时间比较,防计时侧信道爆破。
//   - 日志只打印掩码,绝不记录原始令牌。
//   - 远程兜底验证带:正/负结果缓存(TTL)、目标主机白名单(防 SSRF)、全局限流(防放大)。
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/trace"
)

// TenantConfig 定义多租户配置结构
//
// 安全说明: 强烈建议使用 token_hash 替代 token 字段:
//   - token: 明文令牌(向后兼容保留,启动时会被立即哈希后丢弃明文)。
//   - token_hash: 预先计算好的 SHA-256 哈希(hex 字符串),推荐方式。
//     生成方法: echo -n 'your-token' | sha256sum
//
// 运行时 tenantsByToken map 仅以哈希值为 key,内存中不保留明文令牌。
type TenantConfig struct {
	Name         string   `json:"name"`
	Token        string   `json:"token"`         // 明文令牌(向后兼容,启动后立即哈希存储)
	TokenHash    string   `json:"token_hash"`    // 预计算 SHA-256 哈希(推荐,优先使用)
	UserID       string   `json:"user_id"`       // 可选的租户主体标识
	AllowedTools []string `json:"allowed_tools"` // 允许调用的工具列表，"*" 表示允许所有
	RateRPS      float64  `json:"rate_rps"`      // 租户专属限流速率 (每秒请求数)
	RateBurst    int      `json:"rate_burst"`    // 租户专属限流突发容量
}

// Config 来自环境/命令行的认证配置。
type Config struct {
	// LocalToken 是静态本地令牌;为空表示不启用本地比对。
	LocalToken string
	// Tenants 存储多租户配置。
	Tenants []TenantConfig
	// TenantsConfigured 表示 AUTH_TENANTS 已被显式配置，即使解析后没有租户也应报错。
	TenantsConfigured bool
	// RemoteVerifyURL 是远程验证端点(完整 URL,如 https://yk.xkt.com/api/auth/check);
	// 为空表示不启用远程兜底。
	RemoteVerifyURL string
	// AllowedHosts 是远程验证允许访问的主机白名单(host[:port])。
	// RemoteVerifyURL 的 host 必须在其中,否则远程兜底被禁用(防 SSRF)。
	AllowedHosts []string

	// AllowedCIDRs 是受信任的来源网段(IP 白名单)。命中任一网段的请求【直接放行】,
	// 无需 Bearer 令牌——即「IP 验证通过即可忽略 Authorization 认证」。为空表示不启用。
	AllowedCIDRs []*net.IPNet
	// TrustForwardedHeader 决定【安全决策所用】来源 IP 的取值方式:
	//   false(默认,安全):只认 TCP 连接 of RemoteAddr,杜绝伪造 X-Forwarded-For/
	//                       X-Real-IP 头来冒充可信网段从而绕过认证。
	//   true:信任 X-Forwarded-For(首个)→ X-Real-IP → RemoteAddr。
	//        【仅当】服务部署在会重写/剥离该头的可信反向代理之后才可开启,
	//        否则任意客户端都能伪造来源 IP 绕过 Bearer 认证。
	TrustForwardedHeader bool

	// 以下均有合理默认值。
	PositiveTTL     time.Duration // 远程验证通过结果的缓存时长
	NegativeTTL     time.Duration // 远程验证失败结果的缓存时长
	RemoteRateRPS   float64       // 远程验证每秒最大请求数(令牌桶速率)
	RemoteRateBurst int           // 令牌桶突发容量
	RemoteTimeout   time.Duration // 单次远程验证 HTTP 超时
	// RemoteCacheMaxEntries 是远程验证缓存最大条目数；零值使用默认值。
	RemoteCacheMaxEntries int
}

// Enabled 报告是否配置了任意一种认证方式(本地令牌 / 多租户 / 远程兜底 / IP 白名单)。
func (c Config) Enabled() bool {
	return c.LocalToken != "" || len(c.Tenants) > 0 || c.RemoteVerifyURL != "" || len(c.AllowedCIDRs) > 0
}

type cacheEntry struct {
	ok     bool
	exp    time.Time
	userID string // 远程验证返回的用户 ID（可为空）
}

// ctxKey 是注入 context 的私有 key 类型，防止与其他包冲突。
type ctxKey int

const ctxKeyUserID ctxKey = iota

const maxMCPRequestBodyBytes int64 = 4 << 20

// UserIDFromCtx 从 context 中取出远程验证返回的用户 ID；未设置时返回空字符串。
func UserIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUserID).(string)
	return v
}

type tenantLimiter struct {
	mu      sync.Mutex
	bucket  float64
	lastRef time.Time
}

func newTenantLimiter(burst int) *tenantLimiter {
	return &tenantLimiter{
		bucket:  float64(burst),
		lastRef: time.Now(),
	}
}

func (tl *tenantLimiter) Allow(rps float64, burst int) bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tl.lastRef).Seconds()
	tl.lastRef = now
	tl.bucket += elapsed * rps
	if maxBurst := float64(burst); tl.bucket > maxBurst {
		tl.bucket = maxBurst
	}
	if tl.bucket >= 1 {
		tl.bucket--
		return true
	}
	return false
}

type Tenant struct {
	Config  TenantConfig
	Limiter *tenantLimiter
}

// Authenticator 是可复用的认证器(并发安全)。
type Authenticator struct {
	cfg        Config
	httpClient *http.Client
	remoteOK   bool // RemoteVerifyURL 通过白名单校验,远程兜底可用

	limiterMu sync.Mutex
	bucket    float64   // 当前令牌桶余量
	lastRef   time.Time // 上次补充时间

	cache *verificationCache // 缓存校验结果 (key: sha256_hash_string)

	// sessionUserID 把 MCP sessionID 映射到远程验证返回的 userID。
	// MCP SDK 会 detach HTTP request context，HTTP 中间件注入的 context value
	// 无法传递到 tool handler；通过此 map + MCPMiddleware 在 MCP 消息层面补注。
	sessionUserID sync.Map // key: sessionID(string) → value: userID(string)

	tenantsByToken map[string]*Tenant
}

// Enabled 报告该认证器是否启用了任意一种认证方式。
func (a *Authenticator) Enabled() bool {
	return a.cfg.LocalToken != "" || len(a.tenantsByToken) > 0 || a.remoteOK || len(a.cfg.AllowedCIDRs) > 0
}

// New 构造 Authenticator,并对远程验证 URL 做白名单校验。
func New(cfg Config) (*Authenticator, error) {
	const defaultRemoteCacheMaxEntries = 4096

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
		cfg:            cfg,
		httpClient:     &http.Client{Timeout: cfg.RemoteTimeout},
		cache:          newVerificationCache(cfg.RemoteCacheMaxEntries, time.Now),
		remoteOK:       cfg.RemoteVerifyURL != "",
		bucket:         float64(cfg.RemoteRateBurst),
		lastRef:        time.Now(),
		tenantsByToken: make(map[string]*Tenant),
	}

	// 初始化租户映射与限流器。
	// map key 统一为 SHA-256 哈希,保证内存中不存明文令牌。
	// 优先使用 TokenHash(预计算哈希);若未配置则对 Token 明文即时哈希后丢弃明文。
	for _, tc := range cfg.Tenants {
		// 确定 map key(哈希值)
		var hashKey string
		switch {
		case strings.TrimSpace(tc.TokenHash) != "":
			// 推荐路径:配置侧已预计算哈希,直接使用,明文令牌从未出现在进程内。
			hashKey = strings.ToLower(strings.TrimSpace(tc.TokenHash))
			decoded, err := hex.DecodeString(hashKey)
			if err != nil || len(decoded) != sha256.Size {
				continue
			}
		case strings.TrimSpace(tc.Token) != "":
			// 兼容路径:对明文令牌哈希后存储;完成后丢弃明文引用。
			hashKey = hashToken(strings.TrimSpace(tc.Token))
		default:
			continue // 两者均未配置,跳过
		}
		if hashKey == "" {
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
		// 存入 map 前清除明文令牌字段,进一步减少内存中明文的生命周期。
		tcStored := tc
		tcStored.Token = ""
		tcStored.UserID = strings.TrimSpace(tcStored.UserID)
		a.tenantsByToken[hashKey] = &Tenant{
			Config:  tcStored,
			Limiter: limiter,
		}
		logger.Infof("[Auth] 租户 %s 已注册(哈希存储)", tc.Name)
	}

	if cfg.TenantsConfigured && len(a.tenantsByToken) == 0 {
		return nil, fmt.Errorf("AUTH_TENANTS contains no usable token")
	}

	return a, nil
}

// hostAllowed 校验 rawURL 的 scheme 为 http(s) 且 host 命中白名单。
func hostAllowed(rawURL string, allowed []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	for _, h := range allowed {
		if strings.EqualFold(strings.TrimSpace(h), u.Host) {
			return true
		}
	}
	return false
}

// Middleware 返回包裹 next 的认证中间件。
// 认证通过且携带 userID 时，同时把 sessionID→userID 存入内部 map，
// 供 MCPMiddleware 在 MCP 消息层面补注（SDK 会 detach HTTP context，
// 直接在 HTTP 层注入的 context value 无法到达 tool handler）。
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := a.readRequestBody(w, r)
		if !ok {
			return
		}
		ip := ClientIP(r)

		// 0) 受信任网段直接放行(IP 白名单),无需 Bearer 令牌。
		//    安全决策的 IP 取值由 TrustForwardedHeader 决定:默认仅信任 TCP 连接的
		//    RemoteAddr,杜绝伪造转发头绕过;仅当部署在可信代理后才信任 X-Forwarded-For。
		if len(a.cfg.AllowedCIDRs) > 0 {
			if srcIP := a.securityClientIP(r); srcIP != nil && a.ipAllowed(srcIP) {
				a.serveAuthenticated(w, r, next, body, authenticationDecision{})
				return
			}
		}

		token := bearerFromHeader(r)
		if token == "" {
			a.deny(w, r, ip, "缺少 Bearer 令牌")
			return
		}

		// 1) 多租户鉴权与细粒度流控、ACL 检查。
		// 对请求令牌哈希后查表(map key 为哈希,无需对比明文)。
		if len(a.tenantsByToken) > 0 {
			if tenant, ok := a.tenantsByToken[hashToken(token)]; ok {
				// 租户级流控
				if tenant.Limiter != nil && tenant.Config.RateRPS > 0 {
					burst := tenant.Config.RateBurst
					if burst <= 0 {
						burst = 10
					}
					if !tenant.Limiter.Allow(tenant.Config.RateRPS, burst) {
						a.deny(w, r, ip, fmt.Sprintf("租户 %s 触发限流", tenant.Config.Name))
						return
					}
				}

				a.serveAuthenticated(w, r, next, body, authenticationDecision{
					mode:      "tenant",
					principal: tenant.Config.UserID,
					tenant:    tenant,
				})
				return
			}
		}

		// 2) 本地常量时间比对 (全局静态 Token 兜底)。
		if a.cfg.LocalToken != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.LocalToken)) == 1 {
			a.serveAuthenticated(w, r, next, body, authenticationDecision{mode: "local"})
			return
		}

		// 3) 远程兜底(带缓存/白名单/限流)。
		if a.remoteOK {
			if ok, userID := a.verifyRemote(r.Context(), token); ok {
				ctx := r.Context()
				if userID != "" {
					ctx = context.WithValue(ctx, ctxKeyUserID, userID)
					ctx = trace.WithUserID(ctx, userID)
					// 把 sessionID→userID 存入 map，供 MCPMiddleware 在 MCP 消息层补注。
					// MCP-Session-Id 头由 SDK 在 /mcp 的响应里下发，首次 POST
					// 时客户端会回传该头，从而可在此取到。
					if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
						a.sessionUserID.Store(sid, userID)
					}
				}
				a.serveAuthenticated(w, r.WithContext(ctx), next, body, authenticationDecision{
					mode:      "remote",
					principal: userID,
				})
				return
			}
		}

		a.deny(w, r, ip, "令牌无效")
	})
}

// MCPMiddleware 返回一个 mcp.MiddlewareFunc，在 MCP 消息层面把 userID 注入 context。
// 必须与 Middleware 配合使用，通过 server.AddReceivingMiddleware 注册到 mcp.Server。
//
// 背景：MCP SDK (go-sdk) 在建立 Streamable HTTP / SSE 长连接时会 detach HTTP 请求
// 的 context（见 streamable.go 注释），导致 HTTP 中间件注入的 context value 在
// tool handler 里丢失。本方法在 SDK 的 MCP 消息层面重新补注，确保
// trace.EffectiveUserID(ctx, ...) 能透明取到远程验证返回的 userID。
func (a *Authenticator) MCPMiddleware() func(next mcp.MethodHandler) mcp.MethodHandler {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			// 优先级：ctx 中已有值（URL ?userId= 注入）> session map 中的 userID（远程验证注入）
			if trace.UserIDFromContext(ctx) == "" {
				if sid := req.GetSession().ID(); sid != "" {
					if v, ok := a.sessionUserID.Load(sid); ok {
						ctx = trace.WithUserID(ctx, v.(string))
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}

// CleanSession 清理 session 关闭后残留的 sessionID→userID 映射，防止内存泄漏。
// 在 mcp.ServerOptions.OnSessionClose（或等效回调）中调用。
func (a *Authenticator) CleanSession(sessionID string) {
	a.sessionUserID.Delete(sessionID)
}

// ClientIP 尽力解析请求来源 IP:优先 X-Forwarded-For(取最初客户端)、X-Real-IP,
// 回退到 RemoteAddr。仅用于日志审计,【不用于】安全决策(这些头可被伪造)。
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// securityClientIP 解析【用于安全决策】的来源 IP,返回 nil 表示无法解析。
//
// 与仅用于日志审计的 ClientIP 刻意区分:
//   - TrustForwardedHeader=false(默认):只认 TCP 连接的 RemoteAddr,
//     无视任何可被客户端伪造的 X-Forwarded-For/X-Real-IP 头。
//   - TrustForwardedHeader=true:优先 X-Forwarded-For(首个)→ X-Real-IP → RemoteAddr。
//     仅在服务位于会重写该头的可信代理之后时才应开启。
func (a *Authenticator) securityClientIP(r *http.Request) net.IP {
	if a.cfg.TrustForwardedHeader {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := xff
			if i := strings.IndexByte(xff, ','); i >= 0 {
				first = xff[:i]
			}
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip
			}
		}
		if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
			if ip := net.ParseIP(xrip); ip != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
}

// ipAllowed 报告 ip 是否命中任一受信任网段。
func (a *Authenticator) ipAllowed(ip net.IP) bool {
	for _, n := range a.cfg.AllowedCIDRs {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseCIDRs 把 CIDR 字符串列表解析为 *net.IPNet;空串跳过,任一非法即返回错误(fail-closed)。
func ParseCIDRs(items []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("非法 CIDR %q: %w", s, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	// 仅接受 "Bearer <token>" 形式。
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func (a *Authenticator) deny(w http.ResponseWriter, r *http.Request, ip, reason string) {
	logger.Errorf("[Auth] 验证失败: %s %s from %s, token=%s (%s)", r.Method, r.URL.Path, ip, mask(r.Header.Get("Authorization")), reason)
	w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

func (a *Authenticator) reject(w http.ResponseWriter, r *http.Request, status int, reason string) {
	logger.Errorf("[Auth] 请求拒绝: %s %s from %s, token=%s (%s)",
		r.Method, r.URL.Path, ClientIP(r), mask(r.Header.Get("Authorization")), reason)
	http.Error(w, http.StatusText(status), status)
}

// verifyRemote 查缓存→限流→发起远程验证,并回写缓存。
// 返回 (验证通过, userID)；userID 在远程响应未携带时为空字符串。
func (a *Authenticator) verifyRemote(ctx context.Context, token string) (bool, string) {
	key := hashToken(token)

	// 1. 从缓存载入验证结果。
	if e, ok := a.cache.Get(key); ok {
		return e.ok, e.userID
	}

	// 2. 缓存失效，尝试远程验证（需加限流锁防瞬间穿透爆破）
	a.limiterMu.Lock()
	allowed := a.allowRemoteCallLocked()
	a.limiterMu.Unlock()

	if !allowed {
		logger.Errorf("[Auth] 远程验证被限流,拒绝本次请求")
		return false, ""
	}

	ok, userID := a.doRemoteCall(ctx, token)

	ttl := a.cfg.NegativeTTL
	if ok {
		ttl = a.cfg.PositiveTTL
	}

	// 3. 回写缓存（包含 userID）
	a.cache.Set(key, cacheEntry{ok: ok, userID: userID, exp: time.Now().Add(ttl)})
	return ok, userID
}

// allowRemoteCallLocked 实现简单令牌桶;调用方须持有 a.limiterMu。
func (a *Authenticator) allowRemoteCallLocked() bool {
	now := time.Now()
	elapsed := now.Sub(a.lastRef).Seconds()
	a.lastRef = now
	a.bucket += elapsed * a.cfg.RemoteRateRPS
	if maxBurst := float64(a.cfg.RemoteRateBurst); a.bucket > maxBurst {
		a.bucket = maxBurst
	}
	if a.bucket >= 1 {
		a.bucket--
		return true
	}
	return false
}

// doRemoteCall 向远程验证端点发起请求，返回 (验证通过, userID)。
// userID 从响应体 JSON 的 "userid" 字段解析；响应体非 JSON 或字段缺失时为空字符串。
func (a *Authenticator) doRemoteCall(ctx context.Context, token string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.RemoteVerifyURL, nil)
	if err != nil {
		logger.Errorf("[Auth] 构造远程验证请求失败: %v", err)
		return false, ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		logger.Errorf("[Auth] 远程验证请求异常: %v", err)
		return false, ""
	}
	defer resp.Body.Close()

	// 限制读取大小，防止超大响应体撑爆内存
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		logger.Errorf("[Auth] 读取远程验证响应体失败: %v", err)
		return false, ""
	}

	// 尝试从响应体 JSON 中解析 userid 字段（兼容 data.userid 与 根节点 userid）
	var payload struct {
		UserID string `json:"userid"`
		Data   struct {
			UserID string `json:"userid"`
		} `json:"data"`
	}
	_ = json.Unmarshal(bodyBytes, &payload)

	userID := payload.Data.UserID
	if userID == "" {
		userID = payload.UserID
	}

	ok := resp.StatusCode == http.StatusOK
	if !ok {
		logger.Errorf("[Auth] 远程验证拒绝 token=%s status=%d body=%s",
			mask(token), resp.StatusCode, bodyBytes)
	}
	return ok, userID
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mask 把敏感串脱敏为 "前2…后2" 形式,绝不输出明文。
func mask(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(s, "Bearer "))
	n := len(s)
	if n == 0 {
		return "(empty)"
	}
	if n <= 4 {
		return "****"
	}
	return s[:2] + "…" + s[n-2:]
}

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

func isToolAllowed(toolName string, allowed []string) bool {
	for _, item := range allowed {
		if item == "*" {
			return true
		}
		if item == toolName {
			return true
		}
	}
	return false
}
