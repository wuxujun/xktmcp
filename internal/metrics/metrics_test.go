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

// /metrics 端点应输出 Prometheus 文本格式,且包含自定义指标名。
func TestHandlerExposesMetrics(t *testing.T) {
	ObserveToolCall("exposed_tool", StatusOK, 30*time.Millisecond)

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
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics 输出应包含 %q", want)
		}
	}
}
