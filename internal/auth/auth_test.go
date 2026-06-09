package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newReq(authHeader, rawQuery string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/mcp?"+rawQuery, nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	return r
}

func serve(a *Authenticator, r *http.Request) int {
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, r)
	return rr.Code
}

// 本地令牌:正确放行,错误拒绝。
func TestLocalTokenMatch(t *testing.T) {
	a := New(Config{LocalToken: "secret-123"})
	if code := serve(a, newReq("Bearer secret-123", "")); code != http.StatusOK {
		t.Fatalf("正确令牌应放行,得到 %d", code)
	}
	if code := serve(a, newReq("Bearer wrong", "")); code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401,得到 %d", code)
	}
	if code := serve(a, newReq("", "")); code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应 401,得到 %d", code)
	}
}

// 关键回归:URL ?token= 不再被接受。
func TestURLTokenRejected(t *testing.T) {
	a := New(Config{LocalToken: "secret-123"})
	if code := serve(a, newReq("", "token=secret-123")); code != http.StatusUnauthorized {
		t.Fatalf("URL ?token= 必须被拒绝(只收 Authorization 头),得到 %d", code)
	}
}

// 远程兜底:白名单未命中则禁用远程,仅本地比对生效。
func TestRemoteHostNotAllowed(t *testing.T) {
	a := New(Config{
		RemoteVerifyURL: "https://evil.example.com/check",
		AllowedHosts:    []string{"yk.xkt.com"},
	})
	if a.remoteOK {
		t.Fatal("非白名单主机的远程验证应被禁用")
	}
}

// 远程兜底:命中白名单可用,且结果被缓存(第二次不再打后端)。
func TestRemoteVerifyCache(t *testing.T) {
	var calls int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") == "Bearer good" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer backend.Close()

	host := strings.TrimPrefix(backend.URL, "http://")
	a := New(Config{
		RemoteVerifyURL: backend.URL,
		AllowedHosts:    []string{host},
		PositiveTTL:     time.Minute,
		RemoteRateRPS:   100, // 速率足够高,确保此测试只验证缓存而非限流
		RemoteRateBurst: 100,
	})
	if !a.remoteOK {
		t.Fatal("白名单命中,远程验证应可用")
	}

	if code := serve(a, newReq("Bearer good", "")); code != http.StatusOK {
		t.Fatalf("首次远程验证应放行,得到 %d", code)
	}
	if code := serve(a, newReq("Bearer good", "")); code != http.StatusOK {
		t.Fatalf("缓存命中应放行,得到 %d", code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("缓存应使后端只被调用 1 次,实际 %d", got)
	}
}

// 远程兜底:令牌桶耗尽后,后续(不同令牌、缓存未命中)被限流,不再打后端。
func TestRemoteVerifyRateLimit(t *testing.T) {
	var calls int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized) // 所有令牌都判失败
	}))
	defer backend.Close()

	host := strings.TrimPrefix(backend.URL, "http://")
	a := New(Config{
		RemoteVerifyURL: backend.URL,
		AllowedHosts:    []string{host},
		NegativeTTL:     time.Minute,
		RemoteRateRPS:   1,
		RemoteRateBurst: 1, // 仅允许 1 次远程调用
	})

	// bad1:缓存未命中 + 令牌桶有 1 个令牌 → 打后端 1 次。
	_ = serve(a, newReq("Bearer bad1", ""))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("首个坏令牌应打后端 1 次,实际 %d", got)
	}
	// bad2:不同令牌(缓存未命中)+ 令牌桶已空 → 被限流,不打后端。
	_ = serve(a, newReq("Bearer bad2", ""))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("限流后不应再打后端,期望 1,实际 %d", got)
	}
}

func TestClientIP(t *testing.T) {
	// X-Forwarded-For 优先,取最初客户端(首个)。
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := ClientIP(r); got != "203.0.113.7" {
		t.Errorf("XFF 应取首个客户端,得到 %q", got)
	}

	// 无 XFF 时退到 X-Real-IP。
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:5555"
	r2.Header.Set("X-Real-IP", "198.51.100.9")
	if got := ClientIP(r2); got != "198.51.100.9" {
		t.Errorf("应取 X-Real-IP,得到 %q", got)
	}

	// 都没有时退到 RemoteAddr 并剥掉端口。
	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	r3.RemoteAddr = "192.0.2.55:42000"
	if got := ClientIP(r3); got != "192.0.2.55" {
		t.Errorf("应取 RemoteAddr 的 host 部分,得到 %q", got)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                 "(empty)",
		"abc":              "****",
		"Bearer secret123": "se…23",
	}
	for in, want := range cases {
		if got := mask(in); got != want {
			t.Errorf("mask(%q)=%q, 期望 %q", in, got, want)
		}
	}
}

// mustCIDRs 解析 CIDR 列表,失败即 t.Fatal。
func mustCIDRs(t *testing.T, items ...string) []*net.IPNet {
	t.Helper()
	n, err := ParseCIDRs(items)
	if err != nil {
		t.Fatalf("解析 CIDR 失败: %v", err)
	}
	return n
}

