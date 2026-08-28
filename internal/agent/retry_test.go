package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sammal/internal/provider"
)

// rateLimitErr 构造限流类同步错误。
func rateLimitErr(retryAfter time.Duration) error {
	return &provider.StreamInterruptedError{
		Kind: provider.InterruptRateLimit, Err: errors.New("HTTP 429: slow down"), RetryAfter: retryAfter,
	}
}

// 限流一次后成功：step 边界重连，两次请求逐字节一致（I2），状态事件
// 显示实际等待时长。
func TestRateLimitRetriedThenSucceeds(t *testing.T) {
	fp := &fakeProvider{
		syncErrs: []error{rateLimitErr(0)},
		streams:  [][]provider.Chunk{textChunks("done")},
	}
	fx := newFixture(t, fp)
	fx.ag.retries = 2
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, turnEnded)

	var restarted bool
	var status string
	for _, ev := range evs {
		switch ev := ev.(type) {
		case StreamRestartedEvent:
			restarted = ev.Attempt == 1
		case StatusEvent:
			if strings.Contains(ev.Text, "重连") {
				status = ev.Text
			}
		case TurnEndedEvent:
			if ev.StopReason != StopCompleted {
				t.Errorf("stop = %s", ev.StopReason)
			}
		}
	}
	if !restarted {
		t.Error("missing StreamRestartedEvent{Attempt:1}")
	}
	if !strings.Contains(status, "1s 后重连（1/2）") {
		t.Errorf("status = %q", status)
	}
	if len(fp.calls) != 2 {
		t.Fatalf("provider calls = %d", len(fp.calls))
	}

	b1, err1 := provider.MarshalRequest(fp.calls[0])
	b2, err2 := provider.MarshalRequest(fp.calls[1])
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if string(b1) != string(b2) {
		t.Error("重试请求与首次请求字节不一致（违反 I2）")
	}
}

// 预算耗尽：按配置次数重试后上报错误终止 turn。
func TestRateLimitExhaustedEndsWithError(t *testing.T) {
	fp := &fakeProvider{syncErrs: []error{rateLimitErr(0), rateLimitErr(0), rateLimitErr(0)}}
	fx := newFixture(t, fp)
	fx.ag.retries = 1
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopError
	})

	var gotError bool
	for _, ev := range evs {
		if _, ok := ev.(ErrorEvent); ok {
			gotError = true
		}
	}
	if !gotError {
		t.Error("missing ErrorEvent")
	}
	if len(fp.calls) != 2 {
		t.Fatalf("provider calls = %d, want 首次+1 次重试", len(fp.calls))
	}
}

// 订阅 plan 用量窗口：配额特征命中 → 快速失败，不烧任何重试预算。
func TestQuotaWindowFailsFastWithoutRetry(t *testing.T) {
	quota := &provider.StreamInterruptedError{
		Kind: provider.InterruptQuota,
		Err:  errors.New(`HTTP 429: {"error":{"message":"You have hit your usage limit"}}`),
	}
	fp := &fakeProvider{syncErrs: []error{quota, quota}}
	fx := newFixture(t, fp)
	fx.ag.retries = 3
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopError
	})
	if len(fp.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1（配额窗口不重试）", len(fp.calls))
	}
}

// Retry-After 给到小时级（超过单次等待上限）：立即放弃重试并注明恢复时间点。
func TestOverWaitLimitFailsFastWithResumeHint(t *testing.T) {
	fp := &fakeProvider{syncErrs: []error{rateLimitErr(5 * time.Hour)}}
	fx := newFixture(t, fp)
	fx.ag.retries = 3
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopError
	})

	var errMsg string
	for _, ev := range evs {
		if ee, ok := ev.(ErrorEvent); ok {
			errMsg = ee.Err.Error()
		}
	}
	for _, want := range []string{"要求等待 5h0m0s", "停止自动重试", "可恢复", "稍后重发"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("错误文案缺 %q：%q", want, errMsg)
		}
	}
	if len(fp.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1（超限等待不重试）", len(fp.calls))
	}
}

