// Package auth 提供 HTTP 传输(http/sse)下的 Bearer Token 认证中间件。
//
// 安全设计要点:
//   - 令牌只从 Authorization 头读取,不再支持 URL ?token=(避免凭证落入日志/代理/浏览器历史)。
//   - 本地令牌比对使用 crypto/subtle 常量时间比较,防计时侧信道爆破。
//   - 日志只打印掩码,绝不记录原始令牌。
//   - 远程兜底验证带:正/负结果缓存(TTL)、目标主机白名单(防 SSRF)、全局限流(防放大)。
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wuxujun/xktmcp/internal/logger"
)

// Config 来自环境/命令行的认证配置。
type Config struct {
	// LocalToken 是静态本地令牌;为空表示不启用本地比对。
	LocalToken string
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
	//   false(默认,安全):只认 TCP 连接的 RemoteAddr,杜绝伪造 X-Forwarded-For/
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

// Enabled 报告是否配置了任意一种认证方式(本地令牌 / 远程兜底 / IP 白名单)。
func (c Config) Enabled() bool {
	return c.LocalToken != "" || c.RemoteVerifyURL != "" || len(c.AllowedCIDRs) > 0
}

type cacheEntry struct {
	ok  bool
	exp time.Time
}

// Authenticator 是可复用的认证器(并发安全)。
type Authenticator struct {
	cfg        Config
	httpClient *http.Client
	remoteOK   bool // RemoteVerifyURL 通过白名单校验,远程兜底可用

	mu      sync.Mutex
	cache   map[string]cacheEntry
	bucket  float64   // 当前令牌桶余量
	lastRef time.Time // 上次补充时间
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
		cache:      make(map[string]cacheEntry),
		bucket:     float64(cfg.RemoteRateBurst),
		lastRef:    time.Now(),
	}
	a.remoteOK = cfg.RemoteVerifyURL != "" && hostAllowed(cfg.RemoteVerifyURL, cfg.AllowedHosts)
	if cfg.RemoteVerifyURL != "" && !a.remoteOK {
		logger.Errorf("[Auth] 远程验证 URL 的主机不在白名单内,已禁用远程兜底: %s", cfg.RemoteVerifyURL)
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

		// 1) 本地常量时间比对。
		if a.cfg.LocalToken != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.LocalToken)) == 1 {
			logger.Infof("[Auth] 验证通过: %s %s from %s", r.Method, r.URL.Path, ip)
			next.ServeHTTP(w, r)
			return
		}

		// 2) 远程兜底(带缓存/白名单/限流)。
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

	a.mu.Lock()
	if e, ok := a.cache[key]; ok && time.Now().Before(e.exp) {
		a.mu.Unlock()
		return e.ok
	}
	// 限流:无令牌则直接拒绝远程调用(视为未通过,防放大)。
	if !a.allowRemoteCallLocked() {
		a.mu.Unlock()
		logger.Errorf("[Auth] 远程验证被限流,拒绝本次请求")
		return false
	}
	a.mu.Unlock()

	ok := a.doRemoteCall(ctx, token)

	ttl := a.cfg.NegativeTTL
	if ok {
		ttl = a.cfg.PositiveTTL
	}
	a.mu.Lock()
	a.cache[key] = cacheEntry{ok: ok, exp: time.Now().Add(ttl)}
	a.mu.Unlock()
	return ok
}

// allowRemoteCallLocked 实现简单令牌桶;调用方须持有 a.mu。
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
