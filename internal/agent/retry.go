// Package agent 重试与限流退避：指数退避、限流探测、断流中断处理（搬迁自 agent.go，行为不变）。
package agent

import (
	"context"
	"errors"
	"fmt"

	"sammal/internal/human"
	"sammal/internal/provider"
	"time"
)

const (
	// retryBackoff/retryCap 重连退避曲线：1s 起指数增长，单次等待上限 60s。
	// 要求等待超过上限即订阅 plan 的用量窗口（小时级），走快速失败而非重试。
	retryBackoff = time.Second
	retryCap     = time.Minute
	// rateLimitBackoff 是 429 连续命中超过 rateLimitBudget 后的基础退避：
	// 5s 起步的指数曲线（5→10→20→40s）覆盖大多数分钟级限流窗口。
	rateLimitBackoff = 5 * time.Second
	// rateLimitBudget 同一 step 内连续 429 的宽容次数，内置不可配置。
	rateLimitBudget = 1
	// defaultRetries 断流重连上限，内置不可配置：预算耗尽后的 429 曲线
	// 总耐心 131s，覆盖分钟级限流窗口；端点宕机约 2 分钟内上抛错误。
	defaultRetries = 5
	eventsBuffer   = 256
)

// handleInterrupt 处理一次断流，返回 (继续下一 attempt, 终止错误)，恰一
// 个有效。优先级：用户中止 > 用量窗口快速失败 > 预算内重连 > 上抛。
// rlHits 是当前 step 内连续 429 的次数（调用方已更新）；超过 rateLimitBudget
// 后退避切换 5s 起步。
func (a *Agent) handleInterrupt(ctx context.Context, err error, attempt int, rlHits int) (bool, error) {
	if ctx.Err() != nil {
		return false, context.Canceled
	}
	if overWaitLimit(err, attempt) {
		wait := max(retryBackoff<<attempt, retryAfterOf(err))
		a.emit(ErrorEvent{Err: fmt.Errorf("%v；超过单次等待上限 %s，停止自动重试：预计 %s 前后可恢复（会话已保留，稍后重发即可）",
			err, retryCap, time.Now().Add(wait).Format("15:04"))})
		return false, err
	}
	if retryable(err) && attempt < a.retries {
		base := backoffBaseFor(rlHits)
		if !a.retryPause(ctx, err, attempt, base) {
			return false, context.Canceled
		}
		return true, nil
	}
	a.emit(ErrorEvent{Err: err})
	return false, err
}

func (a *Agent) retryPause(ctx context.Context, err error, attempt int, base time.Duration) bool {
	d := backoffFor(attempt, retryAfterOf(err), base)
	a.emit(StatusEvent{Text: fmt.Sprintf("流中断：%v；%s 后重连（%d/%d）", err, human.Duration(d), attempt+1, a.retries)})
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// backoffBaseFor 返回本次重试的基础退避间隔：同一 step 内连续 429 超过
// rateLimitBudget 次后从 1s 提升到 5s，覆盖大多数分钟级限流窗口。
func backoffBaseFor(rlHits int) time.Duration {
	if rlHits > rateLimitBudget {
		return rateLimitBackoff
	}
	return retryBackoff
}

// backoffFor 实际等待时长：基础退避与端点要求取大者，截断到单次等待上限。
func backoffFor(attempt int, retryAfter time.Duration, base time.Duration) time.Duration {
	return min(max(base<<attempt, retryAfter), retryCap)
}

// overWaitLimit 报告端点要求的等待超出单次上限（订阅 plan 的用量窗口以
// 小时计）：继续循环重试无意义，应立即上报恢复时间点。
func overWaitLimit(err error, attempt int) bool {
	ra := retryAfterOf(err)
	return ra > 0 && max(retryBackoff<<attempt, ra) > retryCap
}

func retryAfterOf(err error) time.Duration {
	var se *provider.StreamInterruptedError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

func retryable(err error) bool {
	var se *provider.StreamInterruptedError
	return errors.As(err, &se) &&
		(se.Kind == provider.InterruptNetwork || se.Kind == provider.InterruptStall ||
			se.Kind == provider.InterruptRateLimit || se.Kind == provider.InterruptServerError)
}

func isRateLimit(err error) bool {
	var se *provider.StreamInterruptedError
	return errors.As(err, &se) && se.Kind == provider.InterruptRateLimit
}
