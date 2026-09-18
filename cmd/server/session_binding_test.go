package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wuxujun/xktmcp/internal/auth"
)

func authenticatedSessionIdentity(t *testing.T, token string) auth.SessionIdentity {
	t.Helper()
	a, err := auth.New(auth.Config{LocalToken: token})
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}

	var identity auth.SessionIdentity
	var ok bool
	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok = auth.SessionIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !ok {
		t.Fatal("authenticated request has no session identity")
	}
	return identity
}

func authenticatedBindingHandler(t *testing.T, token string, next http.Handler) http.Handler {
	t.Helper()
	a, err := auth.New(auth.Config{LocalToken: token})
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	return a.Middleware(next)
}

func authenticatedBindingRequest(method, token, sessionID string) *http.Request {
	req := httptest.NewRequest(method, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	return req
}

func TestSessionBindingsFirstBindAndMatch(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")

	if !bindings.bind(streamableSessionTransport, "session-1", identity) {
		t.Fatal("first bind failed")
	}
	if !bindings.matches(streamableSessionTransport, "session-1", identity) {
		t.Fatal("bound identity did not match")
	}
}

func TestSessionBindingsRejectDifferentAndMissingIdentity(t *testing.T) {
	bindings := newSessionBindings()
	idA := authenticatedSessionIdentity(t, "token-a")
	idB := authenticatedSessionIdentity(t, "token-b")
	if !bindings.bind(streamableSessionTransport, "session-1", idA) {
		t.Fatal("initial bind failed")
	}
	if bindings.bind(streamableSessionTransport, "session-1", idB) {
		t.Fatal("different identity replaced an existing binding")
	}
	if bindings.matches(streamableSessionTransport, "session-1", idB) {
		t.Fatal("different identity matched")
	}
	if bindings.matches(streamableSessionTransport, "unknown", idA) {
		t.Fatal("missing binding matched")
	}
	if bindings.matches(streamableSessionTransport, "session-1", auth.SessionIdentity{}) {
		t.Fatal("missing identity matched")
	}
}

func TestSessionBindingsConcurrentFirstBindNeverOverwrites(t *testing.T) {
	idA := authenticatedSessionIdentity(t, "token-a")
	idB := authenticatedSessionIdentity(t, "token-b")

	for iteration := 0; iteration < 100; iteration++ {
		bindings := newSessionBindings()
		start := make(chan struct{})
		var ready sync.WaitGroup
		var done sync.WaitGroup
		ready.Add(2)
		done.Add(2)
		for _, identity := range []auth.SessionIdentity{idA, idB} {
			go func(identity auth.SessionIdentity) {
				defer done.Done()
				ready.Done()
				<-start
				bindings.bind(streamableSessionTransport, "session-1", identity)
			}(identity)
		}
		ready.Wait()
		close(start)
		done.Wait()

		matchesA := bindings.matches(streamableSessionTransport, "session-1", idA)
		matchesB := bindings.matches(streamableSessionTransport, "session-1", idB)
		if matchesA == matchesB {
			t.Fatalf("iteration %d: matches A=%t, B=%t; want exactly one winner", iteration, matchesA, matchesB)
		}
	}
}

type observingResponseWriter struct {
	header        http.Header
	status        int
	body          bytes.Buffer
	onWriteHeader func(int)
	flushed       bool
}

func newObservingResponseWriter() *observingResponseWriter {
	return &observingResponseWriter{header: make(http.Header)}
}

func (w *observingResponseWriter) Header() http.Header { return w.header }

func (w *observingResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if w.onWriteHeader != nil {
		w.onWriteHeader(status)
	}
	w.status = status
}

func (w *observingResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}

func (w *observingResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.flushed = true
}

func TestStreamableSessionBindingMiddlewareBindsBeforeInitializationResponse(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")
	writer := newObservingResponseWriter()
	writer.onWriteHeader = func(int) {
		if !bindings.matches(streamableSessionTransport, "session-1", identity) {
			t.Error("response header was forwarded before the session was bound")
		}
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", "session-1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("initialized"))
	})
	handler := authenticatedBindingHandler(t, "token-a", streamableSessionBindingMiddleware(next, bindings))

	handler.ServeHTTP(writer, authenticatedBindingRequest(http.MethodPost, "token-a", ""))

	if writer.status != http.StatusOK || writer.body.String() != "initialized" {
		t.Fatalf("response = (%d, %q), want (200, %q)", writer.status, writer.body.String(), "initialized")
	}
}

