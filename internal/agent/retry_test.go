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
		want       time.Duration
	}{
		{"首试盲退避1s", 0, 0, time.Second},
		{"二次2s", 1, 0, 2 * time.Second},
		{"三次4s", 2, 0, 4 * time.Second},
		{"指数增长32s", 5, 0, 32 * time.Second},
		{"64s截断到上限", 6, 0, retryCap},
		{"尊重端点30s", 0, 30 * time.Second, 30 * time.Second},
		{"端点要求90s截断到上限", 0, 90 * time.Second, retryCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoffFor(tt.attempt, tt.retryAfter); got != tt.want {
				t.Errorf("backoffFor(%d, %v) = %v, want %v", tt.attempt, tt.retryAfter, got, tt.want)
			}
		})
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
