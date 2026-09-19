package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/auth"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/trace"
)

const (
	protocolVersion20251125 = "2025-11-25"
	protocolVersion20260728 = "2026-07-28"
)

func TestBuildAuthConfigParsesRemoteCacheCapacity(t *testing.T) {
	t.Setenv("AUTH_REMOTE_CACHE_MAX_ENTRIES", "123")
	cfg, err := buildAuthConfig("token")
	if err != nil || cfg.RemoteCacheMaxEntries != 123 {
		t.Fatalf("capacity=%d err=%v, want 123 nil", cfg.RemoteCacheMaxEntries, err)
	}
}

func TestBuildAuthConfigParsesRemoteCacheTTLs(t *testing.T) {
	t.Setenv("AUTH_REMOTE_CACHE_POSITIVE_TTL", "45s")
	t.Setenv("AUTH_REMOTE_CACHE_NEGATIVE_TTL", "7s")
	cfg, err := buildAuthConfig("token")
	if err != nil || cfg.PositiveTTL != 45*time.Second || cfg.NegativeTTL != 7*time.Second {
		t.Fatalf("positive=%v negative=%v err=%v", cfg.PositiveTTL, cfg.NegativeTTL, err)
	}
}

func TestBuildAuthConfigRejectsInvalidRemoteCacheTTL(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AUTH_REMOTE_CACHE_POSITIVE_TTL", value)
			if _, err := buildAuthConfig("token"); err == nil {
				t.Fatalf("buildAuthConfig accepted %q", value)
			}
		})
	}
}

func TestMetricsAuthHandler(t *testing.T) {
	next := metrics.Handler()
	t.Setenv("METRICS_AUTH_TOKEN", "secret")
	for _, tc := range []struct {
		name   string
		header string
		code   int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer other", http.StatusUnauthorized},
		{"valid", "Bearer secret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.Header.Set("Authorization", tc.header)
			metricsAuthHandler(next).ServeHTTP(rr, req)
			if rr.Code != tc.code {
				t.Fatalf("status=%d, want %d", rr.Code, tc.code)
			}
		})
	}
}

func TestReadinessHandler(t *testing.T) {
	ready := false
	handler := readinessHandler(func() bool { return ready })
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status=%d, want 503", rec.Code)
	}

	ready = true
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ready"}` {
		t.Fatalf("ready response status=%d body=%q, want 200 ready", rec.Code, rec.Body.String())
	}
}

func TestEnvironmentParsingHelpers(t *testing.T) {
	for _, raw := range []string{"1", "true", "YES", "on"} {
		t.Setenv("TEST_BOOL", raw)
		if !envBool("TEST_BOOL") {
			t.Errorf("envBool(%q)=false", raw)
		}
	}
	t.Setenv("TEST_BOOL", "off")
	if envBool("TEST_BOOL") {
		t.Error("envBool(off)=true")
	}
	t.Setenv("TEST_POSITIVE", "7")
	if got, err := envPositiveInt("TEST_POSITIVE", 2); err != nil || got != 7 {
		t.Fatalf("positive=%d err=%v", got, err)
	}
	t.Setenv("TEST_POSITIVE", "bad")
	if _, err := envPositiveInt("TEST_POSITIVE", 2); err == nil {
		t.Fatal("invalid positive integer accepted")
	}
	t.Setenv("TEST_POSITIVE", "0")
	if _, err := envPositiveInt("TEST_POSITIVE", 2); err == nil {
		t.Fatal("zero positive integer accepted")
	}
	t.Setenv("TEST_POSITIVE", "")
	if got, err := envPositiveInt("TEST_POSITIVE", 2); err != nil || got != 2 {
		t.Fatalf("empty fallback=%d err=%v", got, err)
	}
	t.Setenv("TEST_INT", "bad")
	if got := envInt64("TEST_INT", 9); got != 9 {
		t.Fatalf("fallback=%d", got)
	}
	t.Setenv("TEST_INT", "11")
	if got := envInt64("TEST_INT", 9); got != 11 {
		t.Fatalf("parsed int=%d", got)
	}
}

