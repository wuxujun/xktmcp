package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type trackingBody struct {
	data   []byte
	reads  int32
	closed int32
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (b *trackingBody) Read(p []byte) (int, error) {
	atomic.AddInt32(&b.reads, 1)
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *trackingBody) Close() error {
	atomic.StoreInt32(&b.closed, 1)
	return nil
}

func TestReadErrorDetails(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "standard JSON message",
			body:     `{"message": "invalid request parameters", "code": 400}`,
			expected: "invalid request parameters",
		},
		{
			name:     "JSON with msg field",
			body:     `{"msg": "unauthorized access"}`,
			expected: "unauthorized access",
		},
		{
			name:     "JSON with error field",
			body:     `{"error": "rate limit exceeded"}`,
			expected: "rate limit exceeded",
		},
		{
			name:     "JSON with description field",
			body:     `{"description": "resource not found"}`,
			expected: "resource not found",
		},
		{
			name:     "plain text",
			body:     "Simple error message",
			expected: "Simple error message",
		},
		{
			name:     "long plain text is truncated",
			body:     strings.Repeat("A", 300),
			expected: strings.Repeat("A", 200) + "...",
		},
		{
			name:     "empty body",
			body:     "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, _ = rec.WriteString(tt.body)
			resp := rec.Result()
			defer resp.Body.Close()

			actual := readErrorDetails(resp)
			if actual != tt.expected {
				t.Errorf("readErrorDetails() = %q, want %q", actual, tt.expected)
			}
		})
	}
}

func TestDoRequestWithRetry(t *testing.T) {
	t.Run("drains transient response before close", func(t *testing.T) {
		first := &trackingBody{data: []byte("transient response")}
		attempts := 0
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: first, Request: req}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
		})
		client := &http.Client{Transport: transport}
		req, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
		resp, err := doRequestWithRetry(context.Background(), client, req, "TestAPI", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if atomic.LoadInt32(&first.reads) == 0 || atomic.LoadInt32(&first.closed) == 0 {
			t.Fatalf("transient body reads=%d closed=%d, want drained and closed", first.reads, first.closed)
		}
	})

	// 隔离:重置共享熔断器,避免本测试的失败累计影响其它测试(反之亦然)。
	upstreamBreaker.reset()
	defer upstreamBreaker.reset()

	t.Run("success on first try", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()

		if atomic.LoadInt32(&calls) != 1 {
			t.Errorf("expected 1 call, got %d", calls)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
	})

	t.Run("retry on 500 and eventually succeed", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			val := atomic.AddInt32(&calls, 1)
			if val < 3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()

		if atomic.LoadInt32(&calls) != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
	})

	t.Run("do not retry POST without idempotency key", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{"name":"demo"}`))
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if atomic.LoadInt32(&calls) != 1 {
			t.Fatalf("expected 1 call without idempotency key, got %d", calls)
		}
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d, want 500", resp.StatusCode)
		}
	})

	t.Run("retry POST with idempotency key and preserve body", func(t *testing.T) {
		var calls int32
		var bodies []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			val := atomic.AddInt32(&calls, 1)
			body, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(body))
			if val < 3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{"name":"demo"}`))
		req.Header.Set("Idempotency-Key", "request-123")
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if atomic.LoadInt32(&calls) != 3 {
			t.Fatalf("expected 3 calls with idempotency key, got %d", calls)
		}
		for i, body := range bodies {
			if body != `{"name":"demo"}` {
				t.Errorf("attempt %d body=%q, want original body", i+1, body)
			}
		}
	})

	t.Run("no retry on 400", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()

		if atomic.LoadInt32(&calls) != 1 {
			t.Errorf("expected 1 call (no retry for 400), got %d", calls)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request, got %d", resp.StatusCode)
		}
	})

	t.Run("fail after max attempts", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		_, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI", nil)
		if err == nil {
			t.Fatal("expected failure, got success")
		}

		if atomic.LoadInt32(&calls) != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("respect context cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)

		// Cancel immediately to trigger context check
		cancel()

		_, err := doRequestWithRetry(ctx, http.DefaultClient, req, "TestAPI", nil)
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})
}
