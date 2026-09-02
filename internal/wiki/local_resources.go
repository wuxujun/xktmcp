package wiki

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/pii"
)

const (
	pageResourcePrefix     = "wiki://page/"
	maxResourcePageIDBytes = 2048
)

var ErrResourceNotFound = errors.New("wiki resource not found")

var ErrDuplicateResourcePageID = errors.New("duplicate wiki resource page_id")

type ResourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}

type ResourceCatalog struct {
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated"`
	Items     []ResourceDescriptor `json:"items"`
}

func PageResourceURI(pageID string) (string, error) {
	if pageID == "" || strings.TrimSpace(pageID) != pageID || len(pageID) > maxResourcePageIDBytes || !utf8.ValidString(pageID) {
		return "", ErrResourceNotFound
	}
	return pageResourcePrefix + base64.RawURLEncoding.EncodeToString([]byte(pageID)), nil
}

func ParsePageResourceURI(raw string) (string, error) {
	if !strings.HasPrefix(raw, pageResourcePrefix) {
		return "", ErrResourceNotFound
	}
	key := strings.TrimPrefix(raw, pageResourcePrefix)
	if key == "" || strings.ContainsAny(key, "/?#%=") {
		return "", ErrResourceNotFound
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) == 0 || len(decoded) > maxResourcePageIDBytes || !utf8.Valid(decoded) {
		return "", ErrResourceNotFound
	}
	pageID := string(decoded)
	canonical, err := PageResourceURI(pageID)
	if err != nil || canonical != raw {
		return "", ErrResourceNotFound
	}
	return pageID, nil
}

type resourceMetadata struct {
	pageID string
	result model.WikiSearchResult
}

type resourceEntry struct {
	pageID     string
	descriptor ResourceDescriptor
}

func (s *LocalSearcher) ListResources(ctx context.Context, limit int) (ResourceCatalog, error) {
	catalog := ResourceCatalog{Items: make([]ResourceDescriptor, 0)}
	if limit < 1 {
		return catalog, fmt.Errorf("wiki resource limit must be at least 1")
	}
	if err := s.refresh(ctx, false); err != nil {
		return catalog, err
	}

	s.mu.RLock()
	metadata := make([]resourceMetadata, 0, len(s.documents))
	seen := make(map[string]struct{}, len(s.documents))
	for _, doc := range s.documents {
		if err := ctx.Err(); err != nil {
			s.mu.RUnlock()
			return catalog, err
		}
		pageID := doc.result.PageID
		if _, ok := seen[pageID]; ok {
			s.mu.RUnlock()
			return catalog, ErrDuplicateResourcePageID
		}
		seen[pageID] = struct{}{}
		metadata = append(metadata, resourceMetadata{pageID: pageID, result: doc.result})
	}
	s.mu.RUnlock()

	entries := make([]resourceEntry, 0, len(metadata))
	for _, doc := range metadata {
		uri, err := PageResourceURI(doc.pageID)
		if err != nil {
			return catalog, err
		}
		description := fmt.Sprintf("分类: %s | 摘要: %s", doc.result.Category, doc.result.Summary)
		entries = append(entries, resourceEntry{
			pageID: doc.pageID,
			descriptor: ResourceDescriptor{
				URI:         uri,
				Name:        pii.Redact(doc.result.Title),
				Description: pii.Redact(description),
				MIMEType:    "text/markdown",
			},
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].pageID < entries[j].pageID
	})
	catalog.Total = len(entries)
	if len(entries) > limit {
		entries = entries[:limit]
		catalog.Truncated = true
	}
	for _, entry := range entries {
		catalog.Items = append(catalog.Items, entry.descriptor)
	}
	return catalog, nil
}

func (s *LocalSearcher) ReadPageResource(ctx context.Context, uri string) (string, error) {
	pageID, err := ParsePageResourceURI(uri)
	if err != nil {
		return "", err
	}
	page, err := s.GetPage(ctx, "", pageID, "")
	if errors.Is(err, errLocalPageNotFound) {
		return "", ErrResourceNotFound
	}
	if err != nil {
		return "", err
	}
	return pii.Redact(page.Content), nil
}
