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

// GetBacklinks 扫描标准 Markdown 链接及 [[wiki links]]，返回引用目标页面的来源页面。
func (s *LocalSearcher) GetBacklinks(ctx context.Context, _ string, pageID string) ([]model.WikiBacklink, error) {
	if err := s.refresh(ctx, false); err != nil {
		return nil, err
	}
	target, err := s.findDocument(pageID, "")
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	documents := append([]localDocument(nil), s.documents...)
	s.mu.RUnlock()
	links := make([]model.WikiBacklink, 0)
	for _, source := range documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source.result.PageID == target.result.PageID {
			continue
		}
		if line, ok := backlinkContext(source, target); ok {
			links = append(links, model.WikiBacklink{
				SourcePageID: source.result.PageID,
				SourceTitle:  source.result.Title,
				Context:      truncateContext(line, 240),
			})
		}
	}
	sort.SliceStable(links, func(i, j int) bool {
		return strings.ToLower(links[i].SourceTitle) < strings.ToLower(links[j].SourceTitle)
	})
	return links, nil
}

func backlinkContext(source, target localDocument) (string, bool) {
	for _, line := range strings.Split(source.content, "\n") {
		for _, match := range markdownLinkPattern.FindAllStringSubmatch(line, -1) {
			if markdownLinkTargets(source.path, match[1], target.path) {
				return strings.TrimSpace(line), true
			}
		}
		for _, match := range wikiLinkPattern.FindAllStringSubmatch(line, -1) {
			if wikiLinkTargets(match[1], target) {
				return strings.TrimSpace(line), true
			}
		}
	}
	return "", false
}

func markdownLinkTargets(sourcePath, destination, targetPath string) bool {
	destination = strings.Trim(strings.TrimSpace(destination), "<>")
	if destination == "" || strings.HasPrefix(destination, "#") {
		return false
	}
	if parsed, err := url.Parse(destination); err == nil && parsed.Scheme != "" {
		return false
	}
	if index := strings.IndexAny(destination, "#?"); index >= 0 {
		destination = destination[:index]
	}
	if decoded, err := url.PathUnescape(destination); err == nil {
		destination = decoded
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(destination)))
	return resolved == filepath.Clean(targetPath)
}

func wikiLinkTargets(destination string, target localDocument) bool {
	destination = normalize(strings.TrimSuffix(strings.TrimSpace(destination), ".md"))
	destination = strings.Trim(destination, "/")
	pageID := normalize(strings.TrimSuffix(target.result.PageID, ".md"))
	stem := normalize(strings.TrimSuffix(filepath.Base(target.path), filepath.Ext(target.path)))
	return destination == normalize(target.result.Title) || destination == pageID || destination == stem || strings.HasSuffix(pageID, "/"+destination)
}

func truncateContext(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "…"
}
