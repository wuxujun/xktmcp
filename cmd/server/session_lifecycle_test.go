package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sessionLifecycleHandler(t *testing.T, ttl string, capacity string) (*mcp.Server, *sessionBindings, http.Handler) {
	t.Helper()
	t.Setenv("MCP_SESSION_IDLE_TTL", ttl)
	t.Setenv("MCP_SESSION_BINDINGS_MAX_ENTRIES", capacity)
	server := mcp.NewServer(&mcp.Implementation{Name: "lifecycle-test", Version: "1.0.0"}, nil)
	bindings := newSessionBindings(server)
	t.Cleanup(func() {
		bindings.Stop()
		for session := range server.Sessions() {
			_ = session.Close()
		}
	})
	handler := authenticatedBindingHandler(t, "lifecycle-token", streamableSessionBindingMiddleware(newStreamableHTTPHandler(server), bindings))
	return server, bindings, handler
}

func initializeLifecycleSession(t *testing.T, handler http.Handler) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"lifecycle-client","version":"1.0.0"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer lifecycle-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", rec.Code, rec.Body.String())
	}
	id := rec.Header().Get("Mcp-Session-Id")
	if id == "" {
		t.Fatal("initialize response has no session ID")
	}
	return id
}

func lifecycleSessionIDs(server *mcp.Server) []string {
	var ids []string
	for session := range server.Sessions() {
		ids = append(ids, session.ID())
	}
	return ids
}

func TestStreamableSessionLRUEvictionClosesSDKSession(t *testing.T) {
	server, bindings, handler := sessionLifecycleHandler(t, "1h", "1")
	first := initializeLifecycleSession(t, handler)
	second := initializeLifecycleSession(t, handler)
	if first == second {
		t.Fatal("initialize reused a session ID")
	}
	if ids := lifecycleSessionIDs(server); len(ids) != 1 || ids[0] != second {
		t.Fatalf("SDK sessions=%v, want only %q", ids, second)
	}
	if bindings.len() != 1 {
		t.Fatalf("bindings=%d, want 1", bindings.len())
	}
	for _, tc := range []struct {
		id   string
		code int
	}{{first, http.StatusForbidden}, {second, http.StatusAccepted}} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
		setLegacyMCPHeaders(req, tc.id)
		req.Header.Set("Authorization", "Bearer lifecycle-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("session %q status=%d, want %d", tc.id, rec.Code, tc.code)
		}
	}
}

func TestStreamableSessionBindingExpirationClosesSDKSession(t *testing.T) {
	for _, cleanup := range []string{"lazy_lookup", "janitor"} {
		t.Run(cleanup, func(t *testing.T) {
			server, bindings, handler := sessionLifecycleHandler(t, "1h", "4")
			id := initializeLifecycleSession(t, handler)
			if ids := lifecycleSessionIDs(server); len(ids) != 1 {
				t.Fatalf("SDK sessions=%v, want one initialized session", ids)
			}
			future := time.Now().Add(2 * time.Hour)
			bindings.mu.Lock()
			bindings.now = func() time.Time { return future }
			bindings.mu.Unlock()
			if cleanup == "lazy_lookup" {
				identity := authenticatedSessionIdentity(t, "lifecycle-token")
				if bindings.matches(streamableSessionTransport, id, identity) {
					t.Fatal("expired binding accepted")
				}
			} else {
				bindings.deleteExpired()
			}
			if ids := lifecycleSessionIDs(server); len(ids) != 0 {
				t.Fatalf("expired SDK sessions remain: %v", ids)
			}
			if bindings.len() != 0 {
				t.Fatalf("expired bindings remain: %d", bindings.len())
			}
		})
	}
}

