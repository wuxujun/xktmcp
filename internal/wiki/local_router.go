package wiki

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wuxujun/xktmcp/internal/model"
)

var ErrUserWikiNotConfigured = errors.New("local wiki is not configured for this user")

// LocalRouter 按调用者 userId 将所有本地 Wiki 操作路由到相互隔离的目录。
// userId 只作为显式配置映射的 key，绝不参与路径拼接。
type LocalRouter struct {
	defaultSearcher    *LocalSearcher
	users              map[string]*LocalSearcher
	requireUserMapping bool
}

func NewLocalRouter(cfg LocalConfig) (*LocalRouter, error) {
	defaultCfg := cfg
	defaultCfg.Users = nil
	defaultCfg.RequireUserMapping = false
	defaultSearcher, err := NewLocalSearcher(defaultCfg)
	if err != nil {
		return nil, err
	}
	router := &LocalRouter{
		defaultSearcher:    defaultSearcher,
		users:              make(map[string]*LocalSearcher, len(cfg.Users)),
		requireUserMapping: cfg.RequireUserMapping,
	}
	for userID, userCfg := range cfg.Users {
		searcher, err := NewLocalSearcher(userCfg)
		if err != nil {
			return nil, fmt.Errorf("initialize local wiki for user %q: %w", userID, err)
		}
		router.users[userID] = searcher
	}
	return router, nil
}

func (r *LocalRouter) UserCount() int { return len(r.users) }

func (r *LocalRouter) DocumentCount() int {
	total := r.defaultSearcher.DocumentCount()
	for _, searcher := range r.users {
		total += searcher.DocumentCount()
	}
	return total
}

func (r *LocalRouter) searcher(userID string) (*LocalSearcher, error) {
	userID = strings.TrimSpace(userID)
	if searcher, ok := r.users[userID]; ok {
		return searcher, nil
	}
	if r.requireUserMapping {
		return nil, ErrUserWikiNotConfigured
	}
	return r.defaultSearcher, nil
}

func (r *LocalRouter) SearchWiki(ctx context.Context, userID, query, category string, topK int) ([]model.WikiSearchResult, error) {
	searcher, err := r.searcher(userID)
	if err != nil {
		return nil, err
	}
	return searcher.SearchWiki(ctx, userID, query, category, topK)
}

func (r *LocalRouter) GetPage(ctx context.Context, userID, pageID, title string) (*model.WikiPage, error) {
	searcher, err := r.searcher(userID)
	if err != nil {
		return nil, err
	}
	return searcher.GetPage(ctx, userID, pageID, title)
}

func (r *LocalRouter) ListTree(ctx context.Context, userID, parentID string, depth int) ([]model.WikiNode, error) {
	searcher, err := r.searcher(userID)
	if err != nil {
		return nil, err
	}
	return searcher.ListTree(ctx, userID, parentID, depth)
}

func (r *LocalRouter) UpsertPage(ctx context.Context, userID, title, content, category, summary, mode string) (*model.WikiUpsertResult, error) {
	searcher, err := r.searcher(userID)
	if err != nil {
		return nil, err
	}
	return searcher.UpsertPage(ctx, userID, title, content, category, summary, mode)
}

func (r *LocalRouter) GetBacklinks(ctx context.Context, userID, pageID string) ([]model.WikiBacklink, error) {
	searcher, err := r.searcher(userID)
	if err != nil {
		return nil, err
	}
	return searcher.GetBacklinks(ctx, userID, pageID)
}
