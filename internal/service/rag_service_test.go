package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

func TestRagServiceSearch(t *testing.T) {
	t.Run("empty query returns error", func(t *testing.T) {
		svc := NewRagService(nil)
		_, err := svc.RagSearch(context.Background(), "user_123", "")
		if err != ErrInvalidQuery {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("success returns rags", func(t *testing.T) {
		mockRags := []model.Rag{
			{Title: "请假制度", Content: "事假需要提前一天申请", Score: 0.95, Url: "http://example.com/rule1"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/ai/rag/search" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Query().Get("userId") != "user_123" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("query") != "请假" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": mockRags,
			})
		}))
		defer server.Close()

		cfg := client.Config{
			BaseURL:  server.URL,
			APIToken: "test-token",
			Timeout:  2 * time.Second,
		}
		api := client.NewRagAPI(cfg)
		svc := NewRagService(api)

		res, err := svc.RagSearch(context.Background(), "user_123", "请假")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(res) != 1 || res[0].Title != "请假制度" {
			t.Errorf("expected '请假制度', got %+v", res)
		}
	})
}
