package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wuxujun/xktmcp/internal/logger"
	"github.com/wuxujun/xktmcp/internal/model"
)

type Config struct {
	BaseURL  string
	APIToken string
	Timeout  time.Duration
}

type StudentAPI struct {
	baseURL  string
	apiToken string
	client   *http.Client
}

type searchResponse struct {
	Data []model.Student `json:"data"`
}

type orderResponse struct {
	Data []model.StudentOrder `json:"data"`
}

type examResponse struct {
	Data []model.StudentExam `json:"data"`
}

type getResponse struct {
	Data []model.Student `json:"data"`
}

func LoadConfigFromEnv() (Config, error) {
	baseURL := strings.TrimSpace(os.Getenv("BASE_URL"))
	if baseURL == "" {
		baseURL = "https://yk.xkt.com"
	}
	logger.APIf("LoadConfig", "BaseURL: %s", baseURL)
	// 不再使用硬编码弱默认值;缺失上游 API 令牌直接报错(fail-closed)。
	apiToken := strings.TrimSpace(os.Getenv("API_TOKEN"))
	if apiToken == "" {
		return Config{}, fmt.Errorf("missing required env API_TOKEN")
	}

	timeout := 10 * time.Second
	if raw := strings.TrimSpace(os.Getenv("TIMEOUT_SECONDS")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("invalid TIMEOUT_SECONDS")
		}
		timeout = time.Duration(v) * time.Second
	}

	return Config{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		APIToken: apiToken,
		Timeout:  timeout,
	}, nil
}

func NewStudentAPI(cfg Config) *StudentAPI {
	return &StudentAPI{
		baseURL:  cfg.BaseURL,
		apiToken: cfg.APIToken,
		client:   &http.Client{Timeout: cfg.Timeout},
	}
}

func (a *StudentAPI) SearchStudents(ctx context.Context, query string) ([]model.Student, error) {
	u := fmt.Sprintf("%s/api/student?query=%s", a.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "SearchStudents", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "SearchStudents")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	logger.APIfCtx(ctx, "SearchStudents", "响应状态码: %d", resp.StatusCode)
	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("search students failed: status=%d error=%s", resp.StatusCode, errMsg)
	}
	var out searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a *StudentAPI) SearchOrders(ctx context.Context, query string) ([]model.StudentOrder, error) {
	u := fmt.Sprintf("%s/api/student/order?query=%s", a.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "SearchOrders", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "SearchOrders")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	logger.APIfCtx(ctx, "SearchOrders", "响应状态码: %d", resp.StatusCode)
	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("search orders failed: status=%d error=%s", resp.StatusCode, errMsg)
	}
	var out orderResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a *StudentAPI) SearchExam(ctx context.Context, query string) ([]model.StudentExam, error) {
	u := fmt.Sprintf("%s/api/student/exam?query=%s", a.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "SearchExam", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "SearchExam")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	logger.APIfCtx(ctx, "SearchExam", "响应状态码: %d", resp.StatusCode)
	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("search exam failed: status=%d error=%s", resp.StatusCode, errMsg)
	}
	var out examResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a *StudentAPI) GetStudent(ctx context.Context, id string) (*model.Student, error) {
	u := fmt.Sprintf("%s/api/student/%s", a.baseURL, url.PathEscape(id))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	a.applyHeaders(req)

	logger.APIfCtx(ctx, "GetStudent", "发起请求: %s", u)
	resp, err := doRequestWithRetry(ctx, a.client, req, "GetStudent")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	logger.APIfCtx(ctx, "GetStudent", "响应状态码: %d", resp.StatusCode)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("student not found: %s", id)
	}
	if resp.StatusCode >= 300 {
		errMsg := readErrorDetails(resp)
		return nil, fmt.Errorf("get student failed: status=%d error=%s", resp.StatusCode, errMsg)
	}

	var out getResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("student not found: %s", id)
	}
	return &out.Data[0], nil
}

func (a *StudentAPI) applyHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.apiToken)
	req.Header.Set("Accept", "application/json")
}