func TestResponseRecorderCapturesAndUnwraps(t *testing.T) {
	base := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: base, bodyCapture: newLimitedBodyCapture(3)}
	_, _ = rec.Write([]byte("hello"))
	if rec.statusCode != http.StatusOK || rec.bodyCapture.String() != "hel" || !rec.bodyCapture.truncated {
		t.Fatalf("status=%d body=%q truncated=%t", rec.statusCode, rec.bodyCapture.String(), rec.bodyCapture.truncated)
	}
	if rec.Unwrap() != base {
		t.Fatal("Unwrap did not return underlying writer")
	}
	rec.Flush()
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

func TestUserIDMiddlewareInjectsQueryUserID(t *testing.T) {
	var got string
	handler := userIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/sse?userId=user-123", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "user-123" {
		t.Fatalf("expected context userId user-123, got %q", got)
	}
}

func TestUserIDMiddlewareKeepsExistingContextWhenQueryMissing(t *testing.T) {
	var got string
	handler := userIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/messages/1", nil)
	req = req.WithContext(trace.WithUserID(req.Context(), "existing-user"))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "existing-user" {
		t.Fatalf("expected existing context userId, got %q", got)
	}
}

func TestStreamableHTTPDiscoverSupports20260728(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "1.0.0",
	}, nil)
	var gotUserID string
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			gotUserID = trace.UserIDFromContext(ctx)
			return next(ctx, method, req)
		}
	})
	handler := newStreamableHTTPHandler(server)

	body := []byte(`{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"Postman Client","version":"12.24.2"},"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{},"url":{}},"sampling":{}}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion20260728)
	req.Header.Set("Mcp-Method", "server/discover")
	req = req.WithContext(trace.WithUserID(req.Context(), "remote-user-123"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.StatusCode, responseBody)
	}
	if contentType := res.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream for 2026-07-28", contentType)
	}

	// Streamable HTTP 可返回 JSON 或单条 SSE 事件，两种格式均需验证。
	payload := responseBody
	if i := bytes.Index(responseBody, []byte("data: ")); i >= 0 {
		payload = responseBody[i+len("data: "):]
		if j := bytes.IndexByte(payload, '\n'); j >= 0 {
			payload = payload[:j]
		}
	}
	var rpcResponse struct {
		Result struct {
			SupportedVersions []string `json:"supportedVersions"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpcResponse); err != nil {
		t.Fatalf("decode response %q: %v", responseBody, err)
	}
	if len(rpcResponse.Error) > 0 && string(rpcResponse.Error) != "null" {
		t.Fatalf("discover returned error: %s", rpcResponse.Error)
	}
	if !slices.Contains(rpcResponse.Result.SupportedVersions, protocolVersion20260728) {
		t.Fatalf("supportedVersions = %v, want %q", rpcResponse.Result.SupportedVersions, protocolVersion20260728)
	}
	if gotUserID != "remote-user-123" {
		t.Fatalf("MCP request context userID = %q, want remote-user-123", gotUserID)
	}
}

