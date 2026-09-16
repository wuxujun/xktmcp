package wiki

import (
	"container/heap"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wuxujun/xktmcp/internal/metrics"
	"github.com/wuxujun/xktmcp/internal/model"
)

type localDocument struct {
	result        model.WikiSearchResult
	content       string
	searchTitle   string
	searchSummary string
	searchContent string
	tokenized     bool
	titleTerms    []string
	summaryTerms  []string
	contentTerms  []string
	path          string
	frontmatter   map[string]string
}

type wikiMatch struct {
	result model.WikiSearchResult
	order  int
}

type wikiMatchHeap []wikiMatch

func (h wikiMatchHeap) Len() int { return len(h) }

func (h wikiMatchHeap) Less(i, j int) bool {
	return wikiMatchBetter(h[j], h[i])
}

func (h wikiMatchHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *wikiMatchHeap) Push(value any) { *h = append(*h, value.(wikiMatch)) }

func (h *wikiMatchHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	*h = old[:n-1]
	return value
}

// LocalSearcher 对 llm-wiki 编译后的 Markdown 文章建立轻量内存索引。
// 索引按需刷新，不修改 Wiki 源文件或派生 _index.md。
type LocalSearcher struct {
	cfg         LocalConfig
	mu          sync.RWMutex
	writeMu     sync.Mutex
	refreshMu   sync.Mutex
	tokenizer   searchTokenizer
	termIndex   map[string][]int
	documents   []localDocument
	backlinks   map[string][]model.WikiBacklink
	nextRefresh time.Time
}

func NewLocalSearcher(cfg LocalConfig) (*LocalSearcher, error) {
	if err := normalizeLocalConfig(&cfg, "."); err != nil {
		return nil, err
	}
	tokenizer, err := newSearchTokenizerWithDictionary(cfg.Tokenizer, cfg.GSEDictionary)
	if err != nil {
		return nil, err
	}
	s := &LocalSearcher{cfg: cfg, tokenizer: tokenizer}
	if err := s.refresh(context.Background(), true); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *LocalSearcher) DocumentCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.documents)
}

func (s *LocalSearcher) SearchWiki(ctx context.Context, _ string, query, category string, topK int) ([]model.WikiSearchResult, error) {
	if err := s.refresh(ctx, false); err != nil {
		return nil, err
	}
	query = normalize(query)
	category = normalize(category)
	if query == "" {
		return nil, nil
	}
	terms := s.tokenizer.Terms(query)
	if topK <= 0 {
		topK = 5
	}

	s.mu.RLock()
	matches := make([]wikiMatch, 0, topK)
	order := 0
	appendMatch := func(index int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		doc := s.documents[index]
		if category != "" && normalize(doc.result.Category) != category {
			return nil
		}
		score := scoreDocument(doc, query, terms)
		if score <= 0 {
			return nil
		}
		result := doc.result
		result.Score = float32(score)
		matches = append(matches, wikiMatch{result: result, order: order})
		order++
		return nil
	}
	candidates := termIndexCandidates(s.termIndex, terms)
	if candidates == nil {
		for index := range s.documents {
			if err := appendMatch(index); err != nil {
				s.mu.RUnlock()
				return nil, err
			}
		}
	} else {
		// Phrase scores can match even when segmentation produced no shared
		// term. Merge these matches in document order to preserve stable ties.
		next := 0
		for index, doc := range s.documents {
			if err := ctx.Err(); err != nil {
				s.mu.RUnlock()
				return nil, err
			}
			candidate := next < len(candidates) && candidates[next] == index
			if candidate {
				next++
			}
			phrase := !candidate && (strings.Contains(doc.searchTitle, query) ||
				(doc.tokenized && len(terms) > 1 &&
					(strings.Contains(doc.searchSummary, query) || strings.Contains(doc.searchContent, query))))
			if candidate || phrase {
				if err := appendMatch(index); err != nil {
					s.mu.RUnlock()
					return nil, err
				}
			}
		}
	}
	s.mu.RUnlock()

	selected := selectTopK(matches, topK)
	results := make([]model.WikiSearchResult, len(selected))
	for i, match := range selected {
		results[i] = match.result
	}
	return results, nil
}