func TestBackoffFor(t *testing.T) {
	tests := []struct {
		name       string
		attempt    int
		retryAfter time.Duration
		base       time.Duration
		want       time.Duration
	}{
		{"首试盲退避1s", 0, 0, retryBackoff, time.Second},
		{"二次2s", 1, 0, retryBackoff, 2 * time.Second},
		{"三次4s", 2, 0, retryBackoff, 4 * time.Second},
		{"指数增长32s", 5, 0, retryBackoff, 32 * time.Second},
		{"64s截断到上限", 6, 0, retryBackoff, retryCap},
		{"尊重端点30s", 0, 30 * time.Second, retryBackoff, 30 * time.Second},
		{"端点要求90s截断到上限", 0, 90 * time.Second, retryBackoff, retryCap},
		{"5s预算耗尽首试", 0, 0, defaultRateLimitBackoff, 5 * time.Second},
		{"5s预算耗尽二次10s", 1, 0, defaultRateLimitBackoff, 10 * time.Second},
		{"5s预算耗尽三次20s", 2, 0, defaultRateLimitBackoff, 20 * time.Second},
		{"5s预算耗尽四次40s", 3, 0, defaultRateLimitBackoff, 40 * time.Second},
		{"5s预算耗尽五次截断60s", 4, 0, defaultRateLimitBackoff, retryCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoffFor(tt.attempt, tt.retryAfter, tt.base); got != tt.want {
				t.Errorf("backoffFor(%d, %v, %v) = %v, want %v", tt.attempt, tt.retryAfter, tt.base, got, tt.want)
			}
		})
	}
}

func TestBackoffBaseFor(t *testing.T) {
	// budget > 0：未耗尽时 1s，耗尽后 5s。
	if got := backoffBaseFor(0, 0, 5); got != retryBackoff {
		t.Errorf("budget未耗尽 base = %v, want 1s", got)
	}
	if got := backoffBaseFor(0, 5, 5); got != retryBackoff {
		t.Errorf("budget边界 base = %v, want 1s", got)
	}
	if got := backoffBaseFor(0, 5+1, 5); got != defaultRateLimitBackoff {
		t.Errorf("budget耗尽 base = %v, want 5s", got)
	}
	// budget = 0：rlHits > 0 时 5s（关闭该机制时始终 1s 需显式传 budget=0 且 rlHits=0）。
	if got := backoffBaseFor(0, 0, 0); got != retryBackoff {
		t.Errorf("budget=0 且 rlHits=0 base = %v, want 1s", got)
	}
	if got := backoffBaseFor(0, 1, 0); got != defaultRateLimitBackoff {
		t.Errorf("budget=0 且 rlHits>0 base = %v, want 5s", got)
	}
}

func TestOverWaitLimitPredicate(t *testing.T) {
	hourly := rateLimitErr(2 * time.Hour)
	if !overWaitLimit(hourly, 0) {
		t.Error("2h 等待应判定为超限")
	}
	if overWaitLimit(rateLimitErr(0), 0) {
		t.Error("无 Retry-After 不应判定超限")
	}
	if overWaitLimit(&provider.StreamInterruptedError{Kind: provider.InterruptNetwork}, 3) {
		t.Error("网络错误不应判定超限")
	}
	if overWaitLimit(errors.New("plain"), 0) {
		t.Error("非断流错误不应判定超限")
	}
}

// budgetExhaustedSwitchesBase 429 连续命中超出 budget 后，退避从 1s 切到 5s。
// budget=0 + retries=2：attempt 0 用 1s，attempt 1 时 rlHits=1 > budget=0
// 触发 5s 基础退避（5s << 1 = 10s），总等待 1+10=11s，用 15s 自定义超时。
func TestBudgetExhaustedSwitchesBase(t *testing.T) {
	fp := &fakeProvider{
		syncErrs: []error{
			rateLimitErr(0), // attempt 0: rlHits=0，base=1s
			rateLimitErr(0), // attempt 1: rlHits=1 > budget=0（耗尽），base=5s
			rateLimitErr(0), // attempt 2: attempt=2 >= retries=2 → StopError
		},
	}
	fx := newFixture(t, fp)
	fx.ag.retries = 2
	fx.ag.rateLimitBudget = 0
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEventsTimeout(t, events, 15*time.Second, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopError
	})

	var statuses []string
	for _, ev := range evs {
		if se, ok := ev.(StatusEvent); ok && strings.Contains(se.Text, "后重连") {
			statuses = append(statuses, se.Text)
		}
	}
	if len(statuses) < 2 {
		t.Fatalf("期望至少 2 条重连状态，got %d: %v", len(statuses), statuses)
	}
	if !strings.Contains(statuses[0], "1s 后重连") {
		t.Errorf("首次退避应为 1s，got %q", statuses[0])
	}
	if !strings.Contains(statuses[1], "10s 后重连") {
		t.Errorf("budget 耗尽后退避应切换到 5s 起步（5s<<1=10s），got %q", statuses[1])
	}
}

