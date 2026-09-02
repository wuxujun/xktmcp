package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	mcp_server "github.com/wuxujun/xktmcp/internal/server"
	wikibackend "github.com/wuxujun/xktmcp/internal/wiki"
)

func TestWikiResourcesTransports(t *testing.T) {
	t.Setenv("MCP_ENABLED_TOOLS", "")
	configPath := newWikiResourceTransportConfig(t)

	t.Run("streamable_http", func(t *testing.T) {
		server := newWikiResourceTestServer(t, configPath)
		httpServer := httptest.NewServer(newStreamableHTTPHandler(server))
		defer httpServer.Close()
		assertWikiResourcesOverTransport(t, &mcp.StreamableClientTransport{Endpoint: httpServer.URL})
	})

	t.Run("sse", func(t *testing.T) {
		server := newWikiResourceTestServer(t, configPath)
		handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
		httpServer := httptest.NewServer(handler)
		defer httpServer.Close()
		assertWikiResourcesOverTransport(t, &mcp.SSEClientTransport{Endpoint: httpServer.URL})
	})
}

func newWikiResourceTestServer(t *testing.T, configPath string) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "resource-test-server", Version: "1.0.0"}, nil)
	if err := mcp_server.RegisterAll(server, configPath); err != nil {
		t.Fatalf("RegisterAll returned error: %v", err)
	}
	return server
}

func assertWikiResourcesOverTransport(t *testing.T, transport mcp.Transport) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "resource-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListResources(ctx, nil)
	if err != nil || len(listed.Resources) != 2 {
		t.Fatalf("resources=%+v err=%v", listed, err)
	}
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil || len(templates.ResourceTemplates) != 1 {
		t.Fatalf("templates=%+v err=%v", templates, err)
	}
	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "wiki://catalog"})
	if err != nil || len(read.Contents) != 1 || !strings.Contains(read.Contents[0].Text, `"name":"Transport Guide"`) {
		t.Fatalf("catalog=%+v err=%v", read, err)
	}
	var catalog wikibackend.ResourceCatalog
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &catalog); err != nil || len(catalog.Items) != 1 {
		t.Fatalf("decoded catalog=%+v err=%v", catalog, err)
	}
	page, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: catalog.Items[0].URI})
	if err != nil || len(page.Contents) != 1 || page.Contents[0].Text != "Transport body" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func newWikiResourceTransportConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	contentDir := filepath.Join(root, "content")
	if err := os.Mkdir(contentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	page := "---\npage_id: transport-guide\ntitle: Transport Guide\n---\n\nTransport body\n"
	if err := os.WriteFile(filepath.Join(contentDir, "transport-guide.md"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "wiki.json")
	config := `{"mode":"local","resources":{"enabled":true},"local":{"root":".","content_dirs":["content"],"write_dir":"content"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}
