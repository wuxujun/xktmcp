// Package metrics 暴露 Prometheus 指标:每个 MCP 工具的调用量、错误数与耗时分布,
// 以及各工具的缓存命中率。通过 /metrics 端点(promhttp)抓取,排障时无需再翻日志统计。
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 状态标签取値。
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

	// cacheHits 按工具名统计缓存命中次数。
	// 命中率 = hits / (hits + misses)。
	cacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xkt_cache_hits_total",
		Help: "工具缓存命中次数,按工具名(tool)区分。命中率 = hits/(hits+misses)。",
	}, []string{"tool"})

	// cacheMisses 按工具名统计缓存未命中次数。
	cacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xkt_cache_misses_total",
		Help: "工具缓存未命中次数,按工具名(tool)区分。",
	}, []string{"tool"})

	// circuitBreakerTransitions 统计熔断器状态转换次数。
	circuitBreakerTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xkt_circuit_breaker_transitions_total",
		Help: "熔断器状态转换次数,按熔断器名(name)与目标状态(to_state)区分。",
	}, []string{"name", "to_state"})
)

// ObserveToolCall 记录一次工具调用:计数(按状态)+ 观测耗时。
func ObserveToolCall(tool, status string, d time.Duration) {
	toolCalls.WithLabelValues(tool, status).Inc()
	toolDuration.WithLabelValues(tool).Observe(d.Seconds())
}

// ObserveCacheAccess 记录一次缓存访问:hit=true 表示命中,false 表示未命中。
// tool 应与调用工具名保持一致(student_search / rag_search 等)。
func ObserveCacheAccess(tool string, hit bool) {
	if hit {
		cacheHits.WithLabelValues(tool).Inc()
	} else {
		cacheMisses.WithLabelValues(tool).Inc()
	}
}

// ObserveCircuitBreakerTransition 记录一次熔断器状态转换。
func ObserveCircuitBreakerTransition(name, toState string) {
	circuitBreakerTransitions.WithLabelValues(name, toState).Inc()
}

// Handler 返回 Prometheus 文本格式导出端点(默认注册表,含 go_*/process_* 运行时指标)。
func Handler() http.Handler {
	return promhttp.Handler()
}
