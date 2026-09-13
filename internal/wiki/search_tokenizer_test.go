package wiki

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSharedGSEDictionaryConcurrentSegmentation(t *testing.T) {
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
			if err != nil {
				t.Error(err)
				return
			}
			for j := 0; j < 30; j++ {
				if got := tokenizer.Terms("北京大学"); !reflect.DeepEqual(got, []string{"北京", "大学", "北京大学"}) {
					t.Errorf("concurrent segmentation = %v", got)
					return
				}
			}
		}()
	}
	close(start)
	workers.Wait()
}

func TestGSESearchTokenizerReturnsDictionaryTerms(t *testing.T) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		t.Fatal(err)
	}
	terms := tokenizer.Terms("北京大学")
	for _, want := range []string{"北京", "大学"} {
		found := false
		for _, term := range terms {
			if term == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("terms = %v, want term %q", terms, want)
		}
	}
}

func TestGSESearchTokenizerDoesNotKeepWholeLongRunAsFallback(t *testing.T) {
	tokenizer, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		t.Fatal(err)
	}
	longRun := strings.Repeat("北京", 512)
	for _, term := range tokenizer.Terms(longRun) {
		if term == longRun {
			t.Fatalf("terms retained the entire %d-rune document run", len([]rune(longRun)))
		}
	}
}

func TestNewSearchTokenizerSharesGSEDictionary(t *testing.T) {
	first, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSearchTokenizer(SearchTokenizerGSE)
	if err != nil {
		t.Fatal(err)
	}
	firstGSE, ok := first.(*gseSearchTokenizer)
	if !ok {
		t.Fatalf("first tokenizer type = %T, want *gseSearchTokenizer", first)
	}
	secondGSE, ok := second.(*gseSearchTokenizer)
	if !ok {
		t.Fatalf("second tokenizer type = %T, want *gseSearchTokenizer", second)
	}
	if firstGSE == secondGSE {
		t.Fatal("newSearchTokenizer returned the same mutable tokenizer instance")
	}
	if firstGSE.segmenter.Dictionary() == nil || secondGSE.segmenter.Dictionary() == nil {
		t.Fatal("GSE tokenizer has no loaded dictionary")
	}
	if firstGSE.segmenter.Dictionary() != secondGSE.segmenter.Dictionary() {
		t.Fatal("GSE tokenizers do not share the loaded dictionary")
	}
}
