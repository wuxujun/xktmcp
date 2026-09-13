package wiki

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func BenchmarkRepeatedTermCount(b *testing.B) {
	for _, size := range []int{32, 1024, 16384} {
		terms := make([]string, size)
		for i := range terms {
			terms[i] = "大学"
		}
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := countSearchTerm(terms, "大学"); got != size {
					b.Fatalf("count = %d, want %d", got, size)
				}
			}
		})
	}
}

// Experimental lookup only: sorting and dictionary initialization are excluded.
// End-to-end search also includes tokenization, phrase matching and ranking.
func BenchmarkGSETermLookup(b *testing.B) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{32, 1024} {
		var body strings.Builder
		for i := 0; i < size; i++ {
			fmt.Fprintf(&body, "课程%06d 北京大学招生。", i)
		}
		terms := tokenizer.Terms(body.String())
		sort.Strings(terms)
		for label, query := range map[string]string{"hit": terms[len(terms)/2], "miss": "不存在的检索词", "common": "大学"} {
			binaryCount := func(items []string, want string) int {
				start := sort.SearchStrings(items, want)
				end := sort.Search(len(items), func(i int) bool { return items[i] > want })
				return end - start
			}
			want := linearSearchTermCount(terms, query)
			if got := binaryCount(terms, query); got != want {
				b.Fatalf("lookup mismatch: %d != %d", got, want)
			}
			for name, lookup := range map[string]func([]string, string) int{"linear": linearSearchTermCount, "binary": binaryCount} {
				b.Run(fmt.Sprintf("%d/%s/%s", size, label, name), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if got := lookup(terms, query); got != want {
							b.Fatal(got)
						}
					}
				})
			}
		}
	}
}
