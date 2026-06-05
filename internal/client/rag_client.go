package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/model"
)

type RagAPI struct {
	baseURL  string
	apiToken string
	client   *http.Client
}

type ragSearchResponse struct {
	Data []model.Rag `json:"data"`
}

func NewRagAPI(cfg Config) *RagAPI {
	var transport *http.Transport
	if defaultTr, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTr.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.Proxy = nil

	return &RagAPI{
		baseURL:  cfg.BaseURL,
		apiToken: cfg.APIToken,
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}
}

func (a *RagAPI) SearchRags(ctx context.Context, userId, query string) ([]model.Rag, error) {
	u := fmt.Sprintf("%s/api/ai/rag/search?userId=%s&query=%s", a.baseURL, userId, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIf("SearchRags", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "SearchRags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	logger.APIf("SearchRags", "响应状态码: %d", resp.StatusCode)
	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("search rag failed: status=%d error=%s", resp.StatusCode, errMsg)
	}
	var out ragSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a *RagAPI) applyHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.apiToken)
	req.Header.Set("Accept", "application/json")
}
