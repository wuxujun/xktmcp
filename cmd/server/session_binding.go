package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/wuxujun/xktmcp/internal/auth"
	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/pii"
)

type sessionTransport string

const (
	streamableSessionTransport sessionTransport = "streamable"
	sseSessionTransport        sessionTransport = "sse"
	maxSSEEndpointEventBytes                    = 4096
)

type sseRejectionReason string

const (
	sseRejectInvalidEndpointEvent sseRejectionReason = "invalid_endpoint_event"
	sseRejectOversizedEvent       sseRejectionReason = "endpoint_event_too_large"
	sseRejectMissingSessionID     sseRejectionReason = "missing_session_id"
	sseRejectMissingIdentity      sseRejectionReason = "missing_identity"
	sseRejectSessionBinding       sseRejectionReason = "session_binding_failed"
	sseRejectMissingEndpointEvent sseRejectionReason = "missing_endpoint_event"
)

var (
	errInvalidSSEEndpointEvent = errors.New("invalid SSE endpoint event")
	errSSEEndpointNoSessionID  = errors.New("SSE endpoint event has no session ID")
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
	statusCode  int
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
		w.statusCode = statusCode
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if !w.hasIdentity || !w.bindings.bind(streamableSessionTransport, sessionID, w.identity) {
		w.blocked = true
		w.statusCode = http.StatusForbidden
		w.Header().Del("Mcp-Session-Id")
		http.Error(w.ResponseWriter, "Forbidden", http.StatusForbidden)
		return
	}
	w.statusCode = statusCode
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
		writer := newStreamableBindingWriter(w, bindings, identity, hasIdentity)
		next.ServeHTTP(writer, r)
		if r.Method == http.MethodDelete && sessionID != "" &&
			(writer.statusCode == 0 || writer.statusCode >= 200 && writer.statusCode < 300) {
			bindings.delete(streamableSessionTransport, sessionID)
		}
	})
}

type sseEndpointBindingWriter struct {
	http.ResponseWriter
	bindings    *sessionBindings
	identity    auth.SessionIdentity
	hasIdentity bool
	buffer      []byte
	statusCode  int
	sessionID   string
	ready       bool
	blocked     bool
}

func newSSEEndpointBindingWriter(
	w http.ResponseWriter,
	bindings *sessionBindings,
	identity auth.SessionIdentity,
	hasIdentity bool,
) *sseEndpointBindingWriter {
	return &sseEndpointBindingWriter{
		ResponseWriter: w,
		bindings:       bindings,
		identity:       identity,
		hasIdentity:    hasIdentity,
		buffer:         make([]byte, 0, maxSSEEndpointEventBytes),
	}
}

func (w *sseEndpointBindingWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *sseEndpointBindingWriter) WriteHeader(statusCode int) {
	if w.blocked || w.ready || w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
}

func (w *sseEndpointBindingWriter) Write(p []byte) (int, error) {
	if w.blocked {
		return len(p), nil
	}
	if w.ready {
		return w.writeResponse(p)
	}

	bufferedBefore := len(w.buffer)
	for i, b := range p {
		w.buffer = append(w.buffer, b)
		if len(w.buffer) > maxSSEEndpointEventBytes {
			w.reject("", sseRejectOversizedEvent)
			return len(p), nil
		}
		if !hasSSEEventBoundary(w.buffer) {
			continue
		}

		sessionID, err := parseSSEEndpointSessionID(w.buffer)
		if err != nil {
			reason := sseRejectInvalidEndpointEvent
			if errors.Is(err, errSSEEndpointNoSessionID) {
				reason = sseRejectMissingSessionID
			}
			w.reject("", reason)
			return len(p), nil
		}
		if !w.hasIdentity {
			w.reject(sessionID, sseRejectMissingIdentity)
			return len(p), nil
		}
		if !w.bindings.bind(sseSessionTransport, sessionID, w.identity) {
			w.reject(sessionID, sseRejectSessionBinding)
			return len(p), nil
		}

		w.sessionID = sessionID
		w.commitStatus()
		if n, err := w.writeResponse(w.buffer); err != nil {
			n -= bufferedBefore
			if n < 0 {
				n = 0
			}
			if n > i+1 {
				n = i + 1
			}
			return n, err
		}
		w.buffer = nil
		w.ready = true
		if i+1 < len(p) {
			n, err := w.writeResponse(p[i+1:])
			return i + 1 + n, err
		}
		return len(p), nil
	}

	return len(p), nil
}

func (w *sseEndpointBindingWriter) writeResponse(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.blocked = true
		w.ready = false
		w.buffer = nil
	}
	return n, err
}

func (w *sseEndpointBindingWriter) Flush() {
	if w.blocked || !w.ready {
		return
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *sseEndpointBindingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *sseEndpointBindingWriter) commitStatus() {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.statusCode)
}

func (w *sseEndpointBindingWriter) reject(sessionID string, reason sseRejectionReason) {
	logger.Errorf(
		"SSE session rejected: transport=%s session_id=%s reason=%s",
		sseSessionTransport,
		pii.MaskSubject(sessionID),
		reason,
	)
	w.blocked = true
	w.buffer = nil
	w.Header().Del("Cache-Control")
	w.Header().Del("Connection")
	w.Header().Del("Content-Type")
	http.Error(w.ResponseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func hasSSEEventBoundary(event []byte) bool {
	return bytes.HasSuffix(event, []byte("\n\n")) || bytes.HasSuffix(event, []byte("\r\n\r\n"))
}

func parseSSEEndpointSessionID(event []byte) (string, error) {
	normalized := strings.ReplaceAll(string(event), "\r\n", "\n")
	var eventName, data string
	dataLines := 0
	for _, line := range strings.Split(strings.TrimSuffix(normalized, "\n\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			dataLines++
		}
	}
	if eventName != "endpoint" || data == "" || dataLines != 1 {
		return "", errInvalidSSEEndpointEvent
	}
	u, err := url.Parse(data)
	if err != nil {
		return "", errSSEEndpointNoSessionID
	}
	sessionID := strings.TrimSpace(u.Query().Get("sessionid"))
	if sessionID == "" {
		return "", errSSEEndpointNoSessionID
	}
	return sessionID, nil
}

func sseSessionBindingMiddleware(next http.Handler, bindings *sessionBindings) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, hasIdentity := auth.SessionIdentityFromContext(r.Context())
		if r.Method == http.MethodPost {
			sessionID := strings.TrimSpace(r.URL.Query().Get("sessionid"))
			if sessionID == "" || !hasIdentity || !bindings.matches(sseSessionTransport, sessionID, identity) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		writer := newSSEEndpointBindingWriter(w, bindings, identity, hasIdentity)
		defer func() {
			if writer.sessionID != "" {
				bindings.delete(sseSessionTransport, writer.sessionID)
			}
		}()
		next.ServeHTTP(writer, r)
		if !writer.ready && !writer.blocked {
			writer.reject("", sseRejectMissingEndpointEvent)
		}
	})
}
