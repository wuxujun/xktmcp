// Package metrics 暴露 Prometheus 指标:每个 MCP 工具的调用量、错误数与耗时分布。
// 通过 /metrics 端点(promhttp)抓取,排障时无需再翻日志统计。
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 状态标签取值。
const (
	StatusOK    = "ok"
	StatusError = "error"
)

var (
	// toolCalls 按工具名 + 状态(ok/error)统计调用总数;错误率 = error/总数。
	toolCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xkt_tool_calls_total",
		Help: "MCP 工具调用总次数,按工具名(tool)与状态(status: ok/error)区分。",
	}, []string{"tool", "status"})

	// toolDuration 按工具名统计耗时分布(秒)。桶上限到 60s,覆盖上游可能的长超时。
	toolDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "xkt_tool_duration_seconds",
		Help:    "MCP 工具调用耗时分布(秒),按工具名(tool)区分。",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"tool"})
)

// ObserveToolCall 记录一次工具调用:计数(按状态)+ 观测耗时。
func ObserveToolCall(tool, status string, d time.Duration) {
	toolCalls.WithLabelValues(tool, status).Inc()
	toolDuration.WithLabelValues(tool).Observe(d.Seconds())
}

// Handler 返回 Prometheus 文本格式导出端点(默认注册表,含 go_*/process_* 运行时指标)。
func Handler() http.Handler {
	return promhttp.Handler()
}
