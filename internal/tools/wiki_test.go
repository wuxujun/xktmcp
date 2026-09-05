package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	handler := WikiSearchHandler(svc, "")
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

func TestWikiSearchHandlerAddsResourceLinkAndKeepsTextFallback(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	handler := WikiSearchHandler(svc, "")
	res, data, err := handler(context.Background(), nil, WikiSearchArgs{Query: "resource-link", TopK: 5})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	if len(res.Content) != 2 {
		t.Fatalf("expected text fallback and one resource link, got %#v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected first content item to remain text, got %T", res.Content[0])
	}
	wantItem := model.WikiSearchResult{PageID: "p1", Title: "Title 1", Summary: "Summary 1"}
	var textItems []model.WikiSearchResult
	if err := json.Unmarshal([]byte(text.Text), &textItems); err != nil || len(textItems) != 1 || textItems[0] != wantItem {
		t.Fatalf("unexpected text fallback: items=%#v err=%v", textItems, err)
	}
	link, ok := res.Content[1].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("expected second content item to be a resource link, got %T", res.Content[1])
	}
	if link.URI != "wiki://page/cDE" {
		t.Errorf("unexpected resource URI: %q", link.URI)
	}
	if link.Name != "p1" || link.Title != "Title 1" {
		t.Errorf("unexpected resource identity: name=%q title=%q", link.Name, link.Title)
	}
	if link.Description != "Summary 1" || link.MIMEType != "text/markdown" {
		t.Errorf("unexpected resource metadata: description=%q mime_type=%q", link.Description, link.MIMEType)
	}
	structuredJSON, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal structured fallback: %v", err)
	}
	var structured WikiSearchResponse
	if err := json.Unmarshal(structuredJSON, &structured); err != nil || len(structured.Items) != 1 || structured.Items[0] != wantItem {
		t.Fatalf("unexpected structured fallback: data=%#v err=%v", structured, err)
	}
}

func TestWikiSearchHandlerCacheSeparatesResourceLinkBaseURL(t *testing.T) {
	ts, svc := setupWikiToolsTest(t)
	defer ts.Close()

	oldCache := wikiCache
	wikiCache = NewMemoryCacheWithOptions(16, 0)
	t.Cleanup(func() {
		wikiCache.Stop()
		wikiCache = oldCache
	})

	args := WikiSearchArgs{Query: "cache-prefix", TopK: 5}
	first, _, err := WikiSearchHandler(svc, "https://first.example.com/pages")(context.Background(), nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if link, ok := first.Content[1].(*mcp.ResourceLink); !ok || link.URI != "https://first.example.com/pages/cDE" {
		t.Fatalf("first resource link=%#v", first.Content[1])
	}

	second, _, err := WikiSearchHandler(svc, "https://second.example.com/pages")(context.Background(), nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if link, ok := second.Content[1].(*mcp.ResourceLink); !ok || link.URI != "https://second.example.com/pages/cDE" {
		t.Fatalf("second resource link=%#v", second.Content[1])
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
