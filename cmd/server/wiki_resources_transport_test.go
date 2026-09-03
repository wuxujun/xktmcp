package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/auth"
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

func TestAuthenticatedWikiResourcesTransportsIsolateTenants(t *testing.T) {
	t.Setenv("MCP_ENABLED_TOOLS", "")
	configPath := newMultiTenantWikiResourceTransportConfig(t)
	tests := []struct {
		name      string
		handler   func(*mcp.Server) http.Handler
		transport func(string, *http.Client) mcp.Transport
	}{
		{
			name:    "streamable_http",
			handler: newStreamableHTTPHandler,
			transport: func(endpoint string, client *http.Client) mcp.Transport {
				return &mcp.StreamableClientTransport{
					Endpoint: endpoint, HTTPClient: client, DisableStandaloneSSE: true,
				}
			},
		},
		{
			name: "sse",
			handler: func(server *mcp.Server) http.Handler {
				return mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			},
			transport: func(endpoint string, client *http.Client) mcp.Transport {
				return &mcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: client}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newWikiResourceTestServer(t, configPath)
			authenticator, err := auth.New(auth.Config{Tenants: []auth.TenantConfig{
				{Name: "tenant-a", Token: "token-a", UserID: "user-a", AllowedTools: []string{"*"}},
				{Name: "tenant-b", Token: "token-b", UserID: "user-b", AllowedTools: []string{"*"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			handler := userIDMiddleware(authenticator.Middleware(tt.handler(server)))
			httpServer := httptest.NewServer(handler)
			defer httpServer.Close()

			t.Run("no route uses token principal", func(t *testing.T) {
				roundTripper := newWikiResourceAuthRoundTripper("token-a", "")
				transport := tt.transport(httpServer.URL, &http.Client{Transport: roundTripper})
				assertAuthenticatedWikiResources(t, transport, "Tenant A Guide", "Tenant A body")
			})

			t.Run("matching route uses token principal", func(t *testing.T) {
				roundTripper := newWikiResourceAuthRoundTripper("token-b", "user-b")
				transport := tt.transport(httpServer.URL, &http.Client{Transport: roundTripper})
				assertAuthenticatedWikiResources(t, transport, "Tenant B Guide", "Tenant B body")
			})

			t.Run("conflicting route rejects resource read", func(t *testing.T) {
				roundTripper := newWikiResourceAuthRoundTripper("token-a", "")
				transport := tt.transport(httpServer.URL, &http.Client{Transport: roundTripper})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				client := mcp.NewClient(&mcp.Implementation{Name: "authenticated-resource-test", Version: "1.0.0"}, nil)
				session, err := client.Connect(ctx, transport, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()

				roundTripper.setRoutedUser("user-b")
				if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "wiki://catalog"}); err == nil {
					t.Fatal("resources/read accepted a routed user that conflicts with the authenticated principal")
				}
				if status := roundTripper.responseStatus(); status != http.StatusForbidden {
					t.Fatalf("resources/read HTTP status=%d, want 403", status)
				}
			})
		})
	}
}

type wikiResourceAuthRoundTripper struct {
	mu         sync.Mutex
	token      string
	routedUser string
	status     int
}

func newWikiResourceAuthRoundTripper(token, routedUser string) *wikiResourceAuthRoundTripper {
	return &wikiResourceAuthRoundTripper{token: token, routedUser: routedUser}
}

func (t *wikiResourceAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	routedUser := t.routedUser
	t.mu.Unlock()

	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	query := cloned.URL.Query()
	if routedUser == "" {
		query.Del("userId")
	} else {
		query.Set("userId", routedUser)
	}
	cloned.URL.RawQuery = query.Encode()
	response, err := http.DefaultTransport.RoundTrip(cloned)
	if response != nil {
		t.mu.Lock()
		t.status = response.StatusCode
		t.mu.Unlock()
	}
	return response, err
}

func (t *wikiResourceAuthRoundTripper) setRoutedUser(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routedUser = userID
}

func (t *wikiResourceAuthRoundTripper) responseStatus() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
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

func assertAuthenticatedWikiResources(t *testing.T, transport mcp.Transport, wantTitle, wantBody string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "authenticated-resource-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "wiki://catalog"})
	if err != nil || len(read.Contents) != 1 {
		t.Fatalf("catalog=%+v err=%v", read, err)
	}
	var catalog wikibackend.ResourceCatalog
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 1 || catalog.Items[0].Name != wantTitle {
		t.Fatalf("catalog=%+v, want only %q", catalog, wantTitle)
	}
	page, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: catalog.Items[0].URI})
	if err != nil || len(page.Contents) != 1 || page.Contents[0].Text != wantBody {
		t.Fatalf("page=%+v err=%v, want %q", page, err, wantBody)
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

func newMultiTenantWikiResourceTransportConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTenant := func(dirName, title, body string) {
		t.Helper()
		contentDir := filepath.Join(root, dirName, "content")
		if err := os.MkdirAll(contentDir, 0o700); err != nil {
			t.Fatal(err)
		}
		page := "---\npage_id: shared-page\ntitle: " + title + "\n---\n\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(contentDir, "shared-page.md"), []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTenant("default", "Default Guide", "Default body")
	writeTenant("tenant-a", "Tenant A Guide", "Tenant A body")
	writeTenant("tenant-b", "Tenant B Guide", "Tenant B body")

	configPath := filepath.Join(root, "wiki.json")
	config := `{
		"mode":"local",
		"resources":{"enabled":true},
		"local":{
			"root":"default","content_dirs":["content"],"write_dir":"content",
			"require_user_mapping":true,
			"users":{
				"user-a":{"root":"tenant-a","content_dirs":["content"],"write_dir":"content"},
				"user-b":{"root":"tenant-b","content_dirs":["content"],"write_dir":"content"}
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}
