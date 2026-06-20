package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/service"
	"github.com/wuxujun/xktmcp/internal/trace"
)

func TestStaffSearchHandlerUsesEffectiveUserIDAndIsolatesCache(t *testing.T) {
	oldCache := sharedCache
	sharedCache = NewMemoryCacheWithOptions(10, 0)
	defer func() {
		sharedCache.Stop()
		sharedCache = oldCache
	}()

	var calls int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/api/staff" {
			http.NotFound(w, r)
			return
		}

		userID := r.URL.Query().Get("userid")
		if r.URL.Query().Get("query") != "math" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"userid": userID, "name": "teacher-" + userID},
			},
		})
	}))
	defer backend.Close()

	svc := service.NewStaffService(client.NewStaffAPI(client.Config{
		BaseURL:  backend.URL,
		APIToken: "test-token",
		Timeout:  2 * time.Second,
	}))
	handler := StaffSearchHandler(svc)

	ctxA := trace.WithUserID(context.Background(), "user-a")
	resA, _, err := handler(ctxA, &mcp.CallToolRequest{}, StaffSearchArgs{Query: "math"})
	if err != nil {
		t.Fatalf("first call returned error: %v", err)
	}
	if !strings.Contains(resA.Content[0].(*mcp.TextContent).Text, "teacher-user-a") {
		t.Fatalf("first call should use context userId user-a, got %+v", resA.Content)
	}

	ctxB := trace.WithUserID(context.Background(), "user-b")
	resB, _, err := handler(ctxB, &mcp.CallToolRequest{}, StaffSearchArgs{Query: "math"})
	if err != nil {
		t.Fatalf("second call returned error: %v", err)
	}
	if !strings.Contains(resB.Content[0].(*mcp.TextContent).Text, "teacher-user-b") {
		t.Fatalf("second call should not reuse user-a cache, got %+v", resB.Content)
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("different effective userIds should produce separate upstream calls, got %d", got)
	}
}
