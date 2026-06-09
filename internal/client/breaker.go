package client

import (
	"errors"
	"sync"
	"time"

	"github.com/wuxujun/xktmcp/internal/logger"
)

// ErrCircuitOpen 在熔断器处于打开状态、快速失败时返回。调用方可用 errors.Is 判定,
// 据此区分「上游真的不可用」与「单次请求失败」。
var ErrCircuitOpen = errors.New("upstream circuit breaker open: service temporarily unavailable")

// circuitState 是熔断器的三种状态。
type circuitState int

const (
	stateClosed   circuitState = iota // 正常:请求放行,累计连续失败
	stateOpen                         // 打开:快速失败,不打后端,直到冷却期满
	stateHalfOpen                     // 半开:放行有限探测请求,成功则恢复、失败则重新打开
)

func (s circuitState) String() string {
	switch s {
	case stateClosed:
		return "closed"
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// 默认参数:连续 5 次失败打开;冷却 10s 后进入半开;半开放行 1 个探测请求。
const (
	defaultFailureThreshold = 5
	defaultCooldown         = 10 * time.Second
	defaultHalfOpenProbes   = 1
)

// CircuitBreaker 是并发安全的轻量熔断器(三状态)。
//
// 语义:
//   - Closed:Allow 始终放行;RecordFailure 累加连续失败数,达到 failureThreshold 则转 Open;
//     RecordSuccess 清零连续失败数。
//   - Open:Allow 在冷却期内直接返回 ErrCircuitOpen(快速失败,不打后端);冷却期满后,
//     首个 Allow 把状态切到 Half-Open 并放行该探测请求。
//   - Half-Open:最多放行 halfOpenProbes 个探测请求;累计同样数量的成功则转 Closed(恢复);
//     任一探测失败则立刻转回 Open 并重置冷却计时(避免把恢复中的后端再次打垮)。
//
// 只统计「上游健康度」相关的失败(网络错误、5xx 重试耗尽);4xx 视为后端存活、记为成功;
// 调用方主动取消(context 取消/超时)为中性,不计入(由调用方决定是否 Record)。
type CircuitBreaker struct {
	mu               sync.Mutex
	name             string
	enabled          bool
	failureThreshold int
	cooldown         time.Duration
	halfOpenProbes   int

	state               circuitState
	consecutiveFailures int
	openedAt            time.Time
	probesInFlight      int
	probeSuccesses      int

	// nowFn 为可注入时钟,便于测试冷却逻辑;默认 time.Now。
	nowFn func() time.Time
}

// NewCircuitBreaker 用给定参数构造熔断器。非正数参数回退到默认值。
func NewCircuitBreaker(name string, failureThreshold int, cooldown time.Duration, halfOpenProbes int) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = defaultFailureThreshold
	}
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	if halfOpenProbes <= 0 {
		halfOpenProbes = defaultHalfOpenProbes
	}
	return &CircuitBreaker{
		name:             name,
		enabled:          true,
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
		halfOpenProbes:   halfOpenProbes,
		state:            stateClosed,
		nowFn:            time.Now,
	}
}

// Allow 在发起请求前调用。返回 nil 表示放行;返回 ErrCircuitOpen 表示熔断打开、应快速失败。
func (cb *CircuitBreaker) Allow() error {
	if cb == nil || !cb.enabled {
		return nil
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateClosed:
		return nil

	case stateOpen:
		if cb.nowFn().Sub(cb.openedAt) < cb.cooldown {
			return ErrCircuitOpen
		}
		// 冷却期满:进入半开,放行首个探测请求。
		cb.transitionLocked(stateHalfOpen)
		cb.probesInFlight = 1
		cb.probeSuccesses = 0
		return nil

	case stateHalfOpen:
		// 半开期间限制并发探测数量,多余请求继续快速失败,避免冲击恢复中的后端。
		if cb.probesInFlight < cb.halfOpenProbes {
			cb.probesInFlight++
			return nil
		}
		return ErrCircuitOpen

	default:
		return nil
	}
}

// RecordSuccess 在请求成功(后端存活,含 4xx)后调用。
func (cb *CircuitBreaker) RecordSuccess() {
	if cb == nil || !cb.enabled {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateClosed:
		cb.consecutiveFailures = 0
	case stateHalfOpen:
		if cb.probesInFlight > 0 {
			cb.probesInFlight--
		}
		cb.probeSuccesses++
		if cb.probeSuccesses >= cb.halfOpenProbes {
			cb.transitionLocked(stateClosed)
			cb.consecutiveFailures = 0
			cb.probesInFlight = 0
			cb.probeSuccesses = 0
		}
	}
}

// RecordFailure 在请求失败(网络错误或 5xx 重试耗尽)后调用。
func (cb *CircuitBreaker) RecordFailure() {
	if cb == nil || !cb.enabled {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateClosed:
		cb.consecutiveFailures++
		if cb.consecutiveFailures >= cb.failureThreshold {
			cb.transitionLocked(stateOpen)
			cb.openedAt = cb.nowFn()
		}
	case stateHalfOpen:
		// 探测失败:重新打开并重置冷却计时。
		cb.transitionLocked(stateOpen)
		cb.openedAt = cb.nowFn()
		cb.probesInFlight = 0
		cb.probeSuccesses = 0
	}
}

// State 返回当前状态字符串(供测试/可观测使用)。
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state.String()
}

// transitionLocked 切换状态并打日志。调用方须持有 cb.mu。
func (cb *CircuitBreaker) transitionLocked(to circuitState) {
	if cb.state == to {
		return
	}
	from := cb.state
	cb.state = to
	switch to {
	case stateOpen:
		logger.Errorf("[CB:%s] 熔断器打开(从 %s):连续失败达阈值,%v 内快速失败", cb.name, from, cb.cooldown)
	case stateHalfOpen:
		logger.Infof("[CB:%s] 熔断器进入半开(从 %s):放行探测请求", cb.name, from)
	case stateClosed:
		logger.Infof("[CB:%s] 熔断器关闭(从 %s):上游已恢复,放量", cb.name, from)
	}
}

// reset 仅供测试:把熔断器恢复到初始 Closed 状态。
func (cb *CircuitBreaker) reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = stateClosed
	cb.consecutiveFailures = 0
	cb.probesInFlight = 0
	cb.probeSuccesses = 0
	cb.openedAt = time.Time{}
}

// upstreamBreaker 是所有上游 API 调用共享的熔断器(同一后端 BASE_URL)。
// 用合理的硬编码默认值,无需 env;如需按环境调参,可后续在 main 装配后注入。
var upstreamBreaker = NewCircuitBreaker("upstream", defaultFailureThreshold, defaultCooldown, defaultHalfOpenProbes)
