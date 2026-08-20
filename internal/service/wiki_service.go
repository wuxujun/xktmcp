package service

import (
	"context"
	"errors"
	"strings"

	"github.com/wuxujun/xktmcp/internal/client"
	"github.com/wuxujun/xktmcp/internal/model"
)

var (
	ErrInvalidPageID  = errors.New("page_id or title must not be empty")
	ErrInvalidTitle   = errors.New("title must not be empty")
	ErrInvalidContent = errors.New("content must not be empty")
)

type WikiService struct {
	backend WikiBackend
}

type WikiBackend interface {
	SearchWiki(ctx context.Context, userId, query, category string, topK int) ([]model.WikiSearchResult, error)
	GetPage(ctx context.Context, userId, pageID, title string) (*model.WikiPage, error)
	ListTree(ctx context.Context, userId, parentID string, depth int) ([]model.WikiNode, error)
	UpsertPage(ctx context.Context, userId, title, content, category, summary, mode string) (*model.WikiUpsertResult, error)
	GetBacklinks(ctx context.Context, userId, pageID string) ([]model.WikiBacklink, error)
}

// NewWikiService 默认使用 WikiAPI；传入 backend 可整体替换为本地实现。
// 使用可变参数保留现有调用方兼容性。
func NewWikiService(api *client.WikiAPI, backends ...WikiBackend) *WikiService {
	backend := WikiBackend(api)
	if len(backends) > 0 && backends[0] != nil {
		backend = backends[0]
	}
	return &WikiService{backend: backend}
}

func (s *WikiService) Search(ctx context.Context, userId, query, category string, topK int) ([]model.WikiSearchResult, error) {
	userId = strings.TrimSpace(userId)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	if topK <= 0 {
		topK = 5
	} else if topK > 20 {
		topK = 20
	}
	return s.backend.SearchWiki(ctx, userId, query, strings.TrimSpace(category), topK)
}

func (s *WikiService) GetPage(ctx context.Context, userId, pageID, title string) (*model.WikiPage, error) {
	userId = strings.TrimSpace(userId)
	pageID = strings.TrimSpace(pageID)
	title = strings.TrimSpace(title)
	if pageID == "" && title == "" {
		return nil, ErrInvalidPageID
	}
	return s.backend.GetPage(ctx, userId, pageID, title)
}

func (s *WikiService) ListTree(ctx context.Context, userId, parentID string, depth int) ([]model.WikiNode, error) {
	userId = strings.TrimSpace(userId)
	parentID = strings.TrimSpace(parentID)
	if depth <= 0 {
		depth = 3
	} else if depth > 10 {
		depth = 10
	}
	return s.backend.ListTree(ctx, userId, parentID, depth)
}

func (s *WikiService) UpsertPage(ctx context.Context, userId, title, content, category, summary, mode string) (*model.WikiUpsertResult, error) {
	userId = strings.TrimSpace(userId)
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, ErrInvalidTitle
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ErrInvalidContent
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "create"
	}
	return s.backend.UpsertPage(ctx, userId, title, content, strings.TrimSpace(category), strings.TrimSpace(summary), mode)
}

func (s *WikiService) GetBacklinks(ctx context.Context, userId, pageID string) ([]model.WikiBacklink, error) {
	userId = strings.TrimSpace(userId)
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return nil, ErrInvalidID
	}
	return s.backend.GetBacklinks(ctx, userId, pageID)
}