// budgetExhaustedFastTrackSuccess 预算耗尽后切换到 5s 退避，但仍能在 retries 内成功。
// budget=0 + retries=1：attempt 0 用 1s，attempt 1 时 budget 耗尽用 5s，
// 但 attempt=1 >= retries=1 → StopError（不等待 5s）。
// 改用 budget=1 + retries=2：attempt 0/1 用 1s 基础（rlHits=1 未超限），
// attempt 2 时 rlHits=2 > budget=1（耗尽），base=5s，但 attempt=2 >= retries=2 → StopError。
// 总等待 1+2=3s，在 drainEvents 超时内完成。
func TestBudgetExhaustedFastTrackSuccess(t *testing.T) {
	fp := &fakeProvider{
		syncErrs: []error{
			rateLimitErr(0), // attempt 0: rlHits=0，base=1s
			rateLimitErr(0), // attempt 1: rlHits=1（budget=1，未超限），base=1s
			rateLimitErr(0), // attempt 2: rlHits=2 > budget=1（耗尽），attempt=2 >= retries=2 → StopError
		},
	}
	fx := newFixture(t, fp)
	fx.ag.retries = 2
	fx.ag.rateLimitBudget = 1
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopError
	})

	var statuses []string
	for _, ev := range evs {
		if se, ok := ev.(StatusEvent); ok && strings.Contains(se.Text, "后重连") {
			statuses = append(statuses, se.Text)
		}
	}
	if len(statuses) != 2 {
		t.Fatalf("期望 2 条重连状态，got %d: %v", len(statuses), statuses)
	}
	if !strings.Contains(statuses[0], "1s 后重连") {
		t.Errorf("首次退避应为 1s，got %q", statuses[0])
	}
	if !strings.Contains(statuses[1], "2s 后重连") {
		t.Errorf("第二次退避应为 2s，got %q", statuses[1])
	}
}

// nonRateLimitResetsCounter 非 429 断流（网络错误）把 rlHits 清零，
// 之后再次命中 429 不从 budget 顶部继续累加。
func TestNonRateLimitResetsCounter(t *testing.T) {
	fp := &fakeProvider{
		syncErrs: []error{
			rateLimitErr(0),                     // attempt 0: 429，rlHits=1
			&provider.StreamInterruptedError{Kind: provider.InterruptNetwork}, // attempt 1: 网络，rlHits=0
			rateLimitErr(0),                     // attempt 2: 429，rlHits=1（重置后）
			nil,                                 // attempt 3: 成功，落到 streams
		},
		streams: [][]provider.Chunk{textChunks("done")},
	}
	fx := newFixture(t, fp)
	fx.ag.retries = 3
	fx.ag.rateLimitBudget = 2
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, turnEnded)

	// 验证停止原因：成功完成（非 error）。
	var stop string
	var usage *provider.Usage
	for _, ev := range evs {
		if te, ok := ev.(TurnEndedEvent); ok {
			stop = te.StopReason
			usage = te.Usage
		}
	}
	if stop != StopCompleted {
		t.Fatalf("期望 StopCompleted，got %q", stop)
	}
	if usage == nil {
		t.Fatal("期望有 usage")
	}
	if len(fp.calls) != 4 {
		t.Fatalf("provider calls = %d，期望 4（3次重试+1次成功）", len(fp.calls))
	}
}
