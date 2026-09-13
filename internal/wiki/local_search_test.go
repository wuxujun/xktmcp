package wiki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/model"
)

// Pause a refresh at its first cancellation check, after directory traversal starts.
type pausedRefreshContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func TestScoreDocumentUsesPreparedSearchFields(t *testing.T) {
	doc := localDocument{
		result:        model.WikiSearchResult{Title: "Go Concurrency", Summary: "Channel"},
		content:       "unprepared content",
		searchTitle:   "go concurrency",
		searchSummary: "channel",
		searchContent: "context cancellation",
	}
	if got := scoreDocument(doc, "context", []string{"context"}); got != 1 {
		t.Fatalf("scoreDocument = %d, want 1 from prepared search fields", got)
	}
}

func TestScoreDocumentGSEPhraseMatchRanksAboveSplitTerms(t *testing.T) {
	terms := []string{"北京", "大学"}
	phraseDoc := localDocument{
		tokenized:     true,
		searchSummary: "北京大学招生",
		searchContent: "北京大学招生简章",
		summaryTerms:  terms,
		contentTerms:  append(append([]string{}, terms...), "招生", "简章"),
	}
	splitDoc := localDocument{
		tokenized:     true,
		searchSummary: "北京的大学招生",
		searchContent: "北京的大学招生简章",
		summaryTerms:  terms,
		contentTerms:  append(append([]string{}, terms...), "招生", "简章"),
	}
	phraseScore := scoreDocument(phraseDoc, "北京大学", terms)
	splitScore := scoreDocument(splitDoc, "北京大学", terms)
	if phraseScore <= splitScore {
		t.Fatalf("phrase score = %d, split score = %d; complete phrase should rank higher", phraseScore, splitScore)
	}
	builtinPhraseDoc := phraseDoc
	builtinPhraseDoc.tokenized = false
	builtinSplitDoc := splitDoc
	builtinSplitDoc.tokenized = false
	if got, want := scoreDocument(builtinPhraseDoc, "北京大学", terms), scoreDocument(builtinSplitDoc, "北京大学", terms); got != want {
		t.Fatalf("builtin phrase score = %d, split score = %d; builtin ranking must remain unchanged", got, want)
	}
}

func TestSelectTopKPreservesWikiOrderingAndStableTies(t *testing.T) {
	matches := []wikiMatch{
		{result: model.WikiSearchResult{PageID: "first", Title: "Z", Score: 10, UpdatedAt: "2026-09-01"}, order: 0},
		{result: model.WikiSearchResult{PageID: "second", Title: "A", Score: 10, UpdatedAt: "2026-09-01"}, order: 1},
		{result: model.WikiSearchResult{PageID: "third", Title: "A", Score: 10, UpdatedAt: "2026-09-01"}, order: 2},
		{result: model.WikiSearchResult{PageID: "fourth", Title: "Newest", Score: 11, UpdatedAt: "2026-09-02"}, order: 3},
		{result: model.WikiSearchResult{PageID: "fifth", Title: "Older", Score: 9, UpdatedAt: "2026-09-03"}, order: 4},
	}
	got := selectTopK(matches, 3)
	if len(got) != 3 {
		t.Fatalf("selected %d matches, want 3", len(got))
	}
	want := []string{"fourth", "second", "third"}
	for i, pageID := range want {
		if got[i].result.PageID != pageID {
			t.Errorf("selected[%d] = %q, want %q", i, got[i].result.PageID, pageID)
		}
	}
}

func TestTermIndexCandidatesReturnUnionInDocumentOrder(t *testing.T) {
	documents := []localDocument{
		{tokenized: true, titleTerms: []string{"go", "搜索"}, contentTerms: []string{"并发"}},
		{tokenized: true, summaryTerms: []string{"搜索"}},
		{tokenized: true, contentTerms: []string{"缓存"}},
	}
	index := buildTermIndex(documents)
	got := termIndexCandidates(index, []string{"缓存", "搜索"})
	want := []int{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("candidate indexes = %v, want %v", got, want)
	}
	for i, index := range want {
		if got[i] != index {
			t.Errorf("candidate indexes[%d] = %d, want %d", i, got[i], index)
		}
	}
}

