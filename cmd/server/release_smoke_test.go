package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReleaseBinarySmoke(t *testing.T) {
	binary := os.Getenv("MCP_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set MCP_RELEASE_BINARY to smoke test a built release binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "server.log")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-transport=stdio", "-logfile="+logPath)
	command.Env = []string{
		"MCP_ENABLED_TOOLS=file_*",
		"FILE_SEARCH_ROOT=" + root,
		"FILE_SEARCH_TOKENIZER=builtin",
		"API_TOKEN=",
		"BASE_URL=",
		"AUTH_TOKEN=",
		"AUTH_TENANTS=",
		"AUTH_REMOTE_VERIFY_URL=",
		"AUTH_IP_ALLOWLIST=",
		"AUTH_REMOTE_CACHE_MAX_ENTRIES=4096",
		"LOG_HTTP_PAYLOADS=false",
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "release-smoke-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("initialize release binary: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list release binary tools: %v", err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"file_get_info", "file_read_preview", "file_search"}
	if !slices.Equal(names, want) {
		t.Fatalf("release binary tools = %v, want %v", names, want)
	}
}