func TestStreamableHTTPAuthenticatedStateless20260728RequestsRemainIndependent(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	authenticator, err := auth.New(auth.Config{Tenants: []auth.TenantConfig{
		{Name: "tenant-a", Token: "token-a", UserID: "user-a", AllowedTools: []string{"*"}},
		{Name: "tenant-peer", Token: "token-peer", UserID: "user-b", AllowedTools: []string{"*"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Middleware(streamableSessionBindingMiddleware(newStreamableHTTPHandler(server), newSessionBindings()))
	body := []byte(`{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"Postman Client","version":"12.24.2"},"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{},"url":{}},"sampling":{}}}}}`)

	for _, token := range []string{"token-a", "token-peer"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", protocolVersion20260728)
		req.Header.Set("Mcp-Method", "server/discover")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		responseBody := rec.Body.Bytes()
		if rec.Code != http.StatusOK {
			t.Fatalf("independent request with %q returned status %d, want 200; body = %s", token, rec.Code, responseBody)
		}
		payload := responseBody
		if i := bytes.Index(responseBody, []byte("data: ")); i >= 0 {
			payload = responseBody[i+len("data: "):]
			if j := bytes.IndexByte(payload, '\n'); j >= 0 {
				payload = payload[:j]
			}
		}
		var rpcResponse struct {
			Result struct {
				SupportedVersions []string `json:"supportedVersions"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(payload, &rpcResponse); err != nil {
			t.Fatalf("decode independent response for %q %q: %v", token, responseBody, err)
		}
		if len(rpcResponse.Error) > 0 && string(rpcResponse.Error) != "null" {
			t.Fatalf("independent request with %q returned RPC error: %s", token, rpcResponse.Error)
		}
		if !slices.Contains(rpcResponse.Result.SupportedVersions, protocolVersion20260728) {
			t.Fatalf("independent request with %q supportedVersions = %v, want %q; body = %s", token, rpcResponse.Result.SupportedVersions, protocolVersion20260728, responseBody)
		}
		if sessionID := rec.Header().Get("Mcp-Session-Id"); sessionID != "" {
			t.Fatalf("independent request with %q returned Mcp-Session-Id %q", token, sessionID)
		}
	}
}

func TestStreamableHTTPInitialize20251125ReturnsJSON(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "legacy_tool", Description: "legacy protocol test tool"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	handler := newStreamableHTTPHandler(server)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.StatusCode, responseBody)
	}
	if contentType := res.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json; body = %s", contentType, responseBody)
	}
	var rpcResponse struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &rpcResponse); err != nil {
		t.Fatalf("response is not directly parseable JSON: %q: %v", responseBody, err)
	}
	if rpcResponse.Result.ProtocolVersion != protocolVersion20251125 {
		t.Fatalf("protocolVersion = %q, want %q", rpcResponse.Result.ProtocolVersion, protocolVersion20251125)
	}
	sessionID := res.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("2025-11-25 initialize response did not return Mcp-Session-Id")
	}

	initializedReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	setLegacyMCPHeaders(initializedReq, sessionID)
	initializedRec := httptest.NewRecorder()
	handler.ServeHTTP(initializedRec, initializedReq)
	if initializedRec.Code != http.StatusAccepted {
		t.Fatalf("notifications/initialized status = %d, want 202; body = %s", initializedRec.Code, initializedRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	setLegacyMCPHeaders(listReq, sessionID)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200; body = %s", listRec.Code, listRec.Body.String())
	}
	if contentType := listRec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("tools/list Content-Type = %q, want application/json", contentType)
	}
	var listResponse struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("tools/list response is not JSON: %q: %v", listRec.Body.String(), err)
	}
	if len(listResponse.Result.Tools) != 1 || listResponse.Result.Tools[0].Name != "legacy_tool" {
		t.Fatalf("tools/list result = %+v, want legacy_tool; error=%s", listResponse.Result.Tools, listResponse.Error)
	}
}

func setLegacyMCPHeaders(req *http.Request, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion20251125)
	req.Header.Set("Mcp-Session-Id", sessionID)
}

func TestRequestProtocolVersionUsesHeaderAfterInitialization(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	req.Header.Set("Mcp-Protocol-Version", protocolVersion20251125)
	if got := requestProtocolVersion(req); got != protocolVersion20251125 {
		t.Fatalf("requestProtocolVersion = %q, want %q", got, protocolVersion20251125)
	}
}

func TestRequestLoggingMiddlewareLogsPayloadsWithoutTruncatingRequest(t *testing.T) {
	var logs bytes.Buffer
	logger.Init(&logs)
	requestBody := "0123456789"
	responseBody := "abcdefghij"
	var receivedBody string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		receivedBody = string(data)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(responseBody))
	})
	middleware := requestLoggingMiddleware(handler, httpPayloadLogConfig{Enabled: true, MaxBytes: 4})
	req := httptest.NewRequest(http.MethodPut, "/mcp?userId=u1", strings.NewReader(requestBody))
	rec := httptest.NewRecorder()

	middleware.ServeHTTP(rec, req)

	if receivedBody != requestBody {
		t.Fatalf("handler received body %q, want complete %q", receivedBody, requestBody)
	}
	if rec.Code != http.StatusCreated || rec.Body.String() != responseBody {
		t.Fatalf("response = status %d body %q", rec.Code, rec.Body.String())
	}
	output := logs.String()
	for _, expected := range []string{
		`"category":"http"`,
		`"direction":"request"`,
		`"request_body":"0123"`,
		`"request_body_truncated":true`,
		`"direction":"response"`,
		`"response_body":"abcd"`,
		`"response_body_truncated":true`,
		`"response_body_bytes":10`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log output missing %s:\n%s", expected, output)
		}
	}
}

type countingReadCloser struct {
	io.Reader
	bytesRead int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func (*countingReadCloser) Close() error { return nil }

func TestRequestLoggingMiddlewareBoundsZeroLimitPOSTBeforeAuth(t *testing.T) {
	logger.Init(io.Discard)
	source := &countingReadCloser{Reader: strings.NewReader(strings.Repeat("x", protocolDetectionMaxBytes+4096))}
	req := httptest.NewRequest(http.MethodPost, "/mcp", source)
	req.Header.Set("Authorization", "Bearer secret")

	authenticator, err := auth.New(auth.Config{LocalToken: "secret"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	called := false
	handler := requestLoggingMiddleware(authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})), httpPayloadLogConfig{Enabled: true, MaxBytes: 0})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("status=%d called=%t, want 413 false", recorder.Code, called)
	}
	if source.bytesRead != protocolDetectionMaxBytes+1 {
		t.Fatalf("source bytes read=%d, want auth boundary sentinel=%d", source.bytesRead, protocolDetectionMaxBytes+1)
	}
}

func TestRequestLoggingMiddlewareOmitsPayloadsWhenDisabled(t *testing.T) {
	var logs bytes.Buffer
	logger.Init(&logs)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("response-secret"))
	})
	middleware := requestLoggingMiddleware(handler, httpPayloadLogConfig{Enabled: false, MaxBytes: 4})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("request-secret"))
	req.Header.Set("Mcp-Protocol-Version", protocolVersion20251125)
	req.Header.Set("Mcp-Session-Id", "session-123")
	req.Header.Set("Authorization", "Bearer super-secret-token")

	middleware.ServeHTTP(httptest.NewRecorder(), req)

	output := logs.String()
	if strings.Contains(output, "request_body") || strings.Contains(output, "response_body") ||
		strings.Contains(output, "request-secret") || strings.Contains(output, "response-secret") {
		t.Fatalf("disabled payload logging leaked body content:\n%s", output)
	}
	if !strings.Contains(output, `"direction":"request"`) || !strings.Contains(output, `"direction":"response"`) {
		t.Fatalf("metadata logs missing while payload logging disabled:\n%s", output)
	}
	if !strings.Contains(output, `"mcp_protocol_version":"2025-11-25"`) ||
		!strings.Contains(output, `"mcp_session_id":"se*******23"`) {
		t.Fatalf("MCP request headers missing from log:\n%s", output)
	}
	if strings.Contains(output, "super-secret-token") || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("sensitive request header was not redacted:\n%s", output)
	}
}

func TestRequestLoggingMiddlewareSanitizesMCPSessionIDMetadata(t *testing.T) {
	const (
		rawSessionID    = "raw-session-7z9q"
		maskedSessionID = "ra************9q"
	)
	var logs bytes.Buffer
	logger.Init(&logs)
	handler := requestLoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), httpPayloadLogConfig{})
	req := httptest.NewRequest(http.MethodPost, "/messages/?SeSsIoNiD="+rawSessionID+"&trace=trace-123&userId=user-42", nil)
	req.Header.Set("Mcp-Session-Id", rawSessionID)
	req.Header.Set("X-Trace-Id", "header-trace-456")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	output := logs.String()
	if strings.Contains(output, rawSessionID) {
		t.Fatalf("request metadata logs contain raw MCP session ID:\n%s", output)
	}

	var requestLog, responseLog map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line: %v\n%s", err, line)
		}
		switch entry["direction"] {
		case "request":
			requestLog = entry
		case "response":
			responseLog = entry
		}
	}
	if requestLog == nil || responseLog == nil {
		t.Fatalf("missing request or response metadata log:\n%s", output)
	}
	if got := requestLog["mcp_session_id"]; got != maskedSessionID {
		t.Fatalf("mcp_session_id = %q, want %q", got, maskedSessionID)
	}
	for direction, entry := range map[string]map[string]any{"request": requestLog, "response": responseLog} {
		loggedPath, ok := entry["path"].(string)
		if !ok {
			t.Fatalf("%s path = %#v, want string", direction, entry["path"])
		}
		loggedURL, err := url.ParseRequestURI(loggedPath)
		if err != nil {
			t.Fatalf("parse %s path %q: %v", direction, loggedPath, err)
		}
		query := loggedURL.Query()
		if got := query.Get("SeSsIoNiD"); got != maskedSessionID {
			t.Fatalf("%s sessionid query = %q, want %q", direction, got, maskedSessionID)
		}
		if query.Get("trace") != "trace-123" || query.Get("userId") != "user-42" {
			t.Fatalf("%s path lost non-sensitive query metadata: %q", direction, loggedPath)
		}
	}
	headers, ok := requestLog["request_headers"].(map[string]any)
	if !ok {
		t.Fatalf("request_headers = %#v, want object", requestLog["request_headers"])
	}
	if got := headers["Mcp-Session-Id"]; !reflect.DeepEqual(got, []any{"[REDACTED]"}) {
		t.Fatalf("Mcp-Session-Id request header = %#v, want redacted", got)
	}
	if got := headers["X-Trace-Id"]; !reflect.DeepEqual(got, []any{"header-trace-456"}) {
		t.Fatalf("X-Trace-Id request header = %#v, want preserved", got)
	}
}

func TestRequestBodyReadTimeoutMiddlewareRejectsSlowPOST(t *testing.T) {
	authenticator, err := auth.New(auth.Config{LocalToken: "secret"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	handler := requestBodyReadTimeoutMiddleware(
		requestLoggingMiddleware(
			authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("slow request unexpectedly reached handler")
			})),
			httpPayloadLogConfig{Enabled: true, MaxBytes: 1024},
		),
		25*time.Millisecond,
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	request := "POST /mcp HTTP/1.1\r\n" +
		"Host: test\r\n" +
		"Authorization: Bearer secret\r\n" +
		"Content-Length: 10\r\n\r\n" +
		"x"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write partial request: %v", err)
	}

	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read timeout response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if !response.Close {
		t.Fatal("slow request connection remained reusable")
	}
}

type readDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (rec *readDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	rec.deadlines = append(rec.deadlines, deadline)
	return nil
}

func TestRequestBodyReadTimeoutMiddlewareOnlyBoundsPOSTAndClearsDeadline(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := requestBodyReadTimeoutMiddleware(next, time.Second)

	getRecorder := &readDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/sse", nil))
	if len(getRecorder.deadlines) != 0 {
		t.Fatalf("GET deadlines=%v, want none", getRecorder.deadlines)
	}

	postRecorder := &readDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(postRecorder, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}")))
	if len(postRecorder.deadlines) != 2 {
		t.Fatalf("POST deadlines=%v, want set and clear", postRecorder.deadlines)
	}
	if postRecorder.deadlines[0].IsZero() || !postRecorder.deadlines[1].IsZero() {
		t.Fatalf("POST deadlines=%v, want non-zero then zero", postRecorder.deadlines)
	}
}

func TestRequestBodyReadTimeoutMiddlewareKeepsCompletedPOSTConnectionReusable(t *testing.T) {
	handler := requestBodyReadTimeoutMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}), 25*time.Millisecond)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: test\r\nContent-Length: 2\r\n\r\n{}"); err != nil {
		t.Fatalf("write POST: %v", err)
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read POST response: %v", err)
	}
	_ = response.Body.Close()

	time.Sleep(50 * time.Millisecond)
	if _, err := io.WriteString(conn, "GET /sse HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatalf("reuse connection for GET: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	response, err = http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read GET response on reused connection: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("GET status=%d, want %d", response.StatusCode, http.StatusNoContent)
	}
}
