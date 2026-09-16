package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeSearchFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileSearchFieldsAndRanking(t *testing.T) {
	root := t.TempDir()
	writeSearchFile(t, root, "a.txt", "项目介绍\n这里记录部署方案及详细步骤。")
	writeSearchFile(t, root, "b.md", "# 部署方案\n安装指南")
	writeSearchFile(t, root, "部署方案.txt", "操作说明")
	writeSearchFile(t, root, "sub/Manual.TXT", "HELLO world")
	svc, err := NewFileService(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, query, scope string
		want               []string
	}{
		{"all", " 部署方案 ", "", []string{"b.md", "部署方案.txt", "a.txt"}},
		{"title", "部署方案", "title", []string{"b.md", "部署方案.txt"}},
		{"content", "部署方案", "content", []string{"a.txt", "b.md"}},
		{"filename", "b.md", "title", []string{"b.md"}},
		{"case insensitive", "hello", " CONTENT ", []string{"sub/Manual.TXT"}},
		{"no match", "missing", "all", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, err := svc.Search(context.Background(), tc.query, tc.scope, 20)
			if err != nil || items == nil || len(items) != len(tc.want) {
				t.Fatalf("items=%+v err=%v, want paths=%v", items, err, tc.want)
			}
			for i, want := range tc.want {
				if items[i].Path != want || len(items[i].MatchedFields) == 0 {
					t.Errorf("item[%d]=%+v, want path=%s", i, items[i], want)
				}
			}
		})
	}
}

func TestFileSearchBoundariesAndTextFormats(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeSearchFile(t, outside, "secret.txt", "needle outside")
	writeSearchFile(t, root, ".hidden.txt", "needle hidden")
	writeSearchFile(t, root, ".private/secret.txt", "needle hidden directory")
	writeSearchFile(t, root, "binary.txt", "needle\x00binary")
	writeSearchFile(t, root, "invalid.txt", "needle\xff")
	writeSearchFile(t, root, "large.txt", strings.Repeat("x", 2*1024*1024)+"needle")
	writeSearchFile(t, root, "office.pdf", "needle unsupported body")
	writeSearchFile(t, root, "safe/data.json", `{"text":"needle"}`)
	writeSearchFile(t, root, "safe/note.markdown", "# Safe\nneedle")
	for name, target := range map[string]string{
		"link.txt": filepath.Join(outside, "secret.txt"), "linked": outside,
		"inside.txt": filepath.Join(root, "safe", "note.markdown"),
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewFileService(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Search(context.Background(), "needle", "content", 20)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v err=%v, want two safe text files", items, err)
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Path, "safe/") || filepath.IsAbs(item.Path) {
			t.Errorf("unexpected path: %q", item.Path)
		}
	}
}

func TestFileSearchLimitsSnippetAndRefresh(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 105; i++ {
		writeSearchFile(t, root, fmt.Sprintf("%03d.txt", i), strings.Repeat("前文。", 150)+"目标内容"+strings.Repeat("后文。", 150))
	}
	svc, err := NewFileService(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ limit, want int }{{0, 20}, {3, 3}, {200, 100}} {
		items, err := svc.Search(context.Background(), "目标内容", "content", tc.limit)
		if err != nil || len(items) != tc.want {
			t.Fatalf("limit=%d items=%d err=%v", tc.limit, len(items), err)
		}
		if items[0].Path != "000.txt" || !strings.Contains(items[0].Snippet, "目标内容") ||
			!utf8.ValidString(items[0].Snippet) || len([]rune(items[0].Snippet)) > 242 {
			t.Fatalf("invalid first result: %+v", items[0])
		}
	}
	writeSearchFile(t, root, "fresh.txt", "新版本")
	items, err := svc.Search(context.Background(), "新版本", "content", 20)
	if err != nil || len(items) != 1 || items[0].Path != "fresh.txt" {
		t.Fatalf("new file not visible: %+v err=%v", items, err)
	}
	writeSearchFile(t, root, "fresh.txt", "已修改")
	items, err = svc.Search(context.Background(), "新版本", "content", 20)
	if err != nil || len(items) != 0 {
		t.Fatalf("stale content returned: %+v err=%v", items, err)
	}
}

func TestFileSearchValidationAndCancellation(t *testing.T) {
	for _, root := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		if _, err := NewFileService(root); err == nil {
			t.Fatalf("accepted invalid root %q", root)
		}
	}
	svc, err := NewFileService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, scope string }{{" ", "all"}, {"x", "invalid"}, {strings.Repeat("中", 257), "all"}} {
		if _, err := svc.Search(context.Background(), tc.query, tc.scope, 20); err == nil {
			t.Errorf("accepted invalid query=%q scope=%q", tc.query, tc.scope)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Search(ctx, "x", "all", 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search returned %v", err)
	}
}
