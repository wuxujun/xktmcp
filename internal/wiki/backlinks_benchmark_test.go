package wiki

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkWikiRefreshStages(b *testing.B) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		b.Fatal(err)
	}
	s := &LocalSearcher{tokenizer: tokenizer}
	documents := make([]localDocument, 20)
	for i := range documents {
		doc := benchmarkRefreshDocument(1024)
		doc.path = fmt.Sprintf("/wiki/page-%02d.md", i)
		doc.result.PageID = fmt.Sprintf("page-%02d", i)
		doc.content += fmt.Sprintf("\n[相关课程](page-%02d.md)\n", (i+1)%20)
		s.prepareDocument(&doc)
		documents[i] = doc
	}
	b.Run("backlinks", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			links, err := buildBacklinks(context.Background(), documents)
			if err != nil || len(links) != 20 {
				b.Fatalf("links=%d err=%v", len(links), err)
			}
		}
	})
	b.Run("termIndex", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(buildTermIndex(documents)) == 0 {
				b.Fatal("empty index")
			}
		}
	})
}
