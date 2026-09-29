package wiki

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wuxujun/xktmcp/internal/model"
	"github.com/wuxujun/xktmcp/internal/trace"
)

var ErrUserWikiNotConfigured = errors.New("local wiki is not configured for this user")

// LocalRouter 按调用者 userId 将已配置用户的本地 Wiki 操作路由到独立目录，
// 未配置用户是否使用默认目录由 RequireUserMapping 控制。userId 只作为显式配置映射的 key，绝不参与路径拼接。
type LocalRouter struct {
	defaultSearcher *LocalSearcher
	users           map[string]*LocalSearcher
	requireMapping  bool
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
		defaultSearcher: defaultSearcher,
		users:           make(map[string]*LocalSearcher, len(cfg.Users)),
		requireMapping:  cfg.RequireUserMapping,
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

func (r *LocalRouter) searcher(ctx context.Context, userID string) (*LocalSearcher, error) {
	userID = strings.TrimSpace(userID)
	if ctx != nil {
		principal := trace.AuthenticatedUserIDFromContext(ctx)
		// 若请求持有可信主体，且显式指定的 userID 与其冲突，拒绝访问
		if principal != "" && userID != "" && principal != userID {
			return nil, ErrUserWikiNotConfigured
		}
		// 若请求持有可信主体且 userID 为空，优先采用可信主体
		if principal != "" && userID == "" {
			userID = principal
		}

		// 若来自外部网络请求，且试图访问私有租户目录
		if trace.RequestOriginFromContext(ctx) == trace.OriginNetwork {
			if _, isPrivateUser := r.users[userID]; isPrivateUser {
				// 访问私有用户目录必须持有完全一致的已认证可信主体
				if principal == "" || principal != userID {
					if r.requireMapping {
						return nil, ErrUserWikiNotConfigured
					}
					return r.defaultSearcher, nil
				}
			}
		}
	}

	if searcher, ok := r.users[userID]; ok {
		return searcher, nil
	}
	if r.requireMapping {
		return nil, ErrUserWikiNotConfigured
	}
	return r.defaultSearcher, nil
}

func (r *LocalRouter) SearchWiki(ctx context.Context, userID, query, category string, topK int) ([]model.WikiSearchResult, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return nil, err
	}
	return searcher.SearchWiki(ctx, userID, query, category, topK)
}

func (r *LocalRouter) GetPage(ctx context.Context, userID, pageID, title string) (*model.WikiPage, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return nil, err
	}
	return searcher.GetPage(ctx, userID, pageID, title)
}

func (r *LocalRouter) ListTree(ctx context.Context, userID, parentID string, depth int) ([]model.WikiNode, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return nil, err
	}
	return searcher.ListTree(ctx, userID, parentID, depth)
}

func (r *LocalRouter) UpsertPage(ctx context.Context, userID, title, content, category, summary, mode string) (*model.WikiUpsertResult, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return nil, err
	}
	return searcher.UpsertPage(ctx, userID, title, content, category, summary, mode)
}

func (r *LocalRouter) GetBacklinks(ctx context.Context, userID, pageID string) ([]model.WikiBacklink, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return nil, err
	}
	return searcher.GetBacklinks(ctx, userID, pageID)
}

func (r *LocalRouter) ListResources(ctx context.Context, userID string, limit int) (ResourceCatalog, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return ResourceCatalog{}, err
	}
	return searcher.ListResources(ctx, limit)
}

func (r *LocalRouter) ReadPageResource(ctx context.Context, userID, uri string) (string, error) {
	searcher, err := r.searcher(ctx, userID)
	if err != nil {
		return "", err
	}
	return searcher.ReadPageResource(ctx, uri)
}