func TestLocalSearcherGSERefreshRebuildsTermIndex(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fruit.md")
	if err := os.WriteFile(path, []byte("---\ntitle: Fruit\n---\n苹果"), 0o600); err != nil {
		t.Fatal(err)
	}
	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"}, Tokenizer: SearchTokenizerGSE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := termIndexCandidates(searcher.termIndex, []string{"苹果"}); len(got) != 1 || got[0] != 0 {
		t.Fatalf("initial apple candidates = %v, want [0]", got)
	}
	if err := os.WriteFile(path, []byte("---\ntitle: Fruit\n---\n香蕉"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := searcher.refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := termIndexCandidates(searcher.termIndex, []string{"苹果"}); len(got) != 0 {
		t.Fatalf("stale apple candidates = %v, want none", got)
	}
	if got := termIndexCandidates(searcher.termIndex, []string{"香蕉"}); len(got) != 1 || got[0] != 0 {
		t.Fatalf("refreshed banana candidates = %v, want [0]", got)
	}
}

func TestLocalSearcherTopKPreservesSearchOrdering(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	articles := map[string]string{
		"page-a.md": "---\ntitle: Zulu\nupdated: 2026-09-01\n---\nneedle",
		"page-b.md": "---\ntitle: Alpha\nupdated: 2026-09-01\n---\nneedle",
		"page-c.md": "---\ntitle: Alpha\nupdated: 2026-09-01\n---\nneedle",
		"page-d.md": "---\ntitle: Newest\nupdated: 2026-09-02\n---\nneedle",
	}
	for name, content := range articles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	searcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := searcher.SearchWiki(context.Background(), "", "needle", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wiki/page-d", "wiki/page-b", "wiki/page-c"}
	if len(results) != len(want) {
		t.Fatalf("results = %+v, want %d results", results, len(want))
	}
	for i, pageID := range want {
		if results[i].PageID != pageID {
			t.Errorf("results[%d].PageID = %q, want %q", i, results[i].PageID, pageID)
		}
	}
}

func TestLocalSearcherGSEMatchesSegmentedChineseQuery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	article := "---\ntitle: 大学课程\n---\n北京的大学提供课程"
	if err := os.WriteFile(filepath.Join(dir, "university.md"), []byte(article), 0o600); err != nil {
		t.Fatal(err)
	}
	builtinSearcher, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	builtinResults, err := builtinSearcher.SearchWiki(context.Background(), "", "北京大学", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(builtinResults) != 0 {
		t.Fatalf("builtin results = %+v, want contiguous Chinese query to miss split terms", builtinResults)
	}
	searcher, err := NewLocalSearcher(LocalConfig{
		Root: root, ContentDirs: []string{"wiki"}, Tokenizer: SearchTokenizerGSE,
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := searcher.SearchWiki(context.Background(), "", "北京大学", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].PageID != "wiki/university" {
		t.Fatalf("results = %+v, want segmented Chinese query to match article", results)
	}
}

func (c *pausedRefreshContext) Err() error {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Context.Err()
}

func TestLocalSearcherSearchDuringRefresh(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "wiki", "page.md")
	if err := os.WriteFile(path, []byte("# Old title\n\nsearchable"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewLocalSearcher(LocalConfig{Root: root, ContentDirs: []string{"wiki"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# New title\n\nsearchable"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.nextRefresh = time.Time{}
	s.mu.Unlock()
	ctx := &pausedRefreshContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	refreshed := make(chan error, 1)
	go func() { refreshed <- s.refresh(ctx, true) }()
	<-ctx.entered
	defer func() {
		close(ctx.release)
		if err := <-refreshed; err != nil {
			t.Error(err)
		}
		items, err := s.SearchWiki(context.Background(), "", "searchable", "", 5)
		if err != nil || len(items) != 1 || items[0].Title != "New title" {
			t.Errorf("expected refreshed snapshot, got %+v, err=%v", items, err)
		}
	}()
	searched := make(chan error, 1)
	go func() {
		items, err := s.SearchWiki(context.Background(), "", "searchable", "", 5)
		if err == nil && (len(items) != 1 || items[0].Title != "Old title") {
			err = fmt.Errorf("expected previous snapshot, got %+v", items)
		}
		searched <- err
	}()
	select {
	case err := <-searched:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("search blocked behind index refresh")
	}
}

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
