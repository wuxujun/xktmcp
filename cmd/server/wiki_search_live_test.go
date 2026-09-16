package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/model"
)

// findRepoFile searches candidate relative paths to locate a repository file.
func findRepoFile(relPath string) (string, error) {
	candidates := []string{
		relPath,
		filepath.Join("..", "..", relPath),
		filepath.Join("..", relPath),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs, nil
			}
			return c, nil
		}
	}
	return "", fmt.Errorf("repository file not found: %s", relPath)
}

// TestLiveWikiSearchPort8081 tests the wiki search and retrieval functionality
// against the live server running on port 8081 (or MCP_LIVE_SERVER_URL).
// It also reads and verifies wiki-search-session-summary.md.
func TestLiveWikiSearchPort8081(t *testing.T) {
	if strings.ToLower(strings.TrimSpace(os.Getenv("MCP_RUN_LIVE_TESTS"))) != "true" {
		t.Skip("set MCP_RUN_LIVE_TESTS=true to run against a live MCP server")
	}
	serverURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MCP_LIVE_SERVER_URL")), "/")
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8081"
	}

	// 1. Verify health probe of the live server
	healthResp, err := http.Get(serverURL + "/health")
	if err != nil {
		t.Skipf("Live server at %s is not reachable (%v); skipping live test", serverURL, err)
		return
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status=%d, want 200", healthResp.StatusCode)
	}
	healthBody, _ := io.ReadAll(healthResp.Body)
	if !strings.Contains(string(healthBody), `"status":"ok"`) {
		t.Fatalf("GET /health body=%s, want status ok", string(healthBody))
	}

	// 2. Verify ready probe of the live server
	readyResp, err := http.Get(serverURL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready failed: %v", err)
	}
	defer readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ready status=%d, want 200", readyResp.StatusCode)
	}

	// 3. Verify unauthenticated request to /mcp is rejected with 401
	unauthReq, err := http.NewRequest(http.MethodPost, serverURL+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauthenticated /mcp request error: %v", err)
	}
	defer unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /mcp status=%d, want 401", unauthResp.StatusCode)
	}

	// 4. Read wiki-search-session-summary.md and verify its content
	summaryPath, err := findRepoFile("docs/wiki-search-session-summary.md")
	if err != nil {
		t.Fatalf("find wiki-search-session-summary.md failed: %v", err)
	}
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read wiki-search-session-summary.md failed: %v", err)
	}
	summaryContent := string(summaryBytes)
	if len(summaryContent) == 0 {
		t.Fatal("wiki-search-session-summary.md is empty")
	}
	if !strings.Contains(summaryContent, "Wiki Search") {
		t.Fatal("wiki-search-session-summary.md does not contain expected header")
	}
	t.Logf("Successfully read %s (%d bytes)", filepath.Base(summaryPath), len(summaryBytes))

	// 5. Load AUTH_TOKEN from .env or environment
	authToken := strings.TrimSpace(os.Getenv("AUTH_TOKEN"))
	if authToken == "" {
		if envFile, err := findRepoFile(".env"); err == nil {
			if envMap, err := godotenv.Read(envFile); err == nil {
				authToken = strings.TrimSpace(envMap["AUTH_TOKEN"])
			}
		}
	}
	if authToken == "" {
		t.Fatal("AUTH_TOKEN is required to test authenticated live server")
	}

	// 6. Connect authenticated MCP client over Streamable HTTP to tenant 0002
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	roundTripper := newWikiResourceAuthRoundTripper(authToken, "0002")
	transport := &mcp.StreamableClientTransport{
		Endpoint:             serverURL + "/mcp",
		HTTPClient:           &http.Client{Transport: roundTripper},
		DisableStandaloneSSE: true,
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "wiki-search-live-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("failed to connect to live MCP server at %s/mcp: %v", serverURL, err)
	}
	defer session.Close()

	// 7. Test wiki_search tool with a real query
	t.Run("wiki_search_query", func(t *testing.T) {
		start := time.Now()
		searchRes, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "wiki_search",
			Arguments: map[string]any{
				"query": "比赛",
				"top_k": 5,
			},
		})
		latency := time.Since(start)
		if err != nil {
			t.Fatalf("wiki_search failed: %v", err)
		}
		if searchRes == nil || searchRes.IsError {
			t.Fatalf("wiki_search returned error result: %+v", searchRes)
		}
		if len(searchRes.Content) == 0 {
			t.Fatal("wiki_search returned no content")
		}

		textContent, ok := searchRes.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %T", searchRes.Content[0])
		}

		var items []model.WikiSearchResult
		if err := json.Unmarshal([]byte(textContent.Text), &items); err != nil {
			t.Fatalf("failed to unmarshal wiki_search text JSON: %v, raw=%s", err, textContent.Text)
		}
		if len(items) == 0 {
			t.Fatal("wiki_search returned 0 items for '比赛'")
		}

		t.Logf("wiki_search query='比赛' latency=%s items_count=%d top_title=%q",
			latency, len(items), items[0].Title)

		// 8. Test wiki_get_page tool to retrieve the first document found
		t.Run("wiki_get_page", func(t *testing.T) {
			firstDoc := items[0]
			pageRes, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "wiki_get_page",
				Arguments: map[string]any{
					"page_id": firstDoc.PageID,
				},
			})
			if err != nil {
				t.Fatalf("wiki_get_page failed: %v", err)
			}
			if pageRes == nil || pageRes.IsError {
				t.Fatalf("wiki_get_page returned error result: %+v", pageRes)
			}
			if len(pageRes.Content) == 0 {
				t.Fatal("wiki_get_page returned empty content")
			}

			pageText, ok := pageRes.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("expected TextContent, got %T", pageRes.Content[0])
			}
			var page model.WikiPage
			if err := json.Unmarshal([]byte(pageText.Text), &page); err != nil {
				t.Fatalf("failed to unmarshal wiki page: %v, raw=%s", err, pageText.Text)
			}
			if page.PageID != firstDoc.PageID {
				t.Errorf("page.PageID = %q, want %q", page.PageID, firstDoc.PageID)
			}
			if len(page.Content) == 0 {
				t.Error("page.Content is empty")
			}
			t.Logf("wiki_get_page page_id=%q title=%q content_len=%d", page.PageID, page.Title, len(page.Content))
		})
	})

	// 9. Test wiki_search tool with non-existent query (should return 0 results cleanly)
	t.Run("wiki_search_absent_query", func(t *testing.T) {
		searchRes, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "wiki_search",
			Arguments: map[string]any{
				"query": "zzxxyywwqq",
				"top_k": 5,
			},
		})
		if err != nil {
			t.Fatalf("wiki_search absent query failed: %v", err)
		}
		if searchRes == nil || searchRes.IsError {
			t.Fatalf("wiki_search returned error: %+v", searchRes)
		}
		textContent, ok := searchRes.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %T", searchRes.Content[0])
		}
		var items []model.WikiSearchResult
		if err := json.Unmarshal([]byte(textContent.Text), &items); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("expected 0 items for absent query, got %d", len(items))
		}
	})
}

