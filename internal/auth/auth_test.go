package auth

import (
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