func (s *LocalSearcher) refresh(ctx context.Context, force bool) (err error) {
	s.mu.RLock()
	fresh := time.Now().Before(s.nextRefresh)
	s.mu.RUnlock()
	if !force && fresh {
		return nil
	}
	if force {
		s.refreshMu.Lock()
	} else if !s.refreshMu.TryLock() {
		// Another request is rebuilding; readers can use the published snapshot.
		return nil
	}
	defer s.refreshMu.Unlock()
	started := time.Now()
	defer func() {
		count := 0
		if err == nil {
			s.mu.RLock()
			count = len(s.documents)
			s.mu.RUnlock()
		}
		metrics.ObserveWikiIndexRefresh("local", count, time.Now(), time.Since(started), err == nil)
	}()
	s.mu.RLock()
	fresh = time.Now().Before(s.nextRefresh)
	previous := s.documents
	s.mu.RUnlock()
	if !force && fresh {
		return nil
	}
	// Published documents and term slices are immutable. Only keep references
	// for this refresh; removed documents disappear with the next snapshot.
	var reusable map[string]*localDocument
	if s.tokenizer != nil && s.tokenizer.UsesTokenBoundaries() {
		reusable = make(map[string]*localDocument, len(previous))
		for i := range previous {
			reusable[previous[i].path] = &previous[i]
		}
	}

	documents := make([]localDocument, 0)
	seen := make(map[string]struct{})
	for _, contentDir := range s.cfg.ContentDirs {
		dir := filepath.Join(s.cfg.Root, contentDir)
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("stat wiki content directory %q: %w", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("wiki content path %q is not a directory", dir)
		}
		err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				if path != dir && strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || entry.Name() == "_index.md" || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				return nil
			}
			if _, ok := seen[path]; ok {
				return nil
			}
			fileInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if fileInfo.Size() > s.cfg.MaxFileSizeBytes {
				return nil
			}
			doc, err := loadDocument(s.cfg.Root, dir, path, fileInfo)
			if err != nil {
				return err
			}
			old := reusable[path]
			if old != nil && old.tokenized && old.result.Title == doc.result.Title &&
				old.result.Summary == doc.result.Summary && old.content == doc.content {
				doc.tokenized = true
				doc.titleTerms = old.titleTerms
				doc.summaryTerms = old.summaryTerms
				doc.contentTerms = old.contentTerms
			} else {
				s.prepareDocument(&doc)
			}
			seen[path] = struct{}{}
			documents = append(documents, doc)
			return nil
		})
		if err != nil {
			return fmt.Errorf("index wiki content directory %q: %w", dir, err)
		}
	}

	backlinks, err := buildBacklinks(ctx, documents)
	if err != nil {
		return err
	}
	termIndex := buildTermIndex(documents)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents = documents
	s.backlinks = backlinks
	s.termIndex = termIndex
	s.nextRefresh = time.Now().Add(s.cfg.RefreshInterval())
	return nil
}

func (s *LocalSearcher) prepareDocument(doc *localDocument) {
	if s.tokenizer == nil || !s.tokenizer.UsesTokenBoundaries() {
		return
	}
	doc.tokenized = true
	doc.titleTerms = s.tokenizer.Terms(doc.result.Title)
	doc.summaryTerms = s.tokenizer.Terms(doc.result.Summary)
	doc.contentTerms = s.tokenizer.ContentTerms(doc.content)
	// Published GSE fields are sorted once; scoring can then use binary lookup.
	sort.Strings(doc.titleTerms)
	sort.Strings(doc.summaryTerms)
	sort.Strings(doc.contentTerms)
}

func buildTermIndex(documents []localDocument) map[string][]int {
	var index map[string][]int
	for docIndex, doc := range documents {
		if !doc.tokenized {
			continue
		}
		seen := make(map[string]struct{}, len(doc.titleTerms)+len(doc.summaryTerms)+len(doc.contentTerms))
		addTerms := func(terms []string) {
			for _, term := range terms {
				if term == "" {
					continue
				}
				if _, ok := seen[term]; ok {
					continue
				}
				seen[term] = struct{}{}
				if index == nil {
					index = make(map[string][]int)
				}
				index[term] = append(index[term], docIndex)
			}
		}
		addTerms(doc.titleTerms)
		addTerms(doc.summaryTerms)
		addTerms(doc.contentTerms)
	}
	return index
}

