package wiki

import (
	"context"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wuxujun/xktmcp/internal/model"
)

var (
	markdownLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	wikiLinkPattern     = regexp.MustCompile(`\[\[([^]|#]+)(?:#[^]|]+)?(?:\|[^]]+)?\]\]`)
)

// GetBacklinks 从本地索引读取标准 Markdown 链接及 [[wiki links]] 的反向链接。
func (s *LocalSearcher) GetBacklinks(ctx context.Context, _ string, pageID string) ([]model.WikiBacklink, error) {
	if err := s.refresh(ctx, false); err != nil {
		return nil, err
	}
	target, err := s.findDocument(pageID, "")
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	links := append([]model.WikiBacklink(nil), s.backlinks[target.result.PageID]...)
	s.mu.RUnlock()
	return links, nil
}

func buildBacklinks(ctx context.Context, documents []localDocument) (map[string][]model.WikiBacklink, error) {
	backlinks := make(map[string][]model.WikiBacklink)
	// Parse each source only once. Target matching preserves the original
	// source order, duplicate-title behavior, and first matching context.
	references := make([][]backlinkReference, len(documents))
	for i, source := range documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		references[i], err = parseBacklinkReferences(ctx, source.content)
		if err != nil {
			return nil, err
		}
	}
	pathTargets := make(map[string]int, len(documents))
	keyTargets := make(map[string][]int, len(documents)*3)
	for i, target := range documents {
		pathTargets[filepath.Clean(target.path)] = i
		for _, key := range backlinkTargetKeys(target) {
			keyTargets[key] = append(keyTargets[key], i)
		}
	}
	for i, source := range documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidateLines := make(map[int]string)
		for _, reference := range references[i] {
			var candidates []int
			if reference.wiki {
				candidates = wikiBacklinkCandidates(reference.destination, keyTargets, documents)
			} else {
				destination := strings.Trim(strings.TrimSpace(reference.destination), "<>")
				if destination == "" || strings.HasPrefix(destination, "#") {
					continue
				}
				if parsed, err := url.Parse(destination); err == nil && parsed.Scheme != "" {
					continue
				}
				if index := strings.IndexAny(destination, "#?"); index >= 0 {
					destination = destination[:index]
				}
				if decoded, err := url.PathUnescape(destination); err == nil {
					destination = decoded
				}
				if target, ok := pathTargets[filepath.Clean(filepath.Join(filepath.Dir(source.path), filepath.FromSlash(destination)))]; ok {
					candidates = []int{target}
				}
			}
			for _, targetIndex := range candidates {
				target := documents[targetIndex]
				if source.result.PageID != target.result.PageID {
					if _, exists := candidateLines[targetIndex]; !exists {
						candidateLines[targetIndex] = reference.line
					}
				}
			}
		}
		for targetIndex, line := range candidateLines {
			target := documents[targetIndex]
			backlinks[target.result.PageID] = append(backlinks[target.result.PageID], model.WikiBacklink{SourcePageID: source.result.PageID, SourceTitle: source.result.Title, Context: truncateContext(line, 240)})
		}
	}
	for key := range backlinks {
		sort.SliceStable(backlinks[key], func(i, j int) bool {
			return strings.ToLower(backlinks[key][i].SourceTitle) < strings.ToLower(backlinks[key][j].SourceTitle)
		})
	}
	return backlinks, nil
}

func backlinkTargetKeys(target localDocument) []string {
	return []string{"title:" + normalize(target.result.Title), "id:" + normalize(strings.TrimSuffix(target.result.PageID, ".md")), "stem:" + normalize(strings.TrimSuffix(filepath.Base(target.path), filepath.Ext(target.path)))}
}

func wikiBacklinkCandidates(destination string, keys map[string][]int, documents []localDocument) []int {
	destination = normalize(strings.TrimSuffix(strings.TrimSpace(destination), ".md"))
	destination = strings.Trim(destination, "/")
	seen := make(map[int]struct{})
	var result []int
	for _, key := range []string{"title:" + destination, "id:" + destination, "stem:" + destination} {
		for _, index := range keys[key] {
			if _, ok := seen[index]; !ok {
				seen[index] = struct{}{}
				result = append(result, index)
			}
		}
	}
	for i, target := range documents {
		pageID := normalize(strings.TrimSuffix(target.result.PageID, ".md"))
		if strings.HasSuffix(pageID, "/"+destination) {
			if _, ok := seen[i]; !ok {
				result = append(result, i)
			}
		}
	}
	return result
}

type backlinkReference struct {
	destination string
	line        string
	wiki        bool
}

func parseBacklinkReferences(ctx context.Context, content string) ([]backlinkReference, error) {
	var references []backlinkReference
	for line := range strings.SplitSeq(content, "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.Contains(line, "[") {
			continue
		}
		for _, match := range markdownLinkPattern.FindAllStringSubmatch(line, -1) {
			references = append(references, backlinkReference{destination: match[1], line: strings.TrimSpace(line)})
		}
		for _, match := range wikiLinkPattern.FindAllStringSubmatch(line, -1) {
			references = append(references, backlinkReference{destination: match[1], line: strings.TrimSpace(line), wiki: true})
		}
	}
	return references, nil
}

func truncateContext(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "…"
}
