package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wuxujun/xktmcp/internal/logger"
)

func newAPIHTTPClient(timeout time.Duration) *http.Client {
	var transport *http.Transport
	if defaultTr, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTr.Clone()
	} else {
		transport = &http.Transport{}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func doRequestWithRetry(ctx context.Context, httpClient *http.Client, req *http.Request, apiName string, cb *CircuitBreaker) (*http.Response, error) {
	if cb == nil {
		cb = upstreamBreaker
	}
	if err := cb.Allow(); err != nil {
		logger.APIfCtx(ctx, apiName, "熔断器开启,快速失败(不打后端): %v", err)
		return nil, err
	}

	resp, err := doRequestWithRetryInner(ctx, httpClient, req, apiName)

	switch {
	case err == nil:
		// 拿到响应即视为后端存活(即便是 4xx);健康度恢复。
		cb.RecordSuccess()
	case isCallerCanceled(err):
		// 调用方主动取消(context.Canceled)与上游健康无关,保持中性不计入。
		// 注意:超时(DeadlineExceeded)不在此列——它通常是后端卡死导致客户端超时,
		// 正是熔断器要捕捉的「宕机/挂起」信号,应记为失败。
	default:
		cb.RecordFailure()
	}

	return resp, err
}

// isCallerCanceled 判断 err 是否为调用方【主动取消】(context.Canceled)。
// 超时(context.DeadlineExceeded)刻意不算在内:它多由后端卡死引发,应计为失败。
func isCallerCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// doRequestWithRetryInner executes an HTTP request with exponential backoff retries for 5xx and network errors.
func doRequestWithRetryInner(ctx context.Context, httpClient *http.Client, req *http.Request, apiName string) (*http.Response, error) {
	const transientResponseDrainLimit = 64 << 10
	var resp *http.Response
	var err error
	maxAttempts := 3
	if !retryableRequest(req) {
		maxAttempts = 1
	}
	backoff := 100 * time.Millisecond

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Stop if context is already cancelled/timed out
		if err = ctx.Err(); err != nil {
			return nil, err
		}

		if attempt > 1 {
			logger.APIfCtx(ctx, apiName, "正在进行第 %d 次重试，等待 %v...", attempt, backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
		}

		attemptReq, prepareErr := requestForAttempt(ctx, req)
		if prepareErr != nil {
			return nil, prepareErr
		}
		resp, err = httpClient.Do(attemptReq)
		if err == nil {
			// If it's a server-side transient error, retry.
			if retryableRequest(req) && resp.StatusCode >= 500 && resp.StatusCode <= 599 {
				logger.APIfCtx(ctx, apiName, "尝试 %d 失败，服务侧状态码: %d", attempt, resp.StatusCode)
				_, _ = io.CopyN(io.Discard, resp.Body, transientResponseDrainLimit)
				resp.Body.Close()
				err = fmt.Errorf("server error: status=%d", resp.StatusCode)
				continue
			}
			return resp, nil
		}

		if !retryableRequest(req) {
			return nil, fmt.Errorf("request failed after %d attempt: %w", attempt, err)
		}

		// Network/timeout error, retry only for idempotent or explicitly idempotent requests.
		logger.APIfCtx(ctx, apiName, "尝试 %d 异常: %v", attempt, err)
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", maxAttempts, err)
}

func retryableRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	if strings.TrimSpace(req.Header.Get("Idempotency-Key")) != "" {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func requestForAttempt(ctx context.Context, req *http.Request) (*http.Request, error) {
	if req == nil {
		return nil, errors.New("nil HTTP request")
	}
	attempt := req.Clone(ctx)
	if req.Body == nil {
		return attempt, nil
	}
	if req.GetBody == nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("buffer request body for retry: %w", err)
		}
		_ = req.Body.Close()
		payload := append([]byte(nil), body...)
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("replay request body: %w", err)
	}
	attempt.Body = body
	attempt.GetBody = req.GetBody
	return attempt, nil
}

// readErrorDetails reads a portion of the response body and extracts a friendly error message.
func readErrorDetails(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	// Limit read to 1024 bytes to avoid loading huge responses
	limitReader := io.LimitReader(resp.Body, 1024)
	bodyBytes, err := io.ReadAll(limitReader)
	if err != nil || len(bodyBytes) == 0 {
		return ""
	}

	bodyStr := string(bodyBytes)

	// Try to parse as JSON to find common error fields
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &jsonMap); err == nil {
		for _, key := range []string{"message", "msg", "error", "description"} {
			if v, ok := jsonMap[key]; ok {
				if strVal, isStr := v.(string); isStr && strVal != "" {
					return strVal
				}
				return fmt.Sprintf("%v", v)
			}
		}
	}

	// Fallback to plain text snippet
	bodyStr = strings.TrimSpace(bodyStr)
	if len(bodyStr) > 200 {
		bodyStr = bodyStr[:200] + "..."
	}
	return bodyStr
}
