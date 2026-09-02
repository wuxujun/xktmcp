package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/model"
)

type WikiAPI struct {
	baseURL  string
	apiToken string
	client   *http.Client
	breaker  *CircuitBreaker
}

type wikiSearchResponse struct {
	Data []model.WikiSearchResult `json:"data"`
}

type wikiGetPageResponse struct {
	Data model.WikiPage `json:"data"`
}

type wikiListTreeResponse struct {
	Data []model.WikiNode `json:"data"`
}

type wikiUpsertResponse struct {
	Data model.WikiUpsertResult `json:"data"`
}

type wikiBacklinksResponse struct {
	Data []model.WikiBacklink `json:"data"`
}

func NewWikiAPI(cfg Config, breakers ...*CircuitBreaker) *WikiAPI {
	breaker := wikiBreaker
	if len(breakers) > 0 && breakers[0] != nil {
		breaker = breakers[0]
	}
	return &WikiAPI{
		baseURL:  cfg.BaseURL,
		apiToken: cfg.APIToken,
		client:   newAPIHTTPClient(cfg.Timeout),
		breaker:  breaker,
	}
}

// SearchWiki 检索 Wiki 词条与内容
func (a *WikiAPI) SearchWiki(ctx context.Context, userId, query, category string, topK int) ([]model.WikiSearchResult, error) {
	userId = strings.TrimSpace(userId)
	params := url.Values{}
	params.Set("query", query)
	if userId != "" {
		params.Set("userId", userId)
	}
	if category != "" {
		params.Set("category", category)
	}
	if topK > 0 {
		params.Set("top_k", strconv.Itoa(topK))
	}

	u := fmt.Sprintf("%s/api/ai/wiki/search?%s", a.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "SearchWiki", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "SearchWiki", a.breaker)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("search wiki failed: status=%d error=%s", resp.StatusCode, errMsg)
	}

	var out wikiSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetPage 获取指定词条的详情与 Markdown 正文
func (a *WikiAPI) GetPage(ctx context.Context, userId, pageID, title string) (*model.WikiPage, error) {
	userId = strings.TrimSpace(userId)
	params := url.Values{}
	if pageID != "" {
		params.Set("page_id", pageID)
	}
	if title != "" {
		params.Set("title", title)
	}
	if userId != "" {
		params.Set("userId", userId)
	}

	u := fmt.Sprintf("%s/api/ai/wiki/page?%s", a.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "GetWikiPage", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "GetWikiPage", a.breaker)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("get wiki page failed: status=%d error=%s", resp.StatusCode, errMsg)
	}

	var out wikiGetPageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// ListTree 获取 Wiki 目录树
func (a *WikiAPI) ListTree(ctx context.Context, userId, parentID string, depth int) ([]model.WikiNode, error) {
	userId = strings.TrimSpace(userId)
	params := url.Values{}
	if parentID != "" {
		params.Set("parent_id", parentID)
	}
	if depth > 0 {
		params.Set("depth", strconv.Itoa(depth))
	}
	if userId != "" {
		params.Set("userId", userId)
	}

	u := fmt.Sprintf("%s/api/ai/wiki/tree?%s", a.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "ListWikiTree", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "ListWikiTree", a.breaker)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("list wiki tree failed: status=%d error=%s", resp.StatusCode, errMsg)
	}

	var out wikiListTreeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

type upsertPagePayload struct {
	UserID   string `json:"userId,omitempty"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

// UpsertPage 创建、更新或追加 Wiki 词条
func (a *WikiAPI) UpsertPage(ctx context.Context, userId, title, content, category, summary, mode string) (*model.WikiUpsertResult, error) {
	payload := upsertPagePayload{
		UserID:   strings.TrimSpace(userId),
		Title:    title,
		Content:  content,
		Category: category,
		Summary:  summary,
		Mode:     mode,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	u := fmt.Sprintf("%s/api/ai/wiki/page/upsert", a.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	logger.APIfCtx(ctx, "UpsertWikiPage", "发起请求: %s (mode=%s title=%s)", u, mode, title)
	resp, err := doRequestWithRetry(ctx, a.client, req, "UpsertWikiPage", a.breaker)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("upsert wiki page failed: status=%d error=%s", resp.StatusCode, errMsg)
	}

	var out wikiUpsertResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// GetBacklinks 获取某词条的反向引用
func (a *WikiAPI) GetBacklinks(ctx context.Context, userId, pageID string) ([]model.WikiBacklink, error) {
	userId = strings.TrimSpace(userId)
	params := url.Values{}
	params.Set("page_id", pageID)
	if userId != "" {
		params.Set("userId", userId)
	}

	u := fmt.Sprintf("%s/api/ai/wiki/backlinks?%s", a.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "GetWikiBacklinks", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "GetWikiBacklinks", a.breaker)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("get wiki backlinks failed: status=%d error=%s", resp.StatusCode, errMsg)
	}

	var out wikiBacklinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a *WikiAPI) applyHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.apiToken)
	req.Header.Set("Accept", "application/json")
}
