package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

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
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI")
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
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI")
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

	t.Run("no retry on 400", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI")
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
		_, err := doRequestWithRetry(context.Background(), http.DefaultClient, req, "TestAPI")
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

		_, err := doRequestWithRetry(ctx, http.DefaultClient, req, "TestAPI")
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})
}
