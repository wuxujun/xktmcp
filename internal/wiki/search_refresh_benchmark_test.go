package wiki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/wuxujun/xktmcp/internal/model"
)

// The reference isolates sorting cost while preserving body frequencies.
func prepareUnsortedBenchmarkDocument(tokenizer searchTokenizer, doc *localDocument) {
	doc.tokenized = true
	doc.titleTerms = tokenizer.Terms(doc.result.Title)
	doc.summaryTerms = tokenizer.Terms(doc.result.Summary)
	doc.contentTerms = tokenizer.ContentTerms(doc.content)
}

func benchmarkRefreshDocument(lines int) localDocument {
	var body strings.Builder
	// Descending identifiers avoid giving sorting an already ordered fixture.
	for i := lines; i > 0; i-- {
		fmt.Fprintf(&body, "课程%06d 北京大学招生与项目式学习。\n", i)
	}
	return localDocument{result: model.WikiSearchResult{Title: "课程指南", Summary: "北京大学课程"}, content: body.String()}
}

func BenchmarkGSEPrepareDocument(b *testing.B) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		b.Fatal(err)
	}
	s := &LocalSearcher{tokenizer: tokenizer}
	for _, lines := range []int{16, 1024} {
		fixture := benchmarkRefreshDocument(lines)
		before, after := fixture, fixture
		prepareUnsortedBenchmarkDocument(tokenizer, &before)
		s.prepareDocument(&after)
		sort.Strings(before.titleTerms)
		sort.Strings(before.summaryTerms)
		sort.Strings(before.contentTerms)
		if !reflect.DeepEqual(before, after) {
			b.Fatal("sorting changed indexed document contents")
		}
		for _, mode := range []string{"unsorted", "sorted"} {
			b.Run(fmt.Sprintf("%d/%s", lines, mode), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					doc := fixture
					if mode == "unsorted" {
						prepareUnsortedBenchmarkDocument(tokenizer, &doc)
					} else {
						s.prepareDocument(&doc)
					}
					if len(doc.contentTerms) == 0 {
						b.Fatal("empty index")
					}
				}
			})
		}
	}
}

// Measures real forced refresh of unchanged files, including Markdown reads,
// term reuse, inverted index, backlinks and publication. Files and the shared
// dictionary are warm; allocations are cumulative bytes, not retained heap.
func BenchmarkGSEForcedRefresh(b *testing.B) {
	for _, lines := range []int{16, 1024} {
		b.Run(fmt.Sprintf("20docs/%dlines", lines), func(b *testing.B) {
			root := b.TempDir()
			dir := filepath.Join(root, "wiki")
			if err := os.Mkdir(dir, 0o700); err != nil {
				b.Fatal(err)
			}
			fixture := benchmarkRefreshDocument(lines)
			for i := 0; i < 20; i++ {
				article := fmt.Sprintf("---\ntitle: 课程 %d\nsummary: 北京大学课程\n---\n%s\n[相关课程](page-%02d.md)\n", i, fixture.content, (i+1)%20)
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("page-%02d.md", i)), []byte(article), 0o600); err != nil {
					b.Fatal(err)
				}
			}
			s, err := NewLocalSearcher(LocalConfig{Root: root, Tokenizer: SearchTokenizerGSE})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.refresh(context.Background(), true); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if s.DocumentCount() != 20 {
				b.Fatalf("document count = %d", s.DocumentCount())
			}
		})
	}
}
