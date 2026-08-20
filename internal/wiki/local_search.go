package wiki

import (
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

	"github.com/wuxujun/xktmcp/internal/model"
)

type localDocument struct {
	result      model.WikiSearchResult
	content     string
	path        string
	frontmatter map[string]string
}

// LocalSearcher 对 llm-wiki 编译后的 Markdown 文章建立轻量内存索引。
// 索引按需刷新，不修改 Wiki 源文件或派生 _index.md。
type LocalSearcher struct {
	cfg         LocalConfig
	mu          sync.RWMutex
	writeMu     sync.Mutex
	documents   []localDocument
	nextRefresh time.Time
}

func NewLocalSearcher(cfg LocalConfig) (*LocalSearcher, error) {
	if err := normalizeLocalConfig(&cfg, "."); err != nil {
		return nil, err
	}
	s := &LocalSearcher{cfg: cfg}
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
	if topK <= 0 {
		topK = 5
	}

	s.mu.RLock()
	matches := make([]model.WikiSearchResult, 0, topK)
	for _, doc := range s.documents {
		if err := ctx.Err(); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		if category != "" && normalize(doc.result.Category) != category {
			continue
		}
		score := scoreDocument(doc, query)
		if score <= 0 {
			continue
		}
		result := doc.result
		result.Score = float32(score)
		matches = append(matches, result)
	}
	s.mu.RUnlock()

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].UpdatedAt != matches[j].UpdatedAt {
			return matches[i].UpdatedAt > matches[j].UpdatedAt
		}
		return matches[i].Title < matches[j].Title
	})
	if len(matches) > topK {
		matches = matches[:topK]
	}
	return matches, nil
}

func (s *LocalSearcher) refresh(ctx context.Context, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && time.Now().Before(s.nextRefresh) {
		return nil
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
			seen[path] = struct{}{}
			documents = append(documents, doc)
			return nil
		})
		if err != nil {
			return fmt.Errorf("index wiki content directory %q: %w", dir, err)
		}
	}

	s.documents = documents
	s.nextRefresh = time.Now().Add(s.cfg.RefreshInterval())
	return nil
}

func scoreDocument(doc localDocument, query string) int {
	title := normalize(doc.result.Title)
	summary := normalize(doc.result.Summary)
	content := normalize(doc.content)
	score := 0
	if title == query {
		score += 100
	} else if strings.Contains(title, query) {
		score += 40
	}
	for _, term := range queryTerms(query) {
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
