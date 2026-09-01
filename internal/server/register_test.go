package server

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/trace"
)

func TestParseEnabledTools(t *testing.T) {
	all, err := parseEnabledTools("")
	if err != nil || all != nil {
		t.Fatalf("empty config = %#v err=%v, want nil set", all, err)
	}
	set, err := parseEnabledTools(" wiki_search,rag_search,wiki_search ")
	if err != nil || len(set) != 2 || !set["wiki_search"] || !set["rag_search"] {
		t.Fatalf("parsed tools = %#v err=%v", set, err)
	}
	if _, err := parseEnabledTools("unknown_tool"); err == nil {
		t.Fatal("unknown tool was accepted")
	}
}

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

func TestRegisterAllLocalWikiDoesNotRequireUpstreamConfig(t *testing.T) {
	t.Setenv("API_TOKEN", "")
	t.Setenv("BASE_URL", "")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "content"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "wiki.json")
	config := `{"mode":"local","local":{"root":".","content_dirs":["content"],"write_dir":"content"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if err := RegisterAll(server, configPath); err != nil {
		t.Fatalf("RegisterAll returned error: %v", err)
	}

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"wiki_get_backlinks", "wiki_get_page", "wiki_list_tree", "wiki_search", "wiki_upsert_page"}
	if len(names) != len(want) {
		t.Fatalf("registered tools = %v, want only local wiki tools %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("registered tools = %v, want %v", names, want)
		}
	}
}

func TestRegisterAllHTTPWikiStillRequiresUpstreamConfig(t *testing.T) {
	t.Setenv("API_TOKEN", "")
	t.Setenv("BASE_URL", "")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if err := RegisterAll(server); err == nil {
		t.Fatal("RegisterAll succeeded without API_TOKEN in HTTP Wiki mode")
	}
}
