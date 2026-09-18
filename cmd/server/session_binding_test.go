package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wuxujun/xktmcp/internal/auth"
	"github.com/wuxujun/xktmcp/internal/logger"
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
	onWrite       func([]byte)
	flushed       bool
}

type scriptedWriteResult struct {
	n   int
	err error
}

type scriptedResponseWriter struct {
	header       http.Header
	body         bytes.Buffer
	writeResults []scriptedWriteResult
	writeCalls   int
	flushed      bool
}

func newScriptedResponseWriter(results ...scriptedWriteResult) *scriptedResponseWriter {
	return &scriptedResponseWriter{
		header:       make(http.Header),
		writeResults: results,
	}
}

func (w *scriptedResponseWriter) Header() http.Header { return w.header }

func (w *scriptedResponseWriter) WriteHeader(int) {}

func (w *scriptedResponseWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	if len(w.writeResults) == 0 {
		return w.body.Write(p)
	}
	result := w.writeResults[0]
	w.writeResults = w.writeResults[1:]
	if result.n > len(p) {
		result.n = len(p)
	}
	_, _ = w.body.Write(p[:result.n])
	return result.n, result.err
}

func (w *scriptedResponseWriter) Flush() {
	w.flushed = true
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
	if w.onWrite != nil {
		w.onWrite(p)
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

func TestStreamableSessionBindingMiddlewareDeletesBindingOnlyForAcceptedDelete(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantBound bool
	}{
		{name: "downstream rejection", status: http.StatusBadRequest, wantBound: true},
		{name: "implicit success", wantBound: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := newSessionBindings()
			identity := authenticatedSessionIdentity(t, "token-a")
			if !bindings.bind(streamableSessionTransport, "session-1", identity) {
				t.Fatal("initial bind failed")
			}
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
			})
			handler := authenticatedBindingHandler(t, "token-a", streamableSessionBindingMiddleware(next, bindings))

			handler.ServeHTTP(httptest.NewRecorder(), authenticatedBindingRequest(http.MethodDelete, "token-a", "session-1"))

			if got := bindings.matches(streamableSessionTransport, "session-1", identity); got != tt.wantBound {
				t.Fatalf("binding present=%t after DELETE status %d, want %t", got, tt.status, tt.wantBound)
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

func TestSSEEndpointBindingWriterBuffersSplitCRLFEventAndBindsBeforeForwarding(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")
	underlying := newObservingResponseWriter()
	underlying.onWrite = func([]byte) {
		if !bindings.matches(sseSessionTransport, "sse-1", identity) {
			t.Error("endpoint event was forwarded before the session was bound")
		}
	}
	writer := newSSEEndpointBindingWriter(underlying, bindings, identity, true)

	first := "event: endpoint\r\ndata: /messages/?sessionid="
	second := "sse-1\r\n\r\nevent: message\ndata: trailing\n\n"
	if n, err := io.WriteString(writer, first); err != nil || n != len(first) {
		t.Fatalf("first Write() = (%d, %v), want (%d, nil)", n, err, len(first))
	}
	if underlying.status != 0 || underlying.body.Len() != 0 {
		t.Fatalf("partial event exposed status=%d body=%q", underlying.status, underlying.body.String())
	}
	if n, err := io.WriteString(writer, second); err != nil || n != len(second) {
		t.Fatalf("second Write() = (%d, %v), want (%d, nil)", n, err, len(second))
	}
	want := first + second
	if got := underlying.body.String(); got != want {
		t.Fatalf("body immediately after readiness = %q, want %q", got, want)
	}

	later := "event: message\ndata: later\n\n"
	if n, err := io.WriteString(writer, later); err != nil || n != len(later) {
		t.Fatalf("later Write() = (%d, %v), want (%d, nil)", n, err, len(later))
	}
	want += later
	if got := underlying.body.String(); got != want {
		t.Fatalf("body immediately after later write = %q, want %q", got, want)
	}
	if !bindings.matches(sseSessionTransport, "sse-1", identity) {
		t.Fatal("complete endpoint event did not create a binding")
	}
	if got := writer.Unwrap(); got != underlying {
		t.Fatalf("Unwrap() = %T %p, want underlying %T %p", got, got, underlying, underlying)
	}
}

func TestSSEEndpointBindingWriterBlocksAfterIncompleteEndpointWrite(t *testing.T) {
	endpoint := "event: endpoint\ndata: /messages/?sessionid=sse-1\n\n"
	trailing := "event: message\ndata: trailing\n\n"
	writeErr := errors.New("endpoint write failed")

	tests := []struct {
		name     string
		result   scriptedWriteResult
		wantN    int
		wantErr  error
		wantBody string
	}{
		{
			name:     "nil error short write",
			result:   scriptedWriteResult{n: len(endpoint) - 1},
			wantN:    len(endpoint) - 1,
			wantErr:  io.ErrShortWrite,
			wantBody: endpoint[:len(endpoint)-1],
		},
		{
			name:     "short write with error",
			result:   scriptedWriteResult{n: len(endpoint) - 1, err: writeErr},
			wantN:    len(endpoint) - 1,
			wantErr:  writeErr,
			wantBody: endpoint[:len(endpoint)-1],
		},
		{
			name:     "full write with error",
			result:   scriptedWriteResult{n: len(endpoint), err: writeErr},
			wantN:    len(endpoint),
			wantErr:  writeErr,
			wantBody: endpoint,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underlying := newScriptedResponseWriter(tt.result)
			writer := newSSEEndpointBindingWriter(
				underlying,
				newSessionBindings(),
				authenticatedSessionIdentity(t, "token-a"),
				true,
			)

			n, err := io.WriteString(writer, endpoint+trailing)
			if n != tt.wantN || !errors.Is(err, tt.wantErr) {
				t.Fatalf("Write() = (%d, %v), want (%d, %v)", n, err, tt.wantN, tt.wantErr)
			}
			if len(writer.buffer) != 0 {
				t.Fatalf("retained buffer length = %d, want 0", len(writer.buffer))
			}

			later := "event: message\ndata: later\n\n"
			if n, err := io.WriteString(writer, later); n != len(later) || err != nil {
				t.Fatalf("blocked Write() = (%d, %v), want (%d, nil)", n, err, len(later))
			}
			writer.Flush()
			if underlying.writeCalls != 1 || underlying.flushed {
				t.Fatalf("after failure: write calls=%d flushed=%t, want 1 and false", underlying.writeCalls, underlying.flushed)
			}
			if got := underlying.body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want only endpoint bytes %q", got, tt.wantBody)
			}
		})
	}
}

func TestSSEEndpointBindingWriterBlocksAfterPostEndpointWriteFailure(t *testing.T) {
	endpoint := "event: endpoint\ndata: /messages/?sessionid=sse-1\n\n"
	trailing := "event: message\ndata: trailing\n\n"
	direct := "event: message\ndata: direct\n\n"
	writeErr := errors.New("direct write failed")

	tests := []struct {
		name        string
		firstWrite  string
		secondWrite string
		results     []scriptedWriteResult
		wantN       int
		wantErr     error
		wantBody    string
	}{
		{
			name:       "nil error short trailing write",
			firstWrite: endpoint + trailing,
			results: []scriptedWriteResult{
				{n: len(endpoint)},
				{n: len(trailing) - 1},
			},
			wantN:    len(endpoint) + len(trailing) - 1,
			wantErr:  io.ErrShortWrite,
			wantBody: endpoint + trailing[:len(trailing)-1],
		},
		{
			name:        "direct write error",
			firstWrite:  endpoint,
			secondWrite: direct,
			results: []scriptedWriteResult{
				{n: len(endpoint)},
				{n: len(direct), err: writeErr},
			},
			wantN:    len(direct),
			wantErr:  writeErr,
			wantBody: endpoint + direct,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underlying := newScriptedResponseWriter(tt.results...)
			writer := newSSEEndpointBindingWriter(
				underlying,
				newSessionBindings(),
				authenticatedSessionIdentity(t, "token-a"),
				true,
			)

			n, err := io.WriteString(writer, tt.firstWrite)
			if tt.secondWrite != "" {
				if n != len(endpoint) || err != nil {
					t.Fatalf("endpoint Write() = (%d, %v), want (%d, nil)", n, err, len(endpoint))
				}
				n, err = io.WriteString(writer, tt.secondWrite)
			}
			if n != tt.wantN || !errors.Is(err, tt.wantErr) {
				t.Fatalf("failed Write() = (%d, %v), want (%d, %v)", n, err, tt.wantN, tt.wantErr)
			}
			if len(writer.buffer) != 0 {
				t.Fatalf("retained buffer length = %d, want 0", len(writer.buffer))
			}

			later := "event: message\ndata: later\n\n"
			if n, err := io.WriteString(writer, later); n != len(later) || err != nil {
				t.Fatalf("blocked Write() = (%d, %v), want (%d, nil)", n, err, len(later))
			}
			writer.Flush()
			if underlying.writeCalls != 2 || underlying.flushed {
				t.Fatalf("after failure: write calls=%d flushed=%t, want 2 and false", underlying.writeCalls, underlying.flushed)
			}
			if got := underlying.body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestSSEEndpointBindingWriterFlushDoesNotExposePartialEndpoint(t *testing.T) {
	underlying := newObservingResponseWriter()
	writer := newSSEEndpointBindingWriter(
		underlying,
		newSessionBindings(),
		authenticatedSessionIdentity(t, "token-a"),
		true,
	)
	partial := "event: endpoint\ndata: /messages/?sessionid=sse-1\n"

	if _, err := io.WriteString(writer, partial); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	writer.Flush()

	if underlying.status != 0 || underlying.body.Len() != 0 || underlying.flushed {
		t.Fatalf("Flush exposed partial event: status=%d body=%q flushed=%t", underlying.status, underlying.body.String(), underlying.flushed)
	}
}

func TestSSEEndpointBindingWriterRejectsInvalidFirstEventAndLaterWrites(t *testing.T) {
	tests := []struct {
		name           string
		event          string
		hasIdentity    bool
		bindFirst      bool
		wantLogMessage string
	}{
		{
			name:           "missing session ID",
			event:          "event: endpoint\ndata: /messages/?token=endpoint-secret\n\n",
			hasIdentity:    true,
			wantLogMessage: "SSE session rejected: transport=sse session_id=(empty) reason=missing_session_id",
		},
		{
			name:           "wrong event name",
			event:          "event: message\ndata: /messages/?sessionid=sse-1&token=endpoint-secret\n\n",
			hasIdentity:    true,
			wantLogMessage: "SSE session rejected: transport=sse session_id=(empty) reason=invalid_endpoint_event",
		},
		{
			name:           "oversized event",
			event:          strings.Repeat("endpoint-secret", maxSSEEndpointEventBytes/len("endpoint-secret")+1),
			hasIdentity:    true,
			wantLogMessage: "SSE session rejected: transport=sse session_id=(empty) reason=endpoint_event_too_large",
		},
		{
			name:           "missing identity",
			event:          "event: endpoint\ndata: /messages/?sessionid=sse-1&token=endpoint-secret\n\n",
			wantLogMessage: "SSE session rejected: transport=sse session_id=s***1 reason=missing_identity",
		},
		{
			name:           "binding conflict",
			event:          "event: endpoint\ndata: /messages/?sessionid=sse-1&token=endpoint-secret\n\n",
			hasIdentity:    true,
			bindFirst:      true,
			wantLogMessage: "SSE session rejected: transport=sse session_id=s***1 reason=session_binding_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger.Init(&logs)
			t.Cleanup(func() { logger.Init(io.Discard) })

			bindings := newSessionBindings()
			if tt.bindFirst {
				if !bindings.bind(sseSessionTransport, "sse-1", authenticatedSessionIdentity(t, "token-b")) {
					t.Fatal("initial bind failed")
				}
			}
			underlying := newObservingResponseWriter()
			underlying.Header().Set("Content-Type", "text/event-stream")
			underlying.Header().Set("Cache-Control", "no-cache")
			writer := newSSEEndpointBindingWriter(
				underlying,
				bindings,
				authenticatedSessionIdentity(t, "token-a"),
				tt.hasIdentity,
			)

			if n, err := io.WriteString(writer, tt.event); err != nil || n != len(tt.event) {
				t.Fatalf("first Write() = (%d, %v), want (%d, nil)", n, err, len(tt.event))
			}
			_, _ = io.WriteString(writer, "event: message\ndata: must-not-leak\n\n")
			writer.Flush()

			if underlying.status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", underlying.status)
			}
			if got := underlying.body.String(); got != "Internal Server Error\n" {
				t.Fatalf("body = %q, want only generic rejection", got)
			}
			if got := underlying.Header().Get("Content-Type"); got == "text/event-stream" {
				t.Fatalf("Content-Type = %q, want SSE header cleared", got)
			}
			if got := underlying.Header().Get("Cache-Control"); got != "" {
				t.Fatalf("Cache-Control = %q, want cleared", got)
			}

			logLine := strings.TrimSpace(logs.String())
			var record map[string]any
			if err := json.Unmarshal([]byte(logLine), &record); err != nil {
				t.Fatalf("rejection log is not one JSON record: %v; log=%q", err, logLine)
			}
			if got := record["msg"]; got != tt.wantLogMessage {
				t.Fatalf("log message = %q, want %q", got, tt.wantLogMessage)
			}
			for _, forbidden := range []string{"token-a", "token-b", "endpoint-secret", "/messages/", "sse-1"} {
				if strings.Contains(logLine, forbidden) {
					t.Fatalf("rejection log exposed forbidden value %q: %s", forbidden, logLine)
				}
			}
		})
	}
}

func TestSSESessionBindingMiddlewareAllowsOnlyMatchingPOST(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")
	if !bindings.bind(sseSessionTransport, "sse-1", identity) {
		t.Fatal("initial bind failed")
	}
	var calls atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	handler := sseSessionBindingMiddleware(next, bindings)

	matching := authenticatedBindingHandler(t, "token-a", handler)
	matchingRecorder := httptest.NewRecorder()
	matchingRequest := httptest.NewRequest(http.MethodPost, "/messages/?sessionid=sse-1", nil)
	matchingRequest.Header.Set("Authorization", "Bearer token-a")
	matching.ServeHTTP(matchingRecorder, matchingRequest)
	if matchingRecorder.Code != http.StatusNoContent || calls.Load() != 1 {
		t.Fatalf("matching POST status=%d calls=%d, want 204 and 1", matchingRecorder.Code, calls.Load())
	}

	for _, tt := range []struct {
		name      string
		token     string
		sessionID string
		withAuth  bool
	}{
		{name: "different identity", token: "token-b", sessionID: "sse-1", withAuth: true},
		{name: "missing identity", sessionID: "sse-1"},
		{name: "absent session ID", token: "token-a", withAuth: true},
		{name: "unbound session ID", token: "token-a", sessionID: "sse-unknown", withAuth: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var candidate http.Handler = handler
			if tt.withAuth {
				candidate = authenticatedBindingHandler(t, tt.token, candidate)
			}
			recorder := httptest.NewRecorder()
			target := "/messages/"
			if tt.sessionID != "" {
				target += "?sessionid=" + tt.sessionID
			}
			req := httptest.NewRequest(http.MethodPost, target, nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			candidate.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden || calls.Load() != 1 {
				t.Fatalf("status=%d calls=%d, want 403 and 1", recorder.Code, calls.Load())
			}
		})
	}
}

func TestSSESessionBindingMiddlewareDeletesBindingWhenGETReturns(t *testing.T) {
	bindings := newSessionBindings()
	identity := authenticatedSessionIdentity(t, "token-a")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "event: endpoint\ndata: /messages/?sessionid=sse-1\n\n")
		if !bindings.matches(sseSessionTransport, "sse-1", identity) {
			t.Error("SSE session was not bound while GET handler was active")
		}
	})
	handler := authenticatedBindingHandler(t, "token-a", sseSessionBindingMiddleware(next, bindings))
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	req.Header.Set("Authorization", "Bearer token-a")

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != "event: endpoint\ndata: /messages/?sessionid=sse-1\n\n" {
		t.Fatalf("body = %q, want endpoint event", got)
	}
	if bindings.matches(sseSessionTransport, "sse-1", identity) {
		t.Fatal("binding remained after GET returned")
	}
}

func TestSSESessionBindingMiddlewareRejectsMissingFirstEvent(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
	})
	handler := authenticatedBindingHandler(
		t,
		"token-a",
		sseSessionBindingMiddleware(next, newSessionBindings()),
	)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	req.Header.Set("Authorization", "Bearer token-a")

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if got := recorder.Body.String(); got != "Internal Server Error\n" {
		t.Fatalf("body = %q, want only generic rejection", got)
	}
}
