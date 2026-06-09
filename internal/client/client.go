package client

import (
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

// doRequestWithRetry 在指数退避重试之外,前置一层熔断器:
//   - 熔断打开时直接返回 ErrCircuitOpen,快速失败,完全不打后端(也不做那 3 次重试),
//     避免后端真宕时每个请求都白等三轮退避、拖慢整体响应;
//   - 请求结束后据结果更新熔断器:网络错误/5xx 重试耗尽记为失败,拿到响应(含 4xx,
//     说明后端存活)记为成功,调用方主动取消(context 取消/超时)为中性、不计入。
func doRequestWithRetry(ctx context.Context, httpClient *http.Client, req *http.Request, apiName string) (*http.Response, error) {
	if err := upstreamBreaker.Allow(); err != nil {
		logger.APIfCtx(ctx, apiName, "熔断器开启,快速失败(不打后端): %v", err)
		return nil, err
	}

	resp, err := doRequestWithRetryInner(ctx, httpClient, req, apiName)

	switch {
	case err == nil:
		// 拿到响应即视为后端存活(即便是 4xx);健康度恢复。
		upstreamBreaker.RecordSuccess()
	case isCallerCanceled(err):
		// 调用方主动取消(context.Canceled)与上游健康无关,保持中性不计入。
		// 注意:超时(DeadlineExceeded)不在此列——它通常是后端卡死导致客户端超时,
		// 正是熔断器要捕捉的「宕机/挂起」信号,应记为失败。
	default:
		upstreamBreaker.RecordFailure()
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
	var resp *http.Response
	var err error
	maxAttempts := 3
	backoff := 100 * time.Millisecond

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Stop if context is already cancelled/timed out
		if err = ctx.Err(); err != nil {
			return nil, err
		}

		if attempt > 1 {
			logger.APIfCtx(ctx, apiName, "正在进行第 %d 次重试，等待 %v...", attempt, backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		resp, err = httpClient.Do(req)
		if err == nil {
			// If it's a server-side transient error, retry.
			if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
				logger.APIfCtx(ctx, apiName, "尝试 %d 失败，服务侧状态码: %d", attempt, resp.StatusCode)
				resp.Body.Close()
				err = fmt.Errorf("server error: status=%d", resp.StatusCode)
				continue
			}
			return resp, nil
		}

		// Network/timeout error, we can retry.
		logger.APIfCtx(ctx, apiName, "尝试 %d 异常: %v", attempt, err)
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", maxAttempts, err)
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
