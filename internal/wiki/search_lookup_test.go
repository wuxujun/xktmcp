package wiki

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Independent reference for the original linear lookup, also used by benchmarks.
func linearSearchTermCount(terms []string, want string) int {
	count := 0
	for _, term := range terms {
		if term == want {
			count++
		}
	}
	return count
}

func TestSearchTermCountRepeatedBoundaries(t *testing.T) {
	for _, size := range []int{32, 33, 4096} {
		terms := make([]string, size)
		for i := range terms {
			terms[i] = "m"
		}
		for _, query := range []string{"a", "m", "z"} {
			want := 0
			if query == "m" {
				want = size
			}
			if got := countSearchTerm(terms, query); got != want {
				t.Fatalf("size %d query %q: got %d, want %d", size, query, got, want)
			}
		}
	}
}

func TestSearchTermCountPreservesMultiplicity(t *testing.T) {
	var long []string
	for i := 0; i < 100; i++ {
		long = append(long, fmt.Sprintf("term-%03d", i), "北京")
	}
	for _, terms := range [][]string{nil, {"a"}, {"z", "a", "a", "北京", "大学", "北京"}, long} {
		sort.Strings(terms)
		for _, query := range []string{"", "a", "b", "z", "北京", "大学", "missing"} {
			if got, want := countSearchTerm(terms, query), linearSearchTermCount(terms, query); got != want {
				t.Fatalf("count(%v,%q)=%d, want %d", terms, query, got, want)
			}
		}
	}
}

func TestPreparedGSETermLookupMatchesLinearReference(t *testing.T) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := 99; i >= 0; i-- {
		fmt.Fprintf(&body, "课程%06d 北京大学招生。", i)
	}
	doc := localDocument{content: body.String()}
	s := &LocalSearcher{tokenizer: tokenizer}
	s.prepareDocument(&doc)
	if len(doc.contentTerms) <= 32 {
		t.Fatal("fixture must exercise the long-field lookup")
	}
	for _, query := range append(append([]string{}, doc.contentTerms...), "missing") {
		if got, want := countSearchTerm(doc.contentTerms, query), linearSearchTermCount(doc.contentTerms, query); got != want {
			t.Fatalf("query %q count=%d, want %d", query, got, want)
		}
	}
}
