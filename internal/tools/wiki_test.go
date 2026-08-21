package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/service"
)

func setupWikiToolsTest(t *testing.T) (*httptest.Server, *service.WikiService) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ai/wiki/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []model.WikiSearchResult{
					{PageID: "p1", Title: "Title 1", Summary: "Summary 1"},
				},
			})
		case "/api/ai/wiki/page":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": model.WikiPage{PageID: "p1", Title: "Title 1", Content: "# Doc 1"},
			})
		case "/api/ai/wiki/tree":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []model.WikiNode{
					{ID: "node1", Title: "Root Category", HasChildren: false},
				},
			})
		case "/api/ai/wiki/page/upsert":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": model.WikiUpsertResult{PageID: "p1", Version: 1, Status: "created"},
			})
		case "/api/ai/wiki/backlinks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []model.WikiBacklink{
					{SourcePageID: "p2", SourceTitle: "Source Page"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))

	api := client.NewWikiAPI(client.Config{
		BaseURL:  ts.URL,
		APIToken: "test-token",
		Timeout:  2 * time.Second,
	})
	svc := service.NewWikiService(api)
	return ts, svc
}

func TestWikiSearchHandler(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	handler := WikiSearchHandler(svc)
	res, data, err := handler(context.Background(), nil, WikiSearchArgs{Query: "test", TopK: 5})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if data == nil {
		t.Fatalf("expected non-nil data")
	}
	if _, ok := data.(map[string]any)["items"]; !ok {
		t.Fatalf("expected object data containing items, got %#v", data)
	}
}

func TestWikiGetPageHandler(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	handler := WikiGetPageHandler(svc)
	res, data, err := handler(context.Background(), nil, WikiGetPageArgs{PageID: "p1"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if data == nil {
		t.Fatalf("expected non-nil data")
	}
}

func TestWikiUpsertPageHandler(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	handler := WikiUpsertPageHandler(svc)
	res, data, err := handler(context.Background(), nil, WikiUpsertPageArgs{
		Title:   "New Doc",
		Content: "Markdown Content",
		Mode:    "create",
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if data == nil {
		t.Fatalf("expected non-nil data")
	}
}

func TestWikiListTreeHandler(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	handler := WikiListTreeHandler(svc)
	res, data, err := handler(context.Background(), nil, WikiListTreeArgs{Depth: 2})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if data == nil {
		t.Fatalf("expected non-nil data")
	}
	if _, ok := data.(map[string]any)["items"]; !ok {
		t.Fatalf("expected object data containing items, got %#v", data)
	}
}

func TestWikiGetBacklinksHandler(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	handler := WikiGetBacklinksHandler(svc)
	res, data, err := handler(context.Background(), nil, WikiGetBacklinksArgs{PageID: "p1"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if data == nil {
		t.Fatalf("expected non-nil data")
	}
	if _, ok := data.(map[string]any)["items"]; !ok {
		t.Fatalf("expected object data containing items, got %#v", data)
	}
}