// serveFrom 用指定 RemoteAddr/转发头发起请求并返回状态码。
func serveFrom(a *Authenticator, remoteAddr, authHeader string, headers map[string]string) int {
	r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	r.RemoteAddr = remoteAddr
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, r)
	return rr.Code
}

// IP 白名单:命中网段即放行(无需 Bearer 令牌),未命中且无令牌则拒绝。
func TestIPAllowlistBypass(t *testing.T) {
	a := New(Config{AllowedCIDRs: mustCIDRs(t, "160.79.104.0/21")})

	// 网段内(无 Authorization 头)→ 放行。
	if code := serveFrom(a, "160.79.105.42:33000", "", nil); code != http.StatusOK {
		t.Fatalf("可信网段内应放行,得到 %d", code)
	}
	// 网段边界内的另一地址。
	if code := serveFrom(a, "160.79.111.255:1", "", nil); code != http.StatusOK {
		t.Fatalf("网段边界内应放行,得到 %d", code)
	}
	// 网段外且无令牌 → 401。
	if code := serveFrom(a, "8.8.8.8:5000", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("网段外且无令牌应 401,得到 %d", code)
	}
	// 网段外但紧邻(/21 之外)→ 401。
	if code := serveFrom(a, "160.79.112.1:5000", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("网段外(112.x)应 401,得到 %d", code)
	}
}

// IP 白名单与本地令牌共存:网段外仍可凭正确令牌放行。
func TestIPAllowlistWithTokenFallback(t *testing.T) {
	a := New(Config{
		LocalToken:   "secret-123",
		AllowedCIDRs: mustCIDRs(t, "160.79.104.0/21"),
	})
	// 网段外 + 正确令牌 → 放行。
	if code := serveFrom(a, "8.8.8.8:5000", "Bearer secret-123", nil); code != http.StatusOK {
		t.Fatalf("网段外但令牌正确应放行,得到 %d", code)
	}
	// 网段外 + 错误令牌 → 401。
	if code := serveFrom(a, "8.8.8.8:5000", "Bearer wrong", nil); code != http.StatusUnauthorized {
		t.Fatalf("网段外且令牌错误应 401,得到 %d", code)
	}
}

// 安全关键:默认不信任 X-Forwarded-For,伪造转发头无法绕过认证。
func TestIPAllowlistForwardedHeaderNotTrustedByDefault(t *testing.T) {
	a := New(Config{AllowedCIDRs: mustCIDRs(t, "160.79.104.0/21")})
	// 真实连接在网段外,但伪造 XFF 假装在网段内 → 必须仍 401。
	hdr := map[string]string{"X-Forwarded-For": "160.79.105.1"}
	if code := serveFrom(a, "8.8.8.8:5000", "", hdr); code != http.StatusUnauthorized {
		t.Fatalf("默认不信任 XFF,伪造转发头不得绕过,应 401,得到 %d", code)
	}
	// 同理 X-Real-IP 也不应被信任。
	hdr2 := map[string]string{"X-Real-IP": "160.79.105.1"}
	if code := serveFrom(a, "8.8.8.8:5000", "", hdr2); code != http.StatusUnauthorized {
		t.Fatalf("默认不信任 X-Real-IP,应 401,得到 %d", code)
	}
}

// 部署在可信代理后:开启 TrustForwardedHeader 后按 XFF 首个地址判定。
func TestIPAllowlistForwardedHeaderTrusted(t *testing.T) {
	a := New(Config{
		AllowedCIDRs:         mustCIDRs(t, "160.79.104.0/21"),
		TrustForwardedHeader: true,
	})
	// 代理连接(RemoteAddr 为代理 IP,在网段外),XFF 首个为真实客户端(网段内)→ 放行。
	hdr := map[string]string{"X-Forwarded-For": "160.79.105.7, 10.0.0.1"}
	if code := serveFrom(a, "10.0.0.1:5000", "", hdr); code != http.StatusOK {
		t.Fatalf("信任转发头时,XFF 首个在网段内应放行,得到 %d", code)
	}
	// XFF 首个客户端在网段外 → 401。
	hdr2 := map[string]string{"X-Forwarded-For": "8.8.8.8, 10.0.0.1"}
	if code := serveFrom(a, "10.0.0.1:5000", "", hdr2); code != http.StatusUnauthorized {
		t.Fatalf("XFF 首个在网段外应 401,得到 %d", code)
	}
}

// Config.Enabled:仅配置 IP 白名单也算启用了认证(http/sse 可启动)。
func TestEnabledWithCIDROnly(t *testing.T) {
	c := Config{AllowedCIDRs: mustCIDRs(t, "160.79.104.0/21")}
	if !c.Enabled() {
		t.Fatal("仅配置 IP 白名单时 Enabled() 应为 true")
	}
	if (Config{}).Enabled() {
		t.Fatal("空配置 Enabled() 应为 false")
	}
}

// ParseCIDRs:合法解析、空串跳过、非法报错。
func TestParseCIDRs(t *testing.T) {
	got, err := ParseCIDRs([]string{"160.79.104.0/21", "  ", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("合法 CIDR 不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应解析出 2 个网段(空串跳过),得到 %d", len(got))
	}
	if _, err := ParseCIDRs([]string{"not-a-cidr"}); err == nil {
		t.Fatal("非法 CIDR 应返回错误")
	}
}