func TestStreamableSessionIdleTimeoutClosesSDKSession(t *testing.T) {
	server, _, handler := sessionLifecycleHandler(t, "50ms", "4")
	initializeLifecycleSession(t, handler)
	deadline := time.Now().Add(2 * time.Second)
	for len(lifecycleSessionIDs(server)) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("SDK idle timeout did not close abandoned session")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSessionBindingsCloseOutsideLock(t *testing.T) {
	identity := authenticatedSessionIdentity(t, "token-a")
	now := time.Now()
	bindings := newSessionBindingsWithOptions(1, time.Minute, 0, func() time.Time { return now })
	defer bindings.Stop()
	var closed []sessionBindingKey
	bindings.closeSession = func(key sessionBindingKey) {
		_ = bindings.len() // 锁内调用会死锁。
		closed = append(closed, key)
	}
	bindings.bind(streamableSessionTransport, "first", identity)
	bindings.bind(streamableSessionTransport, "second", identity)
	now = now.Add(2 * time.Minute)
	if bindings.bind(streamableSessionTransport, "second", identity) {
		t.Fatal("expired session was rebound")
	}
	if len(closed) != 2 || closed[0].sessionID != "first" || closed[1].sessionID != "second" {
		t.Fatalf("closed sessions=%v, want first and second", closed)
	}
	if bindings.len() != 0 {
		t.Fatalf("bindings=%d, want 0", bindings.len())
	}
}

func TestStreamableSessionResponseDoesNotRebindEvictedSession(t *testing.T) {
	bindings := newSessionBindingsWithOptions(1, time.Hour, 0, time.Now)
	defer bindings.Stop()
	identity := authenticatedSessionIdentity(t, "token-a")
	bindings.bind(streamableSessionTransport, "old", identity)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bindings.bind(streamableSessionTransport, "new", identity)
		w.Header().Set("Mcp-Session-Id", "old")
		w.WriteHeader(http.StatusOK)
	})
	handler := authenticatedBindingHandler(t, "token-a", streamableSessionBindingMiddleware(next, bindings))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer token-a")
	req.Header.Set("Mcp-Session-Id", "old")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("response status=%d, want 403", rec.Code)
	}
	if bindings.matches(streamableSessionTransport, "old", identity) || !bindings.matches(streamableSessionTransport, "new", identity) {
		t.Fatal("in-flight response resurrected evicted session or evicted replacement")
	}
}

func TestSSESessionCleanupClosesConnection(t *testing.T) {
	for _, cleanup := range []string{"lru", "lazy_lookup", "janitor"} {
		t.Run(cleanup, func(t *testing.T) {
			t.Setenv("MCP_SESSION_IDLE_TTL", "1h")
			t.Setenv("MCP_SESSION_BINDINGS_MAX_ENTRIES", "1")
			server := mcp.NewServer(&mcp.Implementation{Name: "sse-lifecycle-test", Version: "1.0.0"}, nil)
			bindings := newSessionBindings(server)
			sdkHandler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			handler := authenticatedBindingHandler(t, "lifecycle-token", sseSessionBindingMiddleware(sdkHandler, bindings))
			ts := httptest.NewServer(handler)
			t.Cleanup(func() { ts.Close(); bindings.Stop() })
			connect := func() (string, *bufio.Reader) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				t.Cleanup(cancel)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/sse", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer lifecycle-token")
				res, err := ts.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = res.Body.Close() })
				if res.StatusCode != http.StatusOK {
					t.Fatalf("SSE status=%d, want 200", res.StatusCode)
				}
				reader := bufio.NewReader(res.Body)
				var event strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					event.WriteString(line)
					if strings.TrimSpace(line) == "" {
						break
					}
				}
				id, err := parseSSEEndpointSessionID([]byte(event.String()))
				if err != nil {
					t.Fatal(err)
				}
				return id, reader
			}
			waitSessions := func(want int) {
				t.Helper()
				deadline := time.Now().Add(2 * time.Second)
				for len(lifecycleSessionIDs(server)) != want {
					if time.Now().After(deadline) {
						t.Fatalf("SDK session count=%d, want %d", len(lifecycleSessionIDs(server)), want)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			id, reader := connect()
			waitSessions(1)
			identity := authenticatedSessionIdentity(t, "lifecycle-token")
			wantSessions := 0
			if cleanup == "lru" {
				second, _ := connect()
				wantSessions = 1
				if !bindings.matches(sseSessionTransport, second, identity) {
					t.Fatal("replacement SSE binding was removed")
				}
			} else {
				future := time.Now().Add(2 * time.Hour)
				bindings.mu.Lock()
				bindings.now = func() time.Time { return future }
				bindings.mu.Unlock()
				if cleanup == "lazy_lookup" {
					if bindings.matches(sseSessionTransport, id, identity) {
						t.Fatal("expired SSE binding accepted")
					}
				} else {
					bindings.deleteExpired()
				}
			}
			closed := make(chan error, 1)
			go func() { _, err := reader.ReadByte(); closed <- err }()
			select {
			case err := <-closed:
				if err != io.EOF {
					t.Fatalf("SSE read after cleanup=%v, want EOF", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("evicted SSE connection remained open")
			}
			waitSessions(wantSessions)
			if bindings.matches(sseSessionTransport, id, identity) {
				t.Fatal("closed SSE session remained bound")
			}
		})
	}
}
