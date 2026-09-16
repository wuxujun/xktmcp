package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ObserveToolCall 应正确累加计数(按工具名 + 状态)。
func TestObserveToolCall_Counter(t *testing.T) {
	before := testutil.ToFloat64(toolCalls.WithLabelValues("unit_test_tool", StatusOK))

	ObserveToolCall("unit_test_tool", StatusOK, 50*time.Millisecond)
	ObserveToolCall("unit_test_tool", StatusOK, 80*time.Millisecond)
	ObserveToolCall("unit_test_tool", StatusError, 10*time.Millisecond)

	if got := testutil.ToFloat64(toolCalls.WithLabelValues("unit_test_tool", StatusOK)); got != before+2 {
		t.Errorf("ok 计数应 +2,得到 %v(before=%v)", got, before)
	}
	if got := testutil.ToFloat64(toolCalls.WithLabelValues("unit_test_tool", StatusError)); got != 1 {
		t.Errorf("error 计数应为 1,得到 %v", got)
	}
}

func TestObserveUpstreamRequestAndRetry(t *testing.T) {
	beforeRetries := testutil.ToFloat64(upstreamRetries.WithLabelValues("unit_test_api"))

	ObserveUpstreamRequest("unit_test_api", "GET", "200", 25*time.Millisecond)
	ObserveUpstreamRetry("unit_test_api")

	if got := testutil.ToFloat64(upstreamRetries.WithLabelValues("unit_test_api")); got != beforeRetries+1 {
		t.Errorf("upstream retry count = %v, want %v", got, beforeRetries+1)
	}
}

func TestObserveCircuitBreakerState(t *testing.T) {
	ObserveCircuitBreakerState("unit_test_breaker", 2)
	if got := testutil.ToFloat64(circuitBreakerState.WithLabelValues("unit_test_breaker")); got != 2 {
		t.Errorf("circuit breaker state = %v, want 2", got)
	}
}

func TestObserveWikiIndexRefresh(t *testing.T) {
	now := time.Unix(123, 0)
	ObserveWikiIndexRefresh("unit_test_index", 7, now, 20*time.Millisecond, true)
	if got := testutil.ToFloat64(wikiIndexDocuments.WithLabelValues("unit_test_index")); got != 7 {
		t.Errorf("wiki document gauge = %v, want 7", got)
	}
	if got := testutil.ToFloat64(wikiIndexLastRefresh.WithLabelValues("unit_test_index")); got != 123 {
		t.Errorf("wiki refresh gauge = %v, want 123", got)
	}
}

// /metrics 端点应输出 Prometheus 文本格式,且包含自定义指标名。
func TestHandlerExposesMetrics(t *testing.T) {
	ObserveToolCall("exposed_tool", StatusOK, 30*time.Millisecond)
	ObserveUpstreamRequest("exposed_api", "GET", "200", 20*time.Millisecond)
	ObserveUpstreamRetry("exposed_api")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics 应返回 200,得到 %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"xkt_tool_calls_total",
		"xkt_tool_duration_seconds",
		`tool="exposed_tool"`,
		"xkt_upstream_request_duration_seconds",
		"xkt_upstream_retries_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics 输出应包含 %q", want)
		}
	}
}