// TestWikiSearchAndReadSessionSummaryCorpus tests reading wiki-search-session-summary.md,
// indexing it into a wiki corpus, searching for key phrases from it, and reading it back
// via wiki search and get_page tools.
func TestWikiSearchAndReadSessionSummaryCorpus(t *testing.T) {
	t.Setenv("MCP_ENABLED_TOOLS", "wiki_search,wiki_get_page,wiki_list_tree,wiki_upsert_page,wiki_get_backlinks")

	// 1. Locate and read the wiki-search-session-summary.md file
	summaryPath, err := findRepoFile("docs/wiki-search-session-summary.md")
	if err != nil {
		t.Fatalf("find wiki-search-session-summary.md failed: %v", err)
	}
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read wiki-search-session-summary.md failed: %v", err)
	}
	if len(summaryBytes) == 0 {
		t.Fatal("wiki-search-session-summary.md is empty")
	}

	// 2. Set up a temporary wiki directory with wiki-search-session-summary.md as a document
	root := t.TempDir()
	contentDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(contentDir, 0o700); err != nil {
		t.Fatal(err)
	}

	targetDocPath := filepath.Join(contentDir, "wiki-search-session-summary.md")
	if err := os.WriteFile(targetDocPath, summaryBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(root, "wiki.json")
	configJSON := fmt.Sprintf(`{
		"mode": "local",
		"resources": {
			"enabled": true,
			"link_base_url": "https://wiki.example.com/page/"
		},
		"local": {
			"root": %q,
			"content_dirs": ["wiki"],
			"write_dir": "wiki",
			"default_category": "docs",
			"tokenizer": "builtin"
		}
	}`, root)
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// 3. Start test MCP server over Streamable HTTP
	server := newWikiResourceTestServer(t, configPath)
	httpServer := httptest.NewServer(newStreamableHTTPHandler(server))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	transport := &mcp.StreamableClientTransport{Endpoint: httpServer.URL}
	client := mcp.NewClient(&mcp.Implementation{Name: "summary-search-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer session.Close()

	// 4. Test searching for phrases known to exist in wiki-search-session-summary.md
	testQueries := []struct {
		name  string
		query string
	}{
		{name: "search_backlinks", query: "反向链接"},
		{name: "search_candidate_buckets", query: "候选桶"},
		{name: "search_gse_tokenizer", query: "GSE"},
		{name: "search_title_words", query: "优化会话总结"},
	}

	for _, tq := range testQueries {
		t.Run(tq.name, func(t *testing.T) {
			searchRes, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "wiki_search",
				Arguments: map[string]any{
					"query": tq.query,
					"top_k": 5,
				},
			})
			if err != nil {
				t.Fatalf("search query %q failed: %v", tq.query, err)
			}
			if searchRes == nil || searchRes.IsError {
				t.Fatalf("search query %q returned error: %+v", tq.query, searchRes)
			}
			if len(searchRes.Content) == 0 {
				t.Fatalf("search query %q returned no content", tq.query)
			}

			textContent, ok := searchRes.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("expected TextContent, got %T", searchRes.Content[0])
			}

			var items []model.WikiSearchResult
			if err := json.Unmarshal([]byte(textContent.Text), &items); err != nil {
				t.Fatalf("failed to unmarshal search results: %v", err)
			}
			if len(items) == 0 {
				t.Fatalf("query %q did not match wiki-search-session-summary.md", tq.query)
			}

			// Verify the matched result corresponds to the session summary
			found := false
			for _, item := range items {
				if strings.Contains(item.Title, "Wiki Search 优化会话总结") ||
					strings.Contains(item.PageID, "wiki-search-session-summary") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("query %q returned items, but none matched wiki-search-session-summary. items=%+v", tq.query, items)
			}
		})
	}

	// 5. Test reading the full page content of wiki-search-session-summary.md via wiki_get_page
	t.Run("read_session_summary_page", func(t *testing.T) {
		pageRes, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "wiki_get_page",
			Arguments: map[string]any{
				"page_id": "wiki/wiki-search-session-summary",
			},
		})
		if err != nil {
			t.Fatalf("wiki_get_page failed: %v", err)
		}
		if pageRes == nil || pageRes.IsError {
			t.Fatalf("wiki_get_page error result: %+v", pageRes)
		}
		pageText, ok := pageRes.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %T", pageRes.Content[0])
		}
		var page model.WikiPage
		if err := json.Unmarshal([]byte(pageText.Text), &page); err != nil {
			t.Fatalf("unmarshal page failed: %v", err)
		}
		if !strings.Contains(page.Content, "Wiki Search 优化会话总结") {
			t.Fatalf("retrieved page content missing expected header")
		}
		if !strings.Contains(page.Content, "反向链接") {
			t.Fatalf("retrieved page content missing '反向链接'")
		}
		t.Logf("Successfully read page_id=%q title=%q content_len=%d", page.PageID, page.Title, len(page.Content))
	})
}
