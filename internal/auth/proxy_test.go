package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRejectsForwardedHeadersWithoutTrustedProxies(t *testing.T) {
	if _, err := New(Config{LocalToken: "token", TrustForwardedHeader: true}); err == nil {
		t.Fatal("forwarded headers were enabled without trusted proxy CIDRs")
	}
}

func TestSecurityClientIPTrustedProxyChain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		peer  string
		xff   []string
		real  []string
		trust bool
		want  string
	}{
		{"disabled", "10.0.0.10:5000", []string{"198.51.100.1"}, nil, false, "10.0.0.10"},
		{"untrusted_peer", "198.51.100.1:5000", []string{"160.79.105.7"}, []string{"160.79.105.7"}, true, "198.51.100.1"},
		{"single_proxy", "10.0.0.10:5000", []string{"198.51.100.1"}, nil, true, "198.51.100.1"},
		{"two_proxies", "10.0.0.10:5000", []string{"198.51.100.1, 10.0.0.11"}, nil, true, "198.51.100.1"},
		{"spoofed_prefix", "10.0.0.10:5000", []string{"160.79.105.7, 198.51.100.1, 10.0.0.11"}, nil, true, "198.51.100.1"},
		{"duplicate_xff", "10.0.0.10:5000", []string{"160.79.105.7", "198.51.100.1"}, nil, true, "198.51.100.1"},
		{"malformed_prefix", "10.0.0.10:5000", []string{"invalid, 198.51.100.1"}, nil, true, "198.51.100.1"},
		{"malformed_nearest_hop", "10.0.0.10:5000", []string{"198.51.100.1, invalid"}, []string{"160.79.105.7"}, true, "10.0.0.10"},
		{"empty_xff", "10.0.0.10:5000", []string{""}, []string{"160.79.105.7"}, true, "10.0.0.10"},
		{"real_ip", "10.0.0.10:5000", nil, []string{"198.51.100.1"}, true, "198.51.100.1"},
		{"duplicate_real_ip", "10.0.0.10:5000", nil, []string{"160.79.105.7", "198.51.100.1"}, true, "10.0.0.10"},
		{"missing_headers", "10.0.0.10:5000", nil, nil, true, "10.0.0.10"},
		{"invalid_peer", "invalid", []string{"160.79.105.7"}, nil, true, ""},
		{"ipv6", "[2001:db8::10]:5000", []string{"2001:db8:1::2"}, nil, true, "2001:db8:1::2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := mustAuthenticator(t, Config{
				TrustForwardedHeader: tc.trust,
				TrustedProxyCIDRs:    mustCIDRs(t, "10.0.0.10/32", "10.0.0.11/32", "2001:db8::10/128"),
			})
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			req.RemoteAddr = tc.peer
			for _, value := range tc.xff {
				req.Header.Add("X-Forwarded-For", value)
			}
			for _, value := range tc.real {
				req.Header.Add("X-Real-IP", value)
			}
			got := ""
			if ip := a.securityClientIP(req); ip != nil {
				got = ip.String()
			}
			if got != tc.want {
				t.Fatalf("client IP=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteVerifyClientsBehindTrustedProxyHaveSeparateLimits(t *testing.T) {
	for _, headers := range []string{"xff", "real_ip", "proxy_chain", "duplicate_xff"} {
		t.Run(headers, func(t *testing.T) {
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"userid":"valid-user"}`))
			}))
			defer backend.Close()
			a := mustAuthenticator(t, Config{
				RemoteVerifyURL: backend.URL, AllowedHosts: []string{strings.TrimPrefix(backend.URL, "http://")},
				TrustForwardedHeader: true, TrustedProxyCIDRs: mustCIDRs(t, "10.0.0.10/32", "10.0.0.11/32"),
				RemoteIPRateRPS: 0.000001, RemoteIPRateBurst: 1,
				RemoteRateRPS: 10, RemoteRateBurst: 10, PositiveTTL: time.Minute,
			})
			request := func(client, token string) int {
				req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
				req.RemoteAddr = "10.0.0.10:5000"
				req.Header.Set("Authorization", "Bearer "+token)
				switch headers {
				case "xff":
					req.Header.Set("X-Forwarded-For", client)
				case "real_ip":
					req.Header.Set("X-Real-IP", client)
				case "proxy_chain":
					req.Header.Set("X-Forwarded-For", "160.79.105.7, "+client+", 10.0.0.11")
				case "duplicate_xff":
					req.Header.Add("X-Forwarded-For", "160.79.105.7")
					req.Header.Add("X-Forwarded-For", client)
				}
				return serve(a, req)
			}
			if code := request("198.51.100.1", "client-a-first"); code != http.StatusOK {
				t.Fatalf("first client status=%d, want 200", code)
			}
			if code := request("198.51.100.1", "client-a-second"); code != http.StatusUnauthorized {
				t.Fatalf("same client status=%d, want per-IP denial", code)
			}
			if code := request("198.51.100.2", "client-b-first"); code != http.StatusOK {
				t.Fatalf("second client shared proxy quota: status=%d, want 200", code)
			}
			if calls.Load() != 2 {
				t.Fatalf("backend calls=%d, want 2", calls.Load())
			}
		})
	}
}

func TestRemoteVerifyUntrustedHeadersCannotRotateLimiterIP(t *testing.T) {
	for _, tc := range []struct {
		name  string
		peer  string
		trust bool
	}{
		{"disabled", "10.0.0.10:5000", false},
		{"untrusted_peer", "198.51.100.1:5000", true},
		{"invalid_peer", "invalid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			a := mustAuthenticator(t, Config{
				RemoteVerifyURL: backend.URL, AllowedHosts: []string{strings.TrimPrefix(backend.URL, "http://")},
				TrustForwardedHeader: tc.trust, TrustedProxyCIDRs: mustCIDRs(t, "10.0.0.10/32"),
				RemoteIPRateRPS: 0.000001, RemoteIPRateBurst: 1, RemoteRateBurst: 10,
			})
			for i, ip := range []string{"198.51.100.11", "198.51.100.12"} {
				code := serveFrom(a, tc.peer, "Bearer "+ip, map[string]string{"X-Forwarded-For": ip, "X-Real-IP": ip})
				want := http.StatusOK
				if i > 0 {
					want = http.StatusUnauthorized
				}
				if code != want {
					t.Fatalf("request %d status=%d, want %d", i, code, want)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("spoofed headers bypassed limiter: backend calls=%d", calls.Load())
			}
		})
	}
}

func TestTrustedProxyHeadersCannotSpoofAllowlistedClient(t *testing.T) {
	a := mustAuthenticator(t, Config{
		AllowedCIDRs:         mustCIDRs(t, "160.79.104.0/21"),
		TrustForwardedHeader: true, TrustedProxyCIDRs: mustCIDRs(t, "10.0.0.10/32"),
	})
	for _, tc := range []struct {
		peer string
		xff  string
	}{
		{"10.0.0.10:5000", "160.79.105.7, 198.51.100.1"},
		{"198.51.100.1:5000", "160.79.105.7"},
		{"10.0.0.10:5000", ""},
	} {
		if code := serveFrom(a, tc.peer, "", map[string]string{"X-Forwarded-For": tc.xff}); code != http.StatusUnauthorized {
			t.Fatalf("spoofed allowlist access: status=%d, want 401", code)
		}
	}
	proxyOnly := Config{TrustForwardedHeader: true, TrustedProxyCIDRs: mustCIDRs(t, "10.0.0.10/32")}
	if proxyOnly.Enabled() || mustAuthenticator(t, proxyOnly).Enabled() {
		t.Fatal("trusted proxy CIDRs enabled authentication bypass")
	}
}
