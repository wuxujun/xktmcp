package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock 是可手动推进的测试时钟。
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time            { return f.t }
func (f *fakeClock) advance(d time.Duration)   { f.t = f.t.Add(d) }

// newTestBreaker 构造一个挂了假时钟的熔断器。
func newTestBreaker(threshold int, cooldown time.Duration, probes int) (*CircuitBreaker, *fakeClock) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	cb := NewCircuitBreaker("test", threshold, cooldown, probes)
	cb.nowFn = clk.now
	return cb, clk
}

// 连续失败达到阈值后打开,并在冷却期内快速失败。
func TestCircuitBreaker_TripsOpen(t *testing.T) {
	cb, _ := newTestBreaker(3, time.Minute, 1)

	for i := 0; i < 2; i++ {
		cb.RecordFailure()
		if err := cb.Allow(); err != nil {
			t.Fatalf("未达阈值前应放行,第 %d 次失败后却被拒", i+1)
		}
	}
	// 第 3 次失败触发打开。
	cb.RecordFailure()
	if got := cb.State(); got != "open" {
		t.Fatalf("达到阈值后应为 open,得到 %s", got)
	}
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("打开后应快速失败返回 ErrCircuitOpen,得到 %v", err)
	}
}

// 中途成功会清零连续失败计数,避免「累计」误触发。
func TestCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	cb, _ := newTestBreaker(3, time.Minute, 1)

	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess() // 清零
	cb.RecordFailure()
	cb.RecordFailure() // 连续仅 2 次,未达阈值 3

	if got := cb.State(); got != "closed" {
		t.Fatalf("成功清零后不应打开,得到 %s", got)
	}
	if err := cb.Allow(); err != nil {
		t.Fatalf("应仍放行,得到 %v", err)
	}
}

// 冷却期满进入半开,放行单个探测;探测成功则恢复关闭。
func TestCircuitBreaker_CooldownThenHalfOpenSuccess(t *testing.T) {
	cb, clk := newTestBreaker(1, 10*time.Second, 1)

	cb.RecordFailure() // 阈值 1,立即打开
	if cb.State() != "open" {
		t.Fatalf("应打开")
	}

	// 冷却期内:快速失败。
	clk.advance(5 * time.Second)
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("冷却期内应快速失败,得到 %v", err)
	}

	// 冷却期满:首个 Allow 进入半开并放行探测。
	clk.advance(6 * time.Second) // 累计 11s > 10s
	if err := cb.Allow(); err != nil {
		t.Fatalf("冷却期满应放行探测请求,得到 %v", err)
	}
	if cb.State() != "half-open" {
		t.Fatalf("应进入 half-open,得到 %s", cb.State())
	}
	// 半开期并发探测受限:第二个请求仍快速失败。
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("半开期超出探测配额应快速失败,得到 %v", err)
	}

	// 探测成功 → 恢复关闭、放量。
	cb.RecordSuccess()
	if cb.State() != "closed" {
		t.Fatalf("探测成功后应恢复 closed,得到 %s", cb.State())
	}
	if err := cb.Allow(); err != nil {
		t.Fatalf("恢复后应放行,得到 %v", err)
	}
}

// 半开期探测失败则重新打开,并重置冷却计时。
func TestCircuitBreaker_HalfOpenProbeFailureReopens(t *testing.T) {
	cb, clk := newTestBreaker(1, 10*time.Second, 1)

	cb.RecordFailure() // 打开
	clk.advance(11 * time.Second)
	if err := cb.Allow(); err != nil {
		t.Fatalf("应进入半开放行探测,得到 %v", err)
	}

	cb.RecordFailure() // 探测失败 → 重新打开
	if cb.State() != "open" {
		t.Fatalf("探测失败后应重新 open,得到 %s", cb.State())
	}
	// 未推进时钟:仍在新的冷却期内,快速失败。
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("重新打开后冷却期内应快速失败,得到 %v", err)
	}
}

// 多次探测成功才恢复(halfOpenProbes>1)。
func TestCircuitBreaker_HalfOpenMultiProbe(t *testing.T) {
	cb, clk := newTestBreaker(1, time.Second, 2)
	cb.RecordFailure()
	clk.advance(2 * time.Second)

	if err := cb.Allow(); err != nil { // 探测 1
		t.Fatalf("探测1 应放行: %v", err)
	}
	if err := cb.Allow(); err != nil { // 探测 2(配额 2)
		t.Fatalf("探测2 应放行: %v", err)
	}
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) { // 超配额
		t.Fatalf("探测3 应被拒: %v", err)
	}

	cb.RecordSuccess() // 1/2,未达
	if cb.State() != "half-open" {
		t.Fatalf("仅 1 次成功不应恢复,得到 %s", cb.State())
	}
	cb.RecordSuccess() // 2/2,恢复
	if cb.State() != "closed" {
		t.Fatalf("达成功配额应恢复 closed,得到 %s", cb.State())
	}
}

// 禁用时熔断器为透明直通:Allow 永远放行,Record 不改变状态。
func TestCircuitBreaker_Disabled(t *testing.T) {
	cb, _ := newTestBreaker(1, time.Minute, 1)
	cb.enabled = false

	for i := 0; i < 10; i++ {
		cb.RecordFailure()
	}
	if err := cb.Allow(); err != nil {
		t.Fatalf("禁用时应始终放行,得到 %v", err)
	}
}

// nil 接收者安全(便于可选注入)。
func TestCircuitBreaker_NilSafe(t *testing.T) {
	var cb *CircuitBreaker
	if err := cb.Allow(); err != nil {
		t.Fatalf("nil 熔断器 Allow 应放行,得到 %v", err)
	}
	cb.RecordFailure() // 不应 panic
	cb.RecordSuccess() // 不应 panic
}

// isCallerCanceled 只把主动取消(Canceled)判为中性;超时(DeadlineExceeded)应计为失败。
func TestIsCallerCanceled(t *testing.T) {
	if !isCallerCanceled(context.Canceled) {
		t.Error("context.Canceled 应判为调用方取消(中性)")
	}
	if isCallerCanceled(context.DeadlineExceeded) {
		t.Error("超时不应判为调用方取消——它应计为失败")
	}
	if isCallerCanceled(errors.New("server error: status=500")) {
		t.Error("普通错误不应判为调用方取消")
	}
	if isCallerCanceled(nil) {
		t.Error("nil 不应判为调用方取消")
	}
}
