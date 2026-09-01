package wiki

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSearcherSearchesCompiledArticles(t *testing.T) {
	root := t.TempDir()
	concepts := filepath.Join(root, "wiki", "concepts")
	if err := os.MkdirAll(concepts, 0o755); err != nil {
		t.Fatal(err)
	}
	article := `---
page_id: go-concurrency
title: "Go 并发模型"
summary: "Goroutine 与 Channel 的协作方式"
category: development
updated: 2026-08-19
---
# Go 并发模型

使用 context 控制并发任务的取消与超时。
`
	if err := os.WriteFile(filepath.Join(concepts, "go-concurrency.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(concepts, "_index.md"), []byte("并发任务派生索引"), 0o600); err != nil {
		t.Fatal(err)
	}

	searcher, err := NewLocalSearcher(LocalConfig{
		Root:                   root,
		ContentDirs:            []string{"wiki"},
		RefreshIntervalSeconds: 30,
		MaxFileSizeBytes:       2 << 20,
	})
	if err != nil {
		t.Fatalf("NewLocalSearcher returned error: %v", err)
	}

	results, err := searcher.SearchWiki(context.Background(), "user-1", "并发任务", "development", 5)
	if err != nil {
		t.Fatalf("SearchWiki returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one article", results)
	}
	if results[0].PageID != "go-concurrency" || results[0].Title != "Go 并发模型" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
	if results[0].Score <= 0 {
		t.Fatalf("score = %v, want positive", results[0].Score)
	}
}

func TestLocalSearcherFiltersCategory(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "wiki")
	if err := os.MkdirAll(articles, 0o755); err != nil {
		t.Fatal(err)
	}
	article := "---\ntitle: Search Guide\ncategory: docs\n---\n\nLocal search content."
	if err := os.WriteFile(filepath.Join(articles, "search.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"},
		RefreshIntervalSeconds: 30, MaxFileSizeBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := searcher.SearchWiki(context.Background(), "", "search", "engineering", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want no category mismatch", results)
	}
}

func TestLocalSearcherSplitsMixedLatinAndChineseQuery(t *testing.T) {
	root := t.TempDir()
	articles := filepath.Join(root, "wiki", "concepts")
	if err := os.MkdirAll(articles, 0o755); err != nil {
		t.Fatal(err)
	}
	article := "# 城市探索 PBL 课程\n\n面向学生的项目式学习课程。"
	if err := os.WriteFile(filepath.Join(articles, "urban-pbl.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"},
		RefreshIntervalSeconds: 30, MaxFileSizeBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := searcher.SearchWiki(context.Background(), "", "PBL信息", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "城市探索 PBL 课程" {
		t.Fatalf("results = %+v, want mixed-language query to match PBL article", results)
	}
}

func TestLocalSearcherRefreshReplacesChangedAndDeletedBacklinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("target-a.md", "---\npage_id: target-a\ntitle: Target A\n---\n")
	write("target-b.md", "---\npage_id: target-b\ntitle: Target B\n---\n")
	write("source.md", "---\npage_id: source\ntitle: Source\n---\nSee [target A](target-a.md).\n")

	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	links, err := searcher.GetBacklinks(context.Background(), "", "target-a")
	if err != nil || len(links) != 1 || links[0].SourcePageID != "source" {
		t.Fatalf("initial backlinks = %+v, err=%v", links, err)
	}

	write("source.md", "---\npage_id: source\ntitle: Source\n---\nSee [target B](target-b.md).\n")
	if err := searcher.refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	links, err = searcher.GetBacklinks(context.Background(), "", "target-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("changed link remained in backlinks = %+v", links)
	}
	links, err = searcher.GetBacklinks(context.Background(), "", "target-b")
	if err != nil || len(links) != 1 || links[0].SourcePageID != "source" {
		t.Fatalf("changed link backlinks = %+v, err=%v", links, err)
	}

	if err := os.Remove(filepath.Join(dir, "source.md")); err != nil {
		t.Fatal(err)
	}
	if err := searcher.refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	links, err = searcher.GetBacklinks(context.Background(), "", "target-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("deleted source remained in backlinks = %+v", links)
	}
}
