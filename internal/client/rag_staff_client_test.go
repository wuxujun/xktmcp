package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRagAndStaffSearchDecodeResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/ai/rag/search" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"title": "policy", "content": "body", "score": 0.9}}})
			return
		}
		if r.URL.Path == "/api/staff" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"name": "Alice"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	cfg := Config{BaseURL: server.URL, APIToken: "token", Timeout: time.Second}
	rag, err := NewRagAPI(cfg).SearchRags(context.Background(), "u1", "policy")
	if err != nil || len(rag) != 1 || rag[0].Title != "policy" {
		t.Fatalf("rag=%#v err=%v", rag, err)
	}
	staff, err := NewStaffAPI(cfg).SearchStaffs(context.Background(), "u1", "Alice")
	if err != nil || len(staff) != 1 || staff[0].Name != "Alice" {
		t.Fatalf("staff=%#v err=%v", staff, err)
	}
}
