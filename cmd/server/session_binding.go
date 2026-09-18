package main

import (
	"net/http"
	"strings"
	"sync"

	"github.com/wuxujun/xktmcp/internal/auth"
)

type sessionTransport string

const (
	streamableSessionTransport sessionTransport = "streamable"
	sseSessionTransport        sessionTransport = "sse"
)

type sessionBindingKey struct {
	transport sessionTransport
	sessionID string
}

type sessionBindings struct {
	mu    sync.Mutex
	items map[sessionBindingKey]auth.SessionIdentity
}

func newSessionBindings() *sessionBindings {
	return &sessionBindings{items: make(map[sessionBindingKey]auth.SessionIdentity)}
}

func (b *sessionBindings) bind(transport sessionTransport, sessionID string, identity auth.SessionIdentity) bool {
	if sessionID == "" || !identity.Equal(identity) {
		return false
	}

	key := sessionBindingKey{transport: transport, sessionID: sessionID}
	b.mu.Lock()
	bound, exists := b.items[key]
	if !exists {
		b.items[key] = identity
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()
	return bound.Equal(identity)
}

func (b *sessionBindings) matches(transport sessionTransport, sessionID string, identity auth.SessionIdentity) bool {
	key := sessionBindingKey{transport: transport, sessionID: sessionID}
	b.mu.Lock()
	bound, exists := b.items[key]
	b.mu.Unlock()
	return exists && bound.Equal(identity)
}

func (b *sessionBindings) delete(transport sessionTransport, sessionID string) {
	key := sessionBindingKey{transport: transport, sessionID: sessionID}
	b.mu.Lock()
	delete(b.items, key)
	b.mu.Unlock()
}

type streamableBindingWriter struct {
	http.ResponseWriter
	bindings    *sessionBindings
	identity    auth.SessionIdentity
	hasIdentity bool
	committed   bool
	blocked     bool
}

func newStreamableBindingWriter(
	w http.ResponseWriter,
	bindings *sessionBindings,
	identity auth.SessionIdentity,
	hasIdentity bool,
) *streamableBindingWriter {
	return &streamableBindingWriter{
		ResponseWriter: w,
		bindings:       bindings,
		identity:       identity,
		hasIdentity:    hasIdentity,
	}
}

func (w *streamableBindingWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *streamableBindingWriter) WriteHeader(statusCode int) {
	if w.committed {
		return
	}
	w.committed = true

	sessionID := strings.TrimSpace(w.Header().Get("Mcp-Session-Id"))
	if sessionID == "" {
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if !w.hasIdentity || !w.bindings.bind(streamableSessionTransport, sessionID, w.identity) {
		w.blocked = true
		w.Header().Del("Mcp-Session-Id")
		http.Error(w.ResponseWriter, "Forbidden", http.StatusForbidden)
		return
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *streamableBindingWriter) Write(p []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if w.blocked {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

func (w *streamableBindingWriter) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *streamableBindingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func streamableSessionBindingMiddleware(next http.Handler, bindings *sessionBindings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, hasIdentity := auth.SessionIdentityFromContext(r.Context())
		sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
		if sessionID != "" && (!hasIdentity || !bindings.matches(streamableSessionTransport, sessionID, identity)) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodDelete && sessionID != "" {
			defer bindings.delete(streamableSessionTransport, sessionID)
		}
		next.ServeHTTP(newStreamableBindingWriter(w, bindings, identity, hasIdentity), r)
	})
}
