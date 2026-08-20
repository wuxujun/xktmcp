package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/model"
)

func TestWikiAPI_SearchWiki(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/wiki/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "golang" {
			t.Errorf("unexpected query: %s", r.URL.Query().Get("query"))
		}
		if r.URL.Query().Get("userId") != "u123" {
			t.Errorf("unexpected userId: %s", r.URL.Query().Get("userId"))
		}
		resp := wikiSearchResponse{
			Data: []model.WikiSearchResult{
				{
					PageID:   "p1",
					Title:    "Go Language Intro",
					Summary:  "Overview of Go",
					Category: "dev",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	api := NewWikiAPI(Config{
		BaseURL:  ts.URL,
		APIToken: "test-token",
		Timeout:  2 * time.Second,
	})

	results, err := api.SearchWiki(context.Background(), "u123", "golang", "dev", 5)
	if err != nil {
		t.Fatalf("SearchWiki failed: %v", err)
	}
	if len(results) != 1 || results[0].PageID != "p1" {
		t.Fatalf("unexpected search results: %+v", results)
	}
}

func TestWikiAPI_GetPage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/wiki/page" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("page_id") != "p1" {
			t.Errorf("unexpected page_id: %s", r.URL.Query().Get("page_id"))
		}
		resp := wikiGetPageResponse{
			Data: model.WikiPage{
				PageID:   "p1",
				Title:    "Go Language Intro",
				Content:  "# Go Intro\nContent goes here.",
				Category: "dev",
				Version:  1,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	api := NewWikiAPI(Config{
		BaseURL:  ts.URL,
		APIToken: "test-token",
		Timeout:  2 * time.Second,
	})

	page, err := api.GetPage(context.Background(), "u123", "p1", "")
	if err != nil {
		t.Fatalf("GetPage failed: %v", err)
	}
	if page == nil || page.PageID != "p1" || page.Version != 1 {
		t.Fatalf("unexpected page result: %+v", page)
	}
}

func TestWikiAPI_UpsertPage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/wiki/page/upsert" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		resp := wikiUpsertResponse{
			Data: model.WikiUpsertResult{
				PageID:  "p2",
				Version: 2,
				Status:  "updated",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	api := NewWikiAPI(Config{
		BaseURL:  ts.URL,
		APIToken: "test-token",
		Timeout:  2 * time.Second,
	})

	result, err := api.UpsertPage(context.Background(), "u123", "New Title", "## Content", "ops", "Summary", "update")
	if err != nil {
		t.Fatalf("UpsertPage failed: %v", err)
	}
	if result == nil || result.PageID != "p2" || result.Status != "updated" {
		t.Fatalf("unexpected upsert result: %+v", result)
	}
}
