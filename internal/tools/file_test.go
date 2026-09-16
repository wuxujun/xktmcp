package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wuxujun/xktmcp/internal/service"
)

func TestFileSearchToolSchema(t *testing.T) {
	tool := FileSearchTool()
	if tool.Name != "file_search" {
		t.Fatalf("name=%q", tool.Name)
	}
	schema := tool.InputSchema.(*jsonschema.Schema)
	if len(schema.Required) != 1 || schema.Required[0] != "query" || len(schema.Properties["search_in"].Enum) != 3 {
		t.Fatalf("unexpected input schema: %+v", schema)
	}
	for _, name := range append(envelopeFields, "root", "path") {
		if _, ok := schema.Properties[name]; ok {
			t.Errorf("internal field %q is public", name)
		}
	}
	if tool.OutputSchema.(*jsonschema.Schema).Type != "object" {
		t.Fatal("output schema must have an object root")
	}
}

func TestFileSearchHandlerRedactionAndErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "contact.txt"), []byte("联系人电话：13812345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewFileService(root)
	if err != nil {
		t.Fatal(err)
	}
	handler := FileSearchHandler(svc)
	res, out, err := handler(context.Background(), nil, FileSearchArgs{Query: "联系人", SearchIn: "content"})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{text, string(encoded)} {
		if strings.Contains(value, "13812345678") || !strings.Contains(value, "138****5678") || strings.Contains(value, root) {
			t.Errorf("unexpected output: %s", value)
		}
	}
	for _, args := range []FileSearchArgs{{Query: " "}, {Query: "x", SearchIn: "invalid"}} {
		res, _, err := handler(context.Background(), nil, args)
		if err != nil || res == nil || !res.IsError {
			t.Fatalf("invalid arguments: result=%+v err=%v", res, err)
		}
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	res, _, err = handler(context.Background(), nil, FileSearchArgs{Query: "联系人"})
	if err != nil || res == nil || !res.IsError || strings.Contains(res.Content[0].(*mcp.TextContent).Text, root) {
		t.Fatalf("filesystem error must not expose root: result=%+v err=%v", res, err)
	}
}
