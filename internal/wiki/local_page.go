package wiki

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wuxujun/xktmcp/internal/model"
)

var errLocalPageNotFound = errors.New("local wiki page not found")

// GetPage 从本地索引按 page_id 或精确标题读取正文与元信息。
func (s *LocalSearcher) GetPage(ctx context.Context, _ string, pageID, title string) (*model.WikiPage, error) {
	if err := s.refresh(ctx, false); err != nil {
		return nil, err
	}
	doc, err := s.findDocument(pageID, title)
	if err != nil {
		return nil, err
	}
	version, _ := strconv.Atoi(doc.frontmatter["version"])
	return &model.WikiPage{
		PageID:    doc.result.PageID,
		Title:     doc.result.Title,
		Content:   doc.content,
		Category:  doc.result.Category,
		Summary:   doc.result.Summary,
		Version:   version,
		Author:    doc.frontmatter["author"],
		CreatedAt: parseWikiTime(doc.frontmatter["created"]),
		UpdatedAt: parseWikiTime(doc.result.UpdatedAt),
	}, nil
}

func (s *LocalSearcher) findDocument(pageID, title string) (localDocument, error) {
	pageID = strings.TrimSpace(pageID)
	title = strings.TrimSpace(title)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matches []localDocument
	for _, doc := range s.documents {
		matched := pageID != "" && doc.result.PageID == pageID
		if pageID == "" && title != "" {
			matched = strings.EqualFold(doc.result.Title, title)
		}
		if matched {
			matches = append(matches, doc)
		}
	}
	if len(matches) == 0 {
		return localDocument{}, fmt.Errorf("%w: page_id=%q title=%q", errLocalPageNotFound, pageID, title)
	}
	if len(matches) > 1 {
		return localDocument{}, fmt.Errorf("local wiki title %q is ambiguous (%d matches); use page_id", title, len(matches))
	}
	return matches[0], nil
}

func parseWikiTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
