package wiki

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/model"
)

// Measures the complete in-memory SearchWiki path with a fresh snapshot.
// Dictionary loading, tokenization at index time, and filesystem I/O are excluded.
func BenchmarkGSESearchSnapshot(b *testing.B) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{16, 1024} {
		var body strings.Builder
		for i := 0; i < size; i++ {
			fmt.Fprintf(&body, "课程%06d 北京大学招生。", i)
		}
		s := &LocalSearcher{tokenizer: tokenizer, nextRefresh: time.Now().Add(time.Hour)}
		doc := localDocument{result: model.WikiSearchResult{Title: "课程指南", Summary: "北京大学课程"}, content: body.String(), searchTitle: "课程指南", searchSummary: "北京大学课程", searchContent: body.String()}
		s.prepareDocument(&doc)
		for i := 0; i < 1000; i++ {
			copyDoc := doc
			copyDoc.result.PageID = fmt.Sprintf("page-%04d", i)
			s.documents = append(s.documents, copyDoc)
		}
		s.termIndex = buildTermIndex(s.documents)
		for _, query := range []string{"大学", "北京大学", "missing-term"} {
			b.Run(fmt.Sprintf("%d/%s", size, query), func(b *testing.B) {
				wantIDs := [...]string{"page-0000", "page-0001", "page-0002", "page-0003", "page-0004"}
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					results, err := s.SearchWiki(context.Background(), "", query, "", 5)
					if err != nil {
						b.Fatal(err)
					}
					if query == "missing-term" {
						if len(results) != 0 {
							b.Fatal(results)
						}
						continue
					}
					if len(results) != 5 || results[0].PageID != "page-0000" {
						b.Fatal(results)
					}
					// Hand-calculated scores: summary hit (10) + capped body frequency (10),
					// plus 6+8 phrase bonus for the three-term Chinese query.
					wantScore := float32(20)
					if query == "北京大学" {
						wantScore = 74
					}
					for j, result := range results {
						if result.Score != wantScore || result.PageID != wantIDs[j] {
							b.Fatalf("unexpected ranking/score: %v", results)
						}
					}
				}
			})
		}
	}
}
