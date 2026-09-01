package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/trace"
)

type testAuditArgs struct {
	UserID string
}

func (a testAuditArgs) CorrelationID() string { return "test-trace" }
func (a testAuditArgs) Querier() string       { return a.UserID }
func (a testAuditArgs) AuditSubject() string  { return "subject" }

func TestWrapToolHandlerRejectsAuthenticatedUserConflict(t *testing.T) {
	called := false
	handler := wrapToolHandler("test_tool", func(context.Context, *mcp.CallToolRequest, testAuditArgs) (*mcp.CallToolResult, any, error) {
		called = true
		return &mcp.CallToolResult{}, nil, nil
	})

	ctx := trace.WithAuthenticatedUserID(context.Background(), "trusted-user")
	result, _, err := handler(ctx, nil, testAuditArgs{UserID: "other-user"})
	if err != nil || result == nil || !result.IsError || called {
		t.Fatalf("result=%#v err=%v called=%t; want MCP error, nil, false", result, err, called)
	}
}

func TestWrapToolHandlerUsesResolvedIdentity(t *testing.T) {
	handler := wrapToolHandler("test_tool", func(ctx context.Context, _ *mcp.CallToolRequest, in testAuditArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, trace.EffectiveUserID(ctx, in.UserID), nil
	})

	ctx := trace.WithAuthenticatedUserID(context.Background(), "trusted-user")
	result, out, err := handler(ctx, nil, testAuditArgs{UserID: "trusted-user"})
	if err != nil || result == nil || result.IsError || out != "trusted-user" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}
