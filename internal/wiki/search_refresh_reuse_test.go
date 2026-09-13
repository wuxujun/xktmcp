package wiki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Wraps the real tokenizer to measure whether refresh repeats expensive work.
type countingRefreshTokenizer struct {
	searchTokenizer
	calls int
}

func (t *countingRefreshTokenizer) Terms(text string) []string {
	t.calls++
	return t.searchTokenizer.Terms(text)
}

func (t *countingRefreshTokenizer) ContentTerms(text string) []string {
	t.calls++
	return t.searchTokenizer.ContentTerms(text)
}

func TestRefreshReusesUnchangedGSETerms(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wiki")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "page.md")
	write := func(title, summary, body, category string) {
		t.Helper()
		content := fmt.Sprintf("---\ntitle: %s\nsummary: %s\ncategory: %s\n---\n%s", title, summary, category, body)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("课程", "学习", "苹果", "old")
	s, err := NewLocalSearcher(LocalConfig{Root: root, Tokenizer: SearchTokenizerGSE})
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingRefreshTokenizer{searchTokenizer: s.tokenizer}
	s.tokenizer = counter
	before, err := s.SearchWiki(context.Background(), "", "苹果", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	counter.calls = 0
	if err := s.refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if counter.calls != 0 {
		t.Fatalf("unchanged refresh tokenized %d fields", counter.calls)
	}
	after, err := s.SearchWiki(context.Background(), "", "苹果", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("results changed: before=%v after=%v", before, after)
	}
	write("课程", "学习", "苹果", "new")
	counter.calls = 0
	if err := s.refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if counter.calls != 0 {
		t.Fatalf("metadata-only update tokenized %d fields", counter.calls)
	}
	items, err := s.SearchWiki(context.Background(), "", "苹果", "new", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("fresh metadata not used: %v, %v", items, err)
	}
	for _, field := range []string{"title", "summary", "body"} {
		t.Run(field, func(t *testing.T) {
			write("课程", "学习", "苹果", "new")
			if err := s.refresh(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			title, summary, body := "课程", "学习", "苹果"
			switch field {
			case "title":
				title = "香蕉"
			case "summary":
				summary = "香蕉"
			case "body":
				body = "香蕉"
			}
			write(title, summary, body, "new")
			// Equal-length content and restored mtime must not conceal an edit.
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			counter.calls = 0
			if err := s.refresh(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if counter.calls == 0 {
				t.Fatal("changed search fields reused stale terms")
			}
			items, err := s.SearchWiki(context.Background(), "", "香蕉", "", 5)
			if err != nil || len(items) != 1 {
				t.Fatalf("changed terms missing: %v, %v", items, err)
			}
		})
	}
}
