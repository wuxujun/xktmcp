package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wuxujun/xktmcp/internal/logger"
)

// doRequestWithRetry executes an HTTP request with exponential backoff retries for 5xx and network errors.
func doRequestWithRetry(ctx context.Context, httpClient *http.Client, req *http.Request, apiName string) (*http.Response, error) {
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
			logger.APIf(apiName, "正在进行第 %d 次重试，等待 %v...", attempt, backoff)
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
				logger.APIf(apiName, "尝试 %d 失败，服务侧状态码: %d", attempt, resp.StatusCode)
				resp.Body.Close()
				err = fmt.Errorf("server error: status=%d", resp.StatusCode)
				continue
			}
			return resp, nil
		}

		// Network/timeout error, we can retry.
		logger.APIf(apiName, "尝试 %d 异常: %v", attempt, err)
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
