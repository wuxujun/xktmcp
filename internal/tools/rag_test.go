package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/service"
)

func newRagHandlerForTest(t *testing.T, items []model.Rag) func(context.Context, *mcp.CallToolRequest, RagSearchArgs) (*mcp.CallToolResult, any, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/rag/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(server.Close)
	api := client.NewRagAPI(client.Config{BaseURL: server.URL, Timeout: 2 * time.Second})
	return RagSearchHandler(service.NewRagService(api))
}

func TestRagSearchAppliesTopKAndMinScore(t *testing.T) {
	handler := newRagHandlerForTest(t, []model.Rag{
		{Title: "high", Content: "high content", Score: 0.95, Url: "https://example/high"},
		{Title: "mid", Content: "mid content", Score: 0.85, Url: "https://example/mid"},
		{Title: "low", Content: "low content", Score: 0.40, Url: "https://example/low"},
	})
	result, out, err := handler(context.Background(), nil, RagSearchArgs{Query: "policy", TopK: 2, MinScore: 0.8})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	response := out.(RagSearchResponse)
	if response.Meta.HitCount != 2 || len(response.Chunks) != 2 || len(response.Sources) != 2 {
		t.Fatalf("hit=%d chunks=%d sources=%d, want 2 each", response.Meta.HitCount, len(response.Chunks), len(response.Sources))
	}
	if strings.Contains(response.Context, "low content") {
		t.Fatal("below-min-score result remained in context")
	}
}

func TestRagSearchIncludeFlagsControlContextAndOutput(t *testing.T) {
	handler := newRagHandlerForTest(t, []model.Rag{{Title: "policy", Content: "secret details", Score: 0.9, Url: "https://example/policy"}})
	includeSources, includeChunks := false, false
	_, out, err := handler(context.Background(), nil, RagSearchArgs{
		Query: "policy", TopK: 5, MinScore: 0.1,
		IncludeSources: &includeSources, IncludeChunks: &includeChunks,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	response := out.(RagSearchResponse)
	if len(response.Sources) != 0 || len(response.Chunks) != 0 {
		t.Fatalf("sources=%d chunks=%d, want both omitted", len(response.Sources), len(response.Chunks))
	}
	if strings.Contains(response.Context, "secret details") || strings.Contains(response.Context, "https://example/policy") {
		t.Fatalf("context leaked disabled fields: %q", response.Context)
	}
}

func TestRagSearchDefaultsMinScoreToPointOne(t *testing.T) {
	handler := newRagHandlerForTest(t, []model.Rag{{Title: "borderline", Content: "content", Score: 0.15, Url: "https://example/borderline"}})
	_, out, err := handler(context.Background(), nil, RagSearchArgs{Query: "policy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	response := out.(RagSearchResponse)
	if response.SearchStrategy.MinScore != 0.1 || response.Meta.HitCount != 1 {
		t.Fatalf("strategy=%#v hit=%d, want min_score 0.1 and one result", response.SearchStrategy, response.Meta.HitCount)
	}
}

func TestRagSearchRejectsOutOfRangeTopK(t *testing.T) {
	handler := newRagHandlerForTest(t, nil)
	result, _, err := handler(context.Background(), nil, RagSearchArgs{Query: "policy", TopK: 21})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%#v err=%v, want MCP validation error", result, err)
	}
}

func TestRewriteQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ends with 怎么处理",
			input:    "请假怎么处理",
			expected: "请假的处理规则",
		},
		{
			name:     "ends with 如何处理",
			input:    "加班如何处理",
			expected: "加班的处理规则",
		},
		{
			name:     "replace 后的的",
			input:    "审批后的的表单",
			expected: "审批后的表单",
		},
		{
			name:     "replace 后考勤",
			input:    "入职后考勤记录",
			expected: "入职后的考勤记录",
		},
		{
			name:     "no match",
			input:    "怎么请假",
			expected: "怎么请假",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := rewriteQuery(tt.input)
			if actual != tt.expected {
				t.Errorf("rewriteQuery(%q) = %q, want %q", tt.input, actual, tt.expected)
			}
		})
	}
}

func TestRewriteQuerySemanticFallback(t *testing.T) {
	// When session is nil, rewriteQuerySemantic should fallback to rewriteQuery
	actual := rewriteQuerySemantic(context.Background(), nil, "请假怎么处理")
	expected := "请假的处理规则"
	if actual != expected {
		t.Errorf("rewriteQuerySemantic(nil) = %q, want %q", actual, expected)
	}
}

func TestIsMethodNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`calling "sampling/createMessage": Method not found`), true},
		{errors.New("method not found"), true}, // 大小写不敏感
		{errors.New("context deadline exceeded"), false},
		{errors.New("connection refused"), false},
	}
	for _, c := range cases {
		if got := isMethodNotFound(c.err); got != c.want {
			t.Errorf("isMethodNotFound(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestSamplingUnsupportedShortCircuit(t *testing.T) {
	// 置位标志后,即使 session 非 nil 的路径也不会被走到——
	// 这里用 nil session 验证短路返回本地改写结果,并确保测试后复位全局标志。
	samplingUnsupported.Store(true)
	defer samplingUnsupported.Store(false)

	got := rewriteQuerySemantic(context.Background(), nil, "请假怎么处理")
	if want := rewriteQuery("请假怎么处理"); got != want {
		t.Errorf("标志置位时应直接走本地改写: got %q, want %q", got, want)
	}
}
