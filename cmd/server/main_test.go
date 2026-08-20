package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/trace"
)

const protocolVersion20260728 = "2026-07-28"

func TestUserIDMiddlewareInjectsQueryUserID(t *testing.T) {
	var got string
	handler := userIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/sse?userId=user-123", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "user-123" {
		t.Fatalf("expected context userId user-123, got %q", got)
	}
}

func TestUserIDMiddlewareKeepsExistingContextWhenQueryMissing(t *testing.T) {
	var got string
	handler := userIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/messages/1", nil)
	req = req.WithContext(trace.WithUserID(req.Context(), "existing-user"))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "existing-user" {
		t.Fatalf("expected existing context userId, got %q", got)
	}
}

func TestStreamableHTTPDiscoverSupports20260728(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "1.0.0",
	}, nil)
	var gotUserID string
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			gotUserID = trace.UserIDFromContext(ctx)
			return next(ctx, method, req)
		}
	})
	handler := newStreamableHTTPHandler(server)

	body := []byte(`{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"Postman Client","version":"12.24.2"},"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{},"url":{}},"sampling":{}}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion20260728)
	req.Header.Set("Mcp-Method", "server/discover")
	req = req.WithContext(trace.WithUserID(req.Context(), "remote-user-123"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.StatusCode, responseBody)
	}

	// Streamable HTTP 可返回 JSON 或单条 SSE 事件，两种格式均需验证。
	payload := responseBody
	if i := bytes.Index(responseBody, []byte("data: ")); i >= 0 {
		payload = responseBody[i+len("data: "):]
		if j := bytes.IndexByte(payload, '\n'); j >= 0 {
			payload = payload[:j]
		}
	}
	var rpcResponse struct {
		Result struct {
			SupportedVersions []string `json:"supportedVersions"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpcResponse); err != nil {
		t.Fatalf("decode response %q: %v", responseBody, err)
	}
	if len(rpcResponse.Error) > 0 && string(rpcResponse.Error) != "null" {
		t.Fatalf("discover returned error: %s", rpcResponse.Error)
	}
	if !slices.Contains(rpcResponse.Result.SupportedVersions, protocolVersion20260728) {
		t.Fatalf("supportedVersions = %v, want %q", rpcResponse.Result.SupportedVersions, protocolVersion20260728)
	}
	if gotUserID != "remote-user-123" {
		t.Fatalf("MCP request context userID = %q, want remote-user-123", gotUserID)
	}
}
