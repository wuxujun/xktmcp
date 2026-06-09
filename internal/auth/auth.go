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
	"encoding/json"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wuxujun/xktmcp/internal/logger"
)

// TenantConfig 定义多租户配置结构
type TenantConfig struct {
	Name         string   `json:"name"`
	Token        string   `json:"token"`
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
}

// Enabled 报告是否配置了任意一种认证方式(本地令牌 / 多租户 / 远程兜底 / IP 白名单)。
func (c Config) Enabled() bool {
	return c.LocalToken != "" || len(c.Tenants) > 0 || c.RemoteVerifyURL != "" || len(c.AllowedCIDRs) > 0
}

type cacheEntry struct {
	ok  bool
	exp time.Time
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

	cache sync.Map // 缓存校验结果 (key: sha256_hash_string, value: cacheEntry)

	tenantsByToken map[string]*Tenant
}

// Enabled 报告该认证器是否启用了任意一种认证方式。
func (a *Authenticator) Enabled() bool { return a.cfg.Enabled() }

// New 构造 Authenticator,并对远程验证 URL 做白名单校验。
func New(cfg Config) *Authenticator {
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

	a := &Authenticator{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.RemoteTimeout},
		bucket:     float64(cfg.RemoteRateBurst),
		lastRef:    time.Now(),
	}
	a.remoteOK = cfg.RemoteVerifyURL != "" && hostAllowed(cfg.RemoteVerifyURL, cfg.AllowedHosts)
	if cfg.RemoteVerifyURL != "" && !a.remoteOK {
		logger.Errorf("[Auth] 远程验证 URL 的主机不在白名单内,已禁用远程兜底: %s", cfg.RemoteVerifyURL)
	}

	// 初始化租户映射与限流器
	a.tenantsByToken = make(map[string]*Tenant)
	for _, tc := range cfg.Tenants {
		if tc.Token == "" {
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
		a.tenantsByToken[tc.Token] = &Tenant{
			Config:  tc,
			Limiter: limiter,
		}
	}

	return a
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
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)

		// 0) 受信任网段直接放行(IP 白名单),无需 Bearer 令牌。
		//    安全决策的 IP 取值由 TrustForwardedHeader 决定:默认仅信任 TCP 连接的
		//    RemoteAddr,杜绝伪造转发头绕过;仅当部署在可信代理后才信任 X-Forwarded-For。
		if len(a.cfg.AllowedCIDRs) > 0 {
			if srcIP := a.securityClientIP(r); srcIP != nil && a.ipAllowed(srcIP) {
				logger.Infof("[Auth] 验证通过(可信网段 %s): %s %s", srcIP, r.Method, r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}
		}

		token := bearerFromHeader(r)
		if token == "" {
			a.deny(w, r, ip, "缺少 Bearer 令牌")
			return
		}

		// 1) 多租户鉴权与细粒度流控、ACL 检查
		if len(a.tenantsByToken) > 0 {
			if tenant, ok := a.tenantsByToken[token]; ok {
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

				// 租户级工具 ACL 权限检查
				toolName, bodyBytes, err := extractToolName(r)
				if err != nil {
					a.deny(w, r, ip, fmt.Sprintf("读取请求 payload 失败: %v", err))
					return
				}
				if len(bodyBytes) > 0 {
					r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				}

				if toolName != "" {
					if !isToolAllowed(toolName, tenant.Config.AllowedTools) {
						a.deny(w, r, ip, fmt.Sprintf("租户 %s 无权调用工具 %s", tenant.Config.Name, toolName))
						return
					}
				}

				logger.Infof("[Auth] 租户 %s 验证通过: %s %s from %s", tenant.Config.Name, r.Method, r.URL.Path, ip)
				next.ServeHTTP(w, r)
				return
			}
		}

		// 2) 本地常量时间比对 (全局静态 Token 兜底)。
		if a.cfg.LocalToken != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.LocalToken)) == 1 {
			logger.Infof("[Auth] 验证通过: %s %s from %s", r.Method, r.URL.Path, ip)
			next.ServeHTTP(w, r)
			return
		}

		// 3) 远程兜底(带缓存/白名单/限流)。
		if a.remoteOK && a.verifyRemote(r.Context(), token) {
			logger.Infof("[Auth] 验证通过(远程): %s %s from %s", r.Method, r.URL.Path, ip)
			next.ServeHTTP(w, r)
			return
		}

		a.deny(w, r, ip, "令牌无效")
	})
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

// verifyRemote 查缓存→限流→发起远程验证,并回写缓存。
func (a *Authenticator) verifyRemote(ctx context.Context, token string) bool {
	key := hashToken(token)

	// 1. 无锁从 sync.Map 载入缓存
	if val, ok := a.cache.Load(key); ok {
		e := val.(cacheEntry)
		if time.Now().Before(e.exp) {
			return e.ok
		}
	}

	// 2. 缓存失效，尝试远程验证（需加限流锁防瞬间穿透爆破）
	a.limiterMu.Lock()
	allowed := a.allowRemoteCallLocked()
	a.limiterMu.Unlock()

	if !allowed {
		logger.Errorf("[Auth] 远程验证被限流,拒绝本次请求")
		return false
	}

	ok := a.doRemoteCall(ctx, token)

	ttl := a.cfg.NegativeTTL
	if ok {
		ttl = a.cfg.PositiveTTL
	}

	// 3. 无锁回写缓存到 sync.Map
	a.cache.Store(key, cacheEntry{ok: ok, exp: time.Now().Add(ttl)})
	return ok
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

func (a *Authenticator) doRemoteCall(ctx context.Context, token string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.RemoteVerifyURL, nil)
	if err != nil {
		logger.Errorf("[Auth] 构造远程验证请求失败: %v", err)
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		logger.Errorf("[Auth] 远程验证请求异常: %v", err)
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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

type jsonRPCRequest struct {
	Method string `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

func extractToolName(r *http.Request) (string, []byte, error) {
	if r.Method != http.MethodPost || r.Body == nil {
		return "", nil, nil
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}

	var rpcReq jsonRPCRequest
	if err := json.Unmarshal(bodyBytes, &rpcReq); err == nil {
		if rpcReq.Method == "tools/call" {
			return rpcReq.Params.Name, bodyBytes, nil
		}
	}
	return "", bodyBytes, nil
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