func TestStreamableSessionBindingMiddlewareFlushBindsBeforeForwarding(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")
	writer := newObservingResponseWriter()
	writer.onWriteHeader = func(int) {
		if !bindings.matches(streamableSessionTransport, "session-1", identity) {
			t.Error("flush forwarded headers before the session was bound")
		}
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", "session-1")
		w.(http.Flusher).Flush()
	})
	handler := authenticatedBindingHandler(t, "token-a", streamableSessionBindingMiddleware(next, bindings))

	handler.ServeHTTP(writer, authenticatedBindingRequest(http.MethodPost, "token-a", ""))

	if !writer.flushed {
		t.Fatal("underlying writer was not flushed")
	}
}

func TestStreamableSessionBindingMiddlewareSuppressesWritesAfterFailedBind(t *testing.T) {
	bindings := newSessionBindings()
	idA := authenticatedSessionIdentity(t, "token-a")
	if !bindings.bind(streamableSessionTransport, "session-1", idA) {
		t.Fatal("initial bind failed")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", "session-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("sdk response must be discarded"))
	})
	handler := authenticatedBindingHandler(t, "token-b", streamableSessionBindingMiddleware(next, bindings))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, authenticatedBindingRequest(http.MethodPost, "token-b", ""))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if got := recorder.Header().Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("response exposed rejected session ID %q", got)
	}
	if got := recorder.Body.String(); got != "Forbidden\n" {
		t.Fatalf("body = %q, want only the rejection body", got)
	}
}

func TestStreamableSessionBindingMiddlewareAllowsMatchingSessionRequests(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			bindings := newSessionBindings()
			identity := authenticatedSessionIdentity(t, "token-a")
			if !bindings.bind(streamableSessionTransport, "session-1", identity) {
				t.Fatal("initial bind failed")
			}
			var called atomic.Bool
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called.Store(true)
				if !bindings.matches(streamableSessionTransport, "session-1", identity) {
					t.Error("binding was deleted before the accepted request reached the handler")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			handler := authenticatedBindingHandler(t, "token-a", streamableSessionBindingMiddleware(next, bindings))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, authenticatedBindingRequest(method, "token-a", "session-1"))

			if !called.Load() || recorder.Code != http.StatusNoContent {
				t.Fatalf("called=%t status=%d, want true and 204", called.Load(), recorder.Code)
			}
			wantBound := method != http.MethodDelete
			if got := bindings.matches(streamableSessionTransport, "session-1", identity); got != wantBound {
				t.Fatalf("binding present=%t after %s, want %t", got, method, wantBound)
			}
		})
	}
}

func TestStreamableSessionBindingMiddlewareRejectsInvalidSessionRequests(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		sessionID string
		withAuth  bool
		bindFirst bool
		method    string
	}{
		{name: "different identity", token: "token-b", sessionID: "session-1", withAuth: true, bindFirst: true, method: http.MethodPost},
		{name: "unknown session", token: "token-a", sessionID: "unknown", withAuth: true, bindFirst: true, method: http.MethodGet},
		{name: "missing identity", sessionID: "session-1", bindFirst: true, method: http.MethodPost},
		{name: "rejected delete preserves binding", token: "token-b", sessionID: "session-1", withAuth: true, bindFirst: true, method: http.MethodDelete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := newSessionBindings()
			idA := authenticatedSessionIdentity(t, "token-a")
			if tt.bindFirst && !bindings.bind(streamableSessionTransport, "session-1", idA) {
				t.Fatal("initial bind failed")
			}
			var called atomic.Bool
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called.Store(true)
				w.WriteHeader(http.StatusNoContent)
			})
			handler := streamableSessionBindingMiddleware(next, bindings)
			if tt.withAuth {
				handler = authenticatedBindingHandler(t, tt.token, handler)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, authenticatedBindingRequest(tt.method, tt.token, tt.sessionID))

			if recorder.Code != http.StatusForbidden || called.Load() {
				t.Fatalf("status=%d called=%t, want 403 and false", recorder.Code, called.Load())
			}
			if tt.method == http.MethodDelete && !bindings.matches(streamableSessionTransport, "session-1", idA) {
				t.Fatal("rejected DELETE removed the existing binding")
			}
		})
	}
}

func TestStreamableSessionBindingMiddlewareDoesNotBindResponseWithoutSessionHeader(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := authenticatedBindingHandler(t, "token-a", streamableSessionBindingMiddleware(next, bindings))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, authenticatedBindingRequest(http.MethodPost, "token-a", ""))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if bindings.matches(streamableSessionTransport, "session-1", identity) {
		t.Fatal("response without a session header created a binding")
	}
}

func TestStreamableSessionBindingMiddlewareWriterUnwrapsUnderlyingWriter(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapper := newStreamableBindingWriter(underlying, newSessionBindings(), auth.SessionIdentity{}, false)
	if got := wrapper.Unwrap(); got != underlying {
		t.Fatalf("Unwrap() = %T %p, want underlying %T %p", got, got, underlying, underlying)
	}
}
