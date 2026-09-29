package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileServiceNamesContentInfoAndPreview(t *testing.T) {
	root := t.TempDir()
	writeSearchFile(t, root, "Guide-2026.md", "# Deploy Guide\nline one\nneedle here\nline four\n")
	writeSearchFile(t, root, "notes.txt", "alpha\nneedle in notes\nomega\n")
	svc, err := NewFileService(root)
	if err != nil {
		t.Fatal(err)
	}

	names, err := svc.SearchFileNames(context.Background(), `(?i)guide-\d+`, true, 10)
	if err != nil || len(names) != 1 || names[0].Path != "Guide-2026.md" {
		t.Fatalf("names=%+v err=%v", names, err)
	}
	hits, err := svc.SearchFileContent(context.Background(), "needle", []string{".md"}, 1, 10)
	if err != nil || len(hits) != 1 || hits[0].Line != 3 || !strings.Contains(hits[0].Context, "line one") {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	info, err := svc.GetFileInfo(context.Background(), "Guide-2026.md")
	if err != nil || info.SizeBytes == 0 || info.Extension != ".md" || info.MIMEType == "" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	preview, err := svc.ReadFilePreview(context.Background(), "Guide-2026.md", 2, 3)
	if err != nil || preview.StartLine != 2 || preview.EndLine != 3 || !strings.Contains(preview.Content, "line one") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := svc.GetFileInfo(context.Background(), "../Guide-2026.md"); err == nil {
		t.Fatal("path traversal accepted")
	}
	for _, badPath := range []string{"..", "folder/..", "folder/../..", "/etc/passwd", "dir\\file"} {
		if _, err := svc.GetFileInfo(context.Background(), badPath); err == nil {
			t.Fatalf("expected path %q to be rejected", badPath)
		}
	}
	if _, err := svc.ReadFilePreview(context.Background(), "Guide-2026.md", 0, 2); err == nil {
		t.Fatal("invalid line accepted")
	}
	writeSearchFile(t, root, "large.log", strings.Repeat("padding\n", 600000)+"target\n")
	preview, err = svc.ReadFilePreview(context.Background(), "large.log", 600001, 600001)
	if err != nil || preview.Content != "target" {
		t.Fatalf("large preview=%+v err=%v", preview, err)
	}
	if _, err := svc.SearchFileNames(context.Background(), "[", true, 10); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTokenizedFileSearchStreamingSnippet(t *testing.T) {
	root := t.TempDir()
	content := "首段介绍。\n这里包含特殊的检索词条项，提供按需摘要提取功能。\n尾段说明。"
	writeSearchFile(t, root, "streaming.txt", content)

	svc, err := NewFileServiceWithOptions(root, FileSearchOptions{Tokenizer: FileSearchTokenizerBuiltin})
	if err != nil {
		t.Fatal(err)
	}

	results, err := svc.Search(context.Background(), "检索词条", "content", 10)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Snippet, "检索词条") {
		t.Fatalf("expected snippet to contain match, got %q", results[0].Snippet)
	}
	if results[0].Path != "streaming.txt" {
		t.Fatalf("expected path streaming.txt, got %s", results[0].Path)
	}
}
