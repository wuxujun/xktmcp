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

func TestWikiUpsertPageHandlerInvalidatesOnlyEffectiveUserCache(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	oldCache := wikiCache
	wikiCache = NewMemoryCacheWithOptions(32, 0)
	t.Cleanup(func() {
		wikiCache.Stop()
		wikiCache = oldCache
	})

	userAKeys := []string{
		"wiki:search:user-a:query::5",
		"wiki:page:user-a:p1:",
		"wiki:tree:user-a::3",
		"wiki:backlinks:user-a:p1",
	}
	userBKeys := []string{
		"wiki:search:user-b:query::5",
		"wiki:page:user-b:p1:",
		"wiki:tree:user-b::3",
		"wiki:backlinks:user-b:p1",
	}
	for _, key := range append(userAKeys, userBKeys...) {
		wikiCache.Set(key, key, time.Minute)
	}

	handler := WikiUpsertPageHandler(svc)
	res, _, err := handler(context.Background(), nil, WikiUpsertPageArgs{
		CommonArgs: CommonArgs{UserID: "user-a"},
		Title:      "New Doc",
		Content:    "Markdown Content",
		Mode:       "create",
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}

	for _, key := range userAKeys {
		if _, ok := wikiCache.Get(key); ok {
			t.Errorf("expected cache key %q to be invalidated", key)
		}
	}
	for _, key := range userBKeys {
		if _, ok := wikiCache.Get(key); !ok {
			t.Errorf("expected other user's cache key %q to remain", key)
		}
	}
}

func TestWikiUpsertPageHandlerWithoutUserInvalidatesAllWikiCache(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	oldCache := wikiCache
	wikiCache = NewMemoryCacheWithOptions(16, 0)
	t.Cleanup(func() {
		wikiCache.Stop()
		wikiCache = oldCache
	})

	for _, key := range []string{
		"wiki:search:user-a:query::5",
		"wiki:page:user-b:p1:",
		"wiki:tree:user-c::3",
		"wiki:backlinks:user-d:p1",
	} {
		wikiCache.Set(key, key, time.Minute)
	}
	wikiCache.Set("student:query:user-a", "student", time.Minute)

	handler := WikiUpsertPageHandler(svc)
	res, _, err := handler(context.Background(), nil, WikiUpsertPageArgs{
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

	if got := wikiCache.Len(); got != 1 {
		t.Fatalf("expected only non-Wiki cache entry to remain, got %d entries", got)
	}
	if _, ok := wikiCache.Get("student:query:user-a"); !ok {
		t.Fatal("expected non-Wiki cache entry to remain")
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
