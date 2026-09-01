package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/model"
)

type StaffAPI struct {
	baseURL  string
	apiToken string
	client   *http.Client
	breaker  *CircuitBreaker
}

type staffSearchResponse struct {
	Data []model.Staff `json:"data"`
}

func NewStaffAPI(cfg Config) *StaffAPI {
	return &StaffAPI{
		baseURL:  cfg.BaseURL,
		apiToken: cfg.APIToken,
		client:   newAPIHTTPClient(cfg.Timeout),
		breaker:  staffBreaker,
	}
}

func (a *StaffAPI) SearchStaffs(ctx context.Context, userId, query string) ([]model.Staff, error) {
	userId = strings.TrimSpace(userId)
	u := fmt.Sprintf("%s/api/staff?query=%s", a.baseURL, url.QueryEscape(query))
	if userId != "" {
		u = fmt.Sprintf("%s/api/staff?userid=%s&query=%s", a.baseURL, url.QueryEscape(userId), url.QueryEscape(query))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "SearchStaffs", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "SearchStaffs", a.breaker)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	logger.APIfCtx(ctx, "SearchStaffs", "响应状态码: %d", resp.StatusCode)
	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("search staff failed: status=%d error=%s", resp.StatusCode, errMsg)
	}
	var out staffSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a *StaffAPI) applyHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.apiToken)
	req.Header.Set("Accept", "application/json")
}
