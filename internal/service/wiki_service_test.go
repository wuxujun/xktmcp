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

func setupWikiMockServer(t *testing.T) (*httptest.Server, *WikiService) {
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
				"data": model.WikiPage{PageID: "p1", Title: "Title 1", Content: "# Doc Content"},
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
	svc := NewWikiService(api)
	return ts, svc
}

func TestWikiService_Search(t *testing.T) {
	ts, svc := setupWikiMockServer(t)
	defer ts.Close()

	// Empty query validation
	_, err := svc.Search(context.Background(), "u1", "", "", 5)
	if err != ErrInvalidQuery {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	res, err := svc.Search(context.Background(), "u1", "golang", "dev", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(res) != 1 || res[0].PageID != "p1" {
		t.Fatalf("unexpected search result: %+v", res)
	}
}

func TestWikiService_GetPage(t *testing.T) {
	ts, svc := setupWikiMockServer(t)
	defer ts.Close()

	// Empty ID and Title validation
	_, err := svc.GetPage(context.Background(), "u1", "", "")
	if err != ErrInvalidPageID {
		t.Fatalf("expected ErrInvalidPageID, got %v", err)
	}

	page, err := svc.GetPage(context.Background(), "u1", "p1", "")
	if err != nil {
		t.Fatalf("GetPage failed: %v", err)
	}
	if page == nil || page.PageID != "p1" {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestWikiService_ListTree(t *testing.T) {
	ts, svc := setupWikiMockServer(t)
	defer ts.Close()

	nodes, err := svc.ListTree(context.Background(), "u1", "", 2)
	if err != nil {
		t.Fatalf("ListTree failed: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node1" {
		t.Fatalf("unexpected tree nodes: %+v", nodes)
	}
}

func TestWikiService_UpsertPage(t *testing.T) {
	ts, svc := setupWikiMockServer(t)
	defer ts.Close()

	// Validation
	if _, err := svc.UpsertPage(context.Background(), "u1", "", "content", "", "", "create"); err != ErrInvalidTitle {
		t.Fatalf("expected ErrInvalidTitle, got %v", err)
	}
	if _, err := svc.UpsertPage(context.Background(), "u1", "title", "", "", "", "create"); err != ErrInvalidContent {
		t.Fatalf("expected ErrInvalidContent, got %v", err)
	}

	res, err := svc.UpsertPage(context.Background(), "u1", "title", "content", "cat", "summary", "create")
	if err != nil {
		t.Fatalf("UpsertPage failed: %v", err)
	}
	if res == nil || res.PageID != "p1" || res.Status != "created" {
		t.Fatalf("unexpected upsert result: %+v", res)
	}
}

func TestWikiService_GetBacklinks(t *testing.T) {
	ts, svc := setupWikiMockServer(t)
	defer ts.Close()

	if _, err := svc.GetBacklinks(context.Background(), "u1", ""); err != ErrInvalidID {
		t.Fatalf("expected ErrInvalidID, got %v", err)
	}

	links, err := svc.GetBacklinks(context.Background(), "u1", "p1")
	if err != nil {
		t.Fatalf("GetBacklinks failed: %v", err)
	}
	if len(links) != 1 || links[0].SourcePageID != "p2" {
		t.Fatalf("unexpected backlinks: %+v", links)
	}
}
