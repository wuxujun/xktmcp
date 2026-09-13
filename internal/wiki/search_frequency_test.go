package wiki

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wuxujun/xktmcp/internal/model"
)

func TestGSESearchPreservesBodyFrequency(t *testing.T) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		t.Fatal(err)
	}
	s := &LocalSearcher{tokenizer: tokenizer, nextRefresh: time.Now().Add(time.Hour)}
	for _, fixture := range []struct {
		id    string
		count int
	}{{"once", 1}, {"three", 3}, {"capped", 40}} {
		body := strings.Repeat("苹果 ", fixture.count)
		doc := localDocument{result: model.WikiSearchResult{PageID: fixture.id}, content: body, searchContent: body}
		s.prepareDocument(&doc)
		s.documents = append(s.documents, doc)
	}
	s.termIndex = buildTermIndex(s.documents)
	for _, query := range []string{"苹果", "苹果 苹果"} {
		results, err := s.SearchWiki(context.Background(), "", query, "", 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 3 {
			t.Fatalf("query %q: results = %v", query, results)
		}
		for i, want := range []struct {
			id    string
			score float32
		}{{"capped", 10}, {"three", 3}, {"once", 1}} {
			if results[i].PageID != want.id || results[i].Score != want.score {
				t.Errorf("query %q result %d = %v, want %s score %v", query, i, results[i], want.id, want.score)
			}
		}
	}
}
