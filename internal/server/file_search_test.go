package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFileSearchStandalone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("# 安装指南\n部署方法"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILE_SEARCH_ROOT", root)
	t.Setenv("MCP_ENABLED_TOOLS", "file_search")
	t.Setenv("API_TOKEN", "")
	t.Setenv("BASE_URL", "")
	// An invalid, unrelated configuration must not prevent standalone file search.
	unrelatedConfig := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(unrelatedConfig, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, session := connectRegisteredServer(t, unrelatedConfig)
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "file_search" {
		t.Fatalf("tools=%+v err=%v", listed, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "file_search", Arguments: map[string]any{"query": "部署", "search_in": "content"},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("call result=%+v err=%v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(encoded), "guide.md") || !strings.Contains(string(encoded), "安装指南") {
		t.Fatalf("structured result=%s err=%v", encoded, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "file_search", Arguments: map[string]any{"query": "不存在"}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("empty search result=%+v err=%v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil || string(encoded) != `{"items":[]}` {
		t.Fatalf("empty structured result=%s err=%v", encoded, err)
	}
}

func TestFileToolsStandaloneAllowlist(t *testing.T) {
	tests := []struct {
		name      string
		allowlist string
		tokenizer string
		want      []string
	}{
		{
			name:      "two file tools",
			allowlist: "file_search,file_get_info",
			want:      []string{"file_get_info", "file_search"},
		},
		{
			name:      "file wildcard",
			allowlist: "file_*",
			want:      []string{"file_get_info", "file_read_preview", "file_search"},
		},
		{
			name:      "metadata tools with duplicate",
			allowlist: "file_get_info,file_get_info,file_read_preview",
			tokenizer: "unsupported",
			want:      []string{"file_get_info", "file_read_preview"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FILE_SEARCH_ROOT", t.TempDir())
			t.Setenv("FILE_SEARCH_TOKENIZER", tt.tokenizer)
			t.Setenv("MCP_ENABLED_TOOLS", tt.allowlist)
			t.Setenv("API_TOKEN", "")
			t.Setenv("BASE_URL", "")
			unrelatedConfig := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(unrelatedConfig, []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, session := connectRegisteredServer(t, unrelatedConfig)
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range listed.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, tt.want) {
				t.Fatalf("registered tools = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestFileToolsMixedWithUpstreamStillRequiresToken(t *testing.T) {
	t.Setenv("FILE_SEARCH_ROOT", t.TempDir())
	t.Setenv("MCP_ENABLED_TOOLS", "file_search,student_search")
	t.Setenv("API_TOKEN", "")
	t.Setenv("BASE_URL", "")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	err := RegisterAll(server, filepath.Join(t.TempDir(), "missing.json"))
	if err == nil || !strings.Contains(err.Error(), "missing required env API_TOKEN") {
		t.Fatalf("mixed tool registration error = %v, want missing API_TOKEN", err)
	}
}

func TestFileToolsMixedWithLocalWikiDoesNotRequireToken(t *testing.T) {
	fileRoot := t.TempDir()
	wikiRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(wikiRoot, "content"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(wikiRoot, "wiki.json")
	config := `{"mode":"local","local":{"root":".","content_dirs":["content"],"write_dir":"content"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILE_SEARCH_ROOT", fileRoot)
	t.Setenv("MCP_ENABLED_TOOLS", "file_search,wiki_search")
	t.Setenv("API_TOKEN", "")
	t.Setenv("BASE_URL", "")
	ctx, session := connectRegisteredServer(t, configPath)
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"file_search", "wiki_search"}
	if !slices.Equal(names, want) {
		t.Fatalf("registered tools = %v, want %v", names, want)
	}
}

func TestFileSearchGSEMatchesSegmentedChineseAndRefreshes(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "guide.txt")
	if err := os.WriteFile(filePath, []byte("北京的大学提供课程"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILE_SEARCH_ROOT", root)
	t.Setenv("FILE_SEARCH_TOKENIZER", "gse")
	t.Setenv("FILE_SEARCH_GSE_DICTIONARY", "zh")
	t.Setenv("MCP_ENABLED_TOOLS", "file_search")
	t.Setenv("API_TOKEN", "")
	t.Setenv("BASE_URL", "")

	ctx, session := connectRegisteredServer(t, "")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "file_search", Arguments: map[string]any{"query": "北京大学", "search_in": "content"},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("segmented search result=%+v err=%v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(encoded), "guide.txt") {
		t.Fatalf("segmented search=%s err=%v, want guide.txt", encoded, err)
	}

	if err := os.WriteFile(filePath, []byte("上海的学院提供课程"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "file_search", Arguments: map[string]any{"query": "北京大学", "search_in": "content"},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("refreshed search result=%+v err=%v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil || string(encoded) != `{"items":[]}` {
		t.Fatalf("refreshed search=%s err=%v, want no stale result", encoded, err)
	}
}

func TestFileSearchRejectsUnsupportedTokenizer(t *testing.T) {
	t.Setenv("FILE_SEARCH_ROOT", t.TempDir())
	t.Setenv("FILE_SEARCH_TOKENIZER", "unknown")
	t.Setenv("MCP_ENABLED_TOOLS", "file_search")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if err := RegisterAll(server); err == nil || !strings.Contains(err.Error(), "FILE_SEARCH_TOKENIZER") {
		t.Fatalf("unsupported tokenizer error=%v, want FILE_SEARCH_TOKENIZER validation", err)
	}
}

func TestFileSearchRejectsUnsupportedGSEDictionary(t *testing.T) {
	t.Setenv("FILE_SEARCH_ROOT", t.TempDir())
	t.Setenv("FILE_SEARCH_TOKENIZER", "gse")
	t.Setenv("FILE_SEARCH_GSE_DICTIONARY", "unknown")
	t.Setenv("MCP_ENABLED_TOOLS", "file_search")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if err := RegisterAll(server); err == nil || !strings.Contains(err.Error(), "FILE_SEARCH_GSE_DICTIONARY") {
		t.Fatalf("unsupported dictionary error=%v, want FILE_SEARCH_GSE_DICTIONARY validation", err)
	}
}

func TestFileSearchBuiltinIgnoresGSEDictionary(t *testing.T) {
	t.Setenv("FILE_SEARCH_ROOT", t.TempDir())
	t.Setenv("FILE_SEARCH_TOKENIZER", "builtin")
	t.Setenv("FILE_SEARCH_GSE_DICTIONARY", "unknown")
	t.Setenv("MCP_ENABLED_TOOLS", "file_search")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if err := RegisterAll(server); err != nil {
		t.Fatalf("builtin tokenizer should ignore GSE dictionary, got %v", err)
	}
}

func TestFileMetadataToolsIgnoreFileSearchTokenizerConfig(t *testing.T) {
	t.Setenv("FILE_SEARCH_ROOT", t.TempDir())
	t.Setenv("FILE_SEARCH_TOKENIZER", "unknown")
	t.Setenv("MCP_ENABLED_TOOLS", "file_get_info")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if err := RegisterAll(server); err != nil {
		t.Fatalf("file metadata tool should not load file_search config, got %v", err)
	}
}

func TestFileSearchConfiguration(t *testing.T) {
	set, err := parseEnabledTools("file_search")
	if err != nil || len(set) != 1 || !set["file_search"] {
		t.Fatalf("allowlist file_search: set=%v err=%v", set, err)
	}
	set, err = parseEnabledTools("file_*")
	if err != nil || len(set) != 3 || !set["file_search"] || !set["file_get_info"] || !set["file_read_preview"] {
		t.Fatalf("allowlist file_*: set=%v err=%v", set, err)
	}
	for _, allowlist := range []string{"file_search", "file_*"} {
		for _, root := range []string{"", filepath.Join(t.TempDir(), "missing")} {
			t.Setenv("FILE_SEARCH_ROOT", root)
			t.Setenv("MCP_ENABLED_TOOLS", allowlist)
			s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			if err := RegisterAll(s); err == nil || !strings.Contains(err.Error(), "FILE_SEARCH_ROOT") {
				t.Fatalf("allowlist %q root %q: err=%v", allowlist, root, err)
			}
		}
	}
}