func termIndexCandidates(index map[string][]int, terms []string) []int {
	if len(index) == 0 || len(terms) == 0 {
		return nil
	}
	seen := make(map[int]struct{})
	for _, term := range terms {
		for _, docIndex := range index[term] {
			seen[docIndex] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	candidates := make([]int, 0, len(seen))
	for docIndex := range seen {
		candidates = append(candidates, docIndex)
	}
	sort.Ints(candidates)
	return candidates
}

func wikiMatchBetter(a, b wikiMatch) bool {
	if a.result.Score != b.result.Score {
		return a.result.Score > b.result.Score
	}
	if a.result.UpdatedAt != b.result.UpdatedAt {
		return a.result.UpdatedAt > b.result.UpdatedAt
	}
	if a.result.Title != b.result.Title {
		return a.result.Title < b.result.Title
	}
	return a.order < b.order
}

func selectTopK(matches []wikiMatch, topK int) []wikiMatch {
	if topK <= 0 || len(matches) == 0 {
		return matches
	}
	if len(matches) <= topK {
		sort.SliceStable(matches, func(i, j int) bool { return wikiMatchBetter(matches[i], matches[j]) })
		return matches
	}

	candidates := make(wikiMatchHeap, 0, topK)
	for _, match := range matches {
		if len(candidates) < topK {
			heap.Push(&candidates, match)
			continue
		}
		if wikiMatchBetter(match, candidates[0]) {
			candidates[0] = match
			heap.Fix(&candidates, 0)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return wikiMatchBetter(candidates[i], candidates[j]) })
	return candidates
}

const (
	gseSummaryPhraseBonus = 6
	gseContentPhraseBonus = 8
)

func scoreDocument(doc localDocument, query string, terms []string) int {
	title := doc.searchTitle
	summary := doc.searchSummary
	content := doc.searchContent
	score := 0
	if title == query {
		score += 100
	} else if strings.Contains(title, query) {
		score += 40
	}
	if doc.tokenized && len(terms) > 1 {
		if strings.Contains(summary, query) {
			score += gseSummaryPhraseBonus
		}
		if strings.Contains(content, query) {
			score += gseContentPhraseBonus
		}
	}
	for _, term := range terms {
		if doc.tokenized {
			if containsSearchTerm(doc.titleTerms, term) {
				score += 20
			}
			if containsSearchTerm(doc.summaryTerms, term) {
				score += 10
			}
			if count := countSearchTerm(doc.contentTerms, term); count > 0 {
				score += min(count, 10)
			}
			continue
		}
		if strings.Contains(title, term) {
			score += 20
		}
		if strings.Contains(summary, term) {
			score += 10
		}
		if count := strings.Count(content, term); count > 0 {
			score += min(count, 10)
		}
	}
	return score
}

func containsSearchTerm(terms []string, want string) bool {
	return countSearchTerm(terms, want) > 0
}

func countSearchTerm(terms []string, want string) int {
	// For small fields a linear scan avoids binary-search comparison overhead.
	if len(terms) <= 32 {
		count := 0
		for _, term := range terms {
			if term == want {
				count++
			}
		}
		return count
	}
	// terms must be sorted, as established by prepareDocument.
	start := sort.SearchStrings(terms, want)
	if start == len(terms) || terms[start] != want {
		return 0
	}
	// Most title/summary terms are unique; avoid another search for those.
	next := start + 1
	if next == len(terms) || terms[next] != want {
		return 1
	}
	// Body terms retain frequency. Find the upper bound without scanning
	// every occurrence of a common word in a long document.
	return 1 + sort.Search(len(terms)-next, func(i int) bool {
		return terms[next+i] > want
	})
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func queryTerms(query string) []string {
	var terms []string
	var current []rune
	currentHan := false
	flush := func() {
		if len(current) > 0 {
			terms = append(terms, string(current))
			current = current[:0]
		}
	}
	for _, r := range query {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			flush()
			continue
		}
		isHan := unicode.Is(unicode.Han, r)
		if len(current) > 0 && isHan != currentHan {
			flush()
		}
		currentHan = isHan
		current = append(current, r)
	}
	flush()
	if len(terms) == 0 {
		return []string{query}
	}
	return terms
}
