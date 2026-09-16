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
	circuitBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xkt_circuit_breaker_state",
		Help: "熔断器当前状态(0=closed, 1=half-open, 2=open)。",
	}, []string{"name"})
	wikiIndexDocuments = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xkt_wiki_index_documents_total",
		Help: "Wiki 当前索引文档数。",
	}, []string{"index"})
	wikiIndexLastRefresh = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xkt_wiki_index_last_refresh_unix",
		Help: "Wiki 索引最后成功刷新时间(Unix 秒)。",
	}, []string{"index"})
	wikiIndexRefreshDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "xkt_wiki_index_refresh_duration_seconds",
		Help:    "Wiki 索引刷新耗时(秒)。",
		Buckets: prometheus.DefBuckets,
	}, []string{"index", "status"})

	upstreamDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "xkt_upstream_request_duration_seconds",
		Help:    "上游 HTTP 请求耗时分布(秒)。",
		Buckets: prometheus.DefBuckets,
	}, []string{"api", "method", "status_code"})
	upstreamRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xkt_upstream_retries_total",
		Help: "上游 HTTP 请求重试次数。",
	}, []string{"api"})
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

// ObserveCircuitBreakerState 更新熔断器当前状态。
func ObserveCircuitBreakerState(name string, state float64) {
	circuitBreakerState.WithLabelValues(name).Set(state)
}

// ObserveWikiIndexRefresh 记录一次 Wiki 索引刷新。
func ObserveWikiIndexRefresh(index string, documents int, refreshedAt time.Time, d time.Duration, success bool) {
	status := StatusError
	if success {
		status = StatusOK
		wikiIndexDocuments.WithLabelValues(index).Set(float64(documents))
		wikiIndexLastRefresh.WithLabelValues(index).Set(float64(refreshedAt.Unix()))
	}
	wikiIndexRefreshDuration.WithLabelValues(index, status).Observe(d.Seconds())
}

// ObserveUpstreamRequest 记录一次上游 HTTP 尝试。
func ObserveUpstreamRequest(api, method, statusCode string, d time.Duration) {
	upstreamDuration.WithLabelValues(api, method, statusCode).Observe(d.Seconds())
}

// ObserveUpstreamRetry 记录一次上游重试。
func ObserveUpstreamRetry(api string) {
	upstreamRetries.WithLabelValues(api).Inc()
}

// Handler 返回 Prometheus 文本格式导出端点(默认注册表,含 go_*/process_* 运行时指标)。
func Handler() http.Handler {
	return promhttp.Handler()
}
