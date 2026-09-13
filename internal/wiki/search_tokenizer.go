package wiki

import (
	"fmt"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

type searchTokenizer interface {
	Terms(string) []string
	ContentTerms(string) []string
	UsesTokenBoundaries() bool
}

type builtinSearchTokenizer struct{}

func (builtinSearchTokenizer) Terms(text string) []string {
	return queryTerms(normalize(text))
}

func (t builtinSearchTokenizer) ContentTerms(text string) []string { return t.Terms(text) }

func (builtinSearchTokenizer) UsesTokenBoundaries() bool { return false }

type gseSearchTokenizer struct {
	mu        sync.Mutex
	segmenter gse.Segmenter
}

type gseTemplateEntry struct {
	once      sync.Once
	segmenter gse.Segmenter
	err       error
}

var gseModelOnce sync.Once

// The map is fixed at initialization; entries load independently on demand.
var gseTemplates = map[string]*gseTemplateEntry{
	GSEDictionaryZH:  {},
	GSEDictionaryZHS: {},
}

func newSearchTokenizer(name string) (searchTokenizer, error) {
	return newSearchTokenizerWithDictionary(name, GSEDictionaryZH)
}

func newSearchTokenizerWithDictionary(name, dictionary string) (searchTokenizer, error) {
	switch name {
	case "", SearchTokenizerBuiltin:
		return builtinSearchTokenizer{}, nil
	case SearchTokenizerGSE:
		segmenter, err := sharedGSESegmenter(dictionary)
		if err != nil {
			return nil, fmt.Errorf("load gse search dictionary: %w", err)
		}
		return &gseSearchTokenizer{segmenter: segmenter}, nil
	default:
		return nil, fmt.Errorf("unsupported search tokenizer %q", name)
	}
}

func sharedGSESegmenter(dictionary string) (gse.Segmenter, error) {
	dictionary, err := normalizeGSEDictionary(dictionary)
	if err != nil {
		return gse.Segmenter{}, err
	}
	entry := gseTemplates[dictionary]
	entry.once.Do(func() {
		// GSE's default HMM model is global and LoadModel rewrites its maps.
		// Initialize it once before any dictionary can publish a segmenter.
		gseModelOnce.Do(func() { entry.segmenter.LoadModel() })
		entry.segmenter.NotLoadHMM = true
		entry.segmenter.SkipLog = true
		entry.err = entry.segmenter.LoadDictEmbed(dictionary)
	})
	if entry.err != nil {
		return gse.Segmenter{}, entry.err
	}
	// Segmenter is copied per searcher so each instance keeps its own lock;
	// the embedded dictionary is read-only after initialization and shared.
	return entry.segmenter, nil
}

func (t *gseSearchTokenizer) Terms(text string) []string {
	return t.terms(text, true)
}

// ContentTerms preserves occurrences for body term-frequency scoring.
func (t *gseSearchTokenizer) ContentTerms(text string) []string {
	return t.terms(text, false)
}

func (t *gseSearchTokenizer) terms(text string, unique bool) []string {
	text = normalize(text)
	if text == "" {
		return nil
	}

	t.mu.Lock()
	words := t.segmenter.CutSearch(text, true)
	t.mu.Unlock()

	terms := make([]string, 0, len(words)+4)
	var seen map[string]struct{}
	if unique {
		seen = make(map[string]struct{}, len(words)+4)
	}
	appendTerm := func(term string) {
		term = normalize(term)
		if term == "" || !isSearchTerm(term) {
			return
		}
		if unique {
			if _, ok := seen[term]; ok {
				return
			}
			seen[term] = struct{}{}
		}
		terms = append(terms, term)
	}
	for _, word := range words {
		appendTerm(word)
	}
	// Keep the existing lexical fallback only when segmentation yielded no
	// searchable terms. This avoids retaining an entire long document run as
	// one extra index term while still handling unsupported input safely.
	if len(terms) == 0 {
		for _, term := range queryTerms(text) {
			appendTerm(term)
		}
	}
	return terms
}

func (t *gseSearchTokenizer) UsesTokenBoundaries() bool { return true }

func isSearchTerm(term string) bool {
	for _, r := range term {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}
