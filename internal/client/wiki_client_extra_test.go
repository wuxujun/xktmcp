package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWikiClientTreeBacklinksAndUpsert(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/ai/wiki/tree":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "p1", "title": "Root"}}})
		case "/api/ai/wiki/backlinks":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"source_page_id": "p2", "source_title": "Child"}}})
		case "/api/ai/wiki/page/upsert":
			body, _ := io.ReadAll(r.Body)
			if len(body) == 0 {
				t.Error("upsert body is empty")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"page_id": "p3", "version": 1, "status": "created"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := NewWikiAPI(Config{BaseURL: server.URL, APIToken: "token", Timeout: time.Second})
	ctx := context.Background()
	tree, err := a.ListTree(ctx, "u1", "", 3)
	if err != nil || len(tree) != 1 || tree[0].ID != "p1" {
		t.Fatalf("tree=%#v err=%v", tree, err)
	}
	links, err := a.GetBacklinks(ctx, "u1", "p1")
	if err != nil || len(links) != 1 || links[0].SourcePageID != "p2" {
		t.Fatalf("links=%#v err=%v", links, err)
	}
	upsert, err := a.UpsertPage(ctx, "u1", "Title", "Content", "cat", "summary", "create")
	if err != nil || upsert == nil || upsert.PageID != "p3" {
		t.Fatalf("upsert=%#v err=%v", upsert, err)
	}
}
