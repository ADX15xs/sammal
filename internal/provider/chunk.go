package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sammal/internal/human"
)

// parseRetryAfter 把限流重试信号头解析为时长；无法识别一律返回 0。
// 认识标准 Retry-After（整数秒 / HTTP-date）与常见 x-ratelimit-reset
// （unix 秒 / RFC3339）；其余变体格式繁多，不做脆弱解析（DEBT）。
func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	var d time.Duration
	if v := header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			d = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			d = t.Sub(now)
		}
		return clampFuture(d)
	}
	if v := header.Get("X-Ratelimit-Reset"); v != "" {
		v = strings.TrimSpace(v)
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			d = time.Unix(secs, 0).Sub(now)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			d = t.Sub(now)
		}
		return clampFuture(d)
	}
	return 0
}

// clampFuture 只保留「未来」语义的等待：过去时点视为可立即重试。
func clampFuture(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// Chunk 是规范化后的流式增量。Err 非空的 Chunk 是最后一帧，标志流中断。
type Chunk struct {
	TextDelta     string
	ReasonDelta   string // 兼容 reasoning_content / reasoning 双字段
	ToolCallDelta *ToolCallDelta
	Usage         *Usage // finish 前保证送达
	FinishReason  string
	Err           error
}

// ToolCallDelta 按 index 聚合的工具调用增量；ID/Name 仅首个分片携带。
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	ArgsDelta string
}

// Usage 透出缓存命中字段（I2 的可观测出口）：
// DeepSeek 系 prompt_cache_hit_tokens，OpenAI 系 prompt_tokens_details.cached_tokens。
type Usage struct {
	PromptTokens          int
	CompletionTokens      int
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
	CachedTokens          int
}

// CacheHitTokens 返回该端点透出的缓存命中数（无则 0）。
func (u *Usage) CacheHitTokens() int {
	if u == nil {
		return 0
	}
	if u.PromptCacheHitTokens > 0 {
		return u.PromptCacheHitTokens
	}
	return u.CachedTokens
}

// CacheHitRatio 返回命中比例；无缓存数据时返回 -1（界面显示"-"）。
func (u *Usage) CacheHitRatio() float64 {
	if u == nil {
		return -1
	}
	if hit := u.CacheHitTokens(); hit > 0 {
		return float64(hit) / float64(u.PromptTokens)
	}
	if u.PromptCacheHitTokens == 0 && u.CachedTokens == 0 && u.PromptCacheMissTokens > 0 {
		return 0
	}
	return -1
}

// InterruptKind 断流分类，决定 agent 侧的重连策略。
type InterruptKind int

const (
	InterruptNetwork InterruptKind = iota
	InterruptStall
	InterruptProtocol
	InterruptContextOverflow
	InterruptRateLimit   // HTTP 429：瞬时限流，可短等待重试
	InterruptServerError // HTTP 5xx：服务端瞬断，可重试
	InterruptQuota       // 429 且命中配额特征（订阅 plan 用量窗口耗尽）：等待无意义
)

// ErrStalled 是看门狗超时的 cancel cause。
var ErrStalled = errors.New("stream stalled: no chunk within watchdog timeout")

// StreamInterruptedError 流中断；Network/Stall/RateLimit/ServerError 可在
// step 边界重连，Protocol/Quota 上抛用户，ContextOverflow 触发压缩后重试。
// RetryAfter 是端点要求等待的时长（0 = 未告知），仅限流类携带。
type StreamInterruptedError struct {
	Kind       InterruptKind
	Err        error
	RetryAfter time.Duration
}

func (e *StreamInterruptedError) Error() string {
	kind := map[InterruptKind]string{
		InterruptNetwork:         "网络错误",
		InterruptStall:           "流停滞",
		InterruptProtocol:        "协议错误",
		InterruptContextOverflow: "上下文溢出",
		InterruptRateLimit:       "限流",
		InterruptServerError:     "服务端错误",
		InterruptQuota:           "配额窗口",
	}[e.Kind]
	msg := fmt.Sprintf("流中断（%s）", kind)
	if e.Err != nil {
		msg += fmt.Sprintf("：%s", e.Err)
	}
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf("；要求等待 %s 后重试", human.Duration(e.RetryAfter))
	}
	return msg
}

func (e *StreamInterruptedError) Unwrap() error { return e.Err }

// IsContextOverflow 报告 err 是否为上下文溢出类错误。
func IsContextOverflow(err error) bool {
	var se *StreamInterruptedError
	return errors.As(err, &se) && se.Kind == InterruptContextOverflow
}

// overflowMarkers 常见 OpenAI 兼容端点对超长输入的 400 响应特征。
var overflowMarkers = []string{
	"context length", "context_length_exceeded", "maximum context",
	"too long", "exceeds the maximum",
}

// quotaMarkers 订阅制（coding/token plan）端点用量窗口耗尽的 429 响应特征。
var quotaMarkers = []string{
	"usage limit", "usage_limit", "limit reached", "quota exceeded",
}

func classifyHTTPError(status int, body []byte, retryAfter time.Duration) error {
	lower := strings.ToLower(string(body))
	for _, m := range overflowMarkers {
		if status == 400 && strings.Contains(lower, m) {
			return &StreamInterruptedError{Kind: InterruptContextOverflow, Err: fmt.Errorf("HTTP %d: %s", status, excerpt(body))}
		}
	}
	if status == http.StatusTooManyRequests {
		for _, m := range quotaMarkers {
			if strings.Contains(lower, m) {
				return &StreamInterruptedError{Kind: InterruptQuota, Err: fmt.Errorf("HTTP %d: %s", status, excerpt(body)), RetryAfter: retryAfter}
			}
		}
		return &StreamInterruptedError{Kind: InterruptRateLimit, Err: fmt.Errorf("HTTP %d: %s", status, excerpt(body)), RetryAfter: retryAfter}
	}
	if status >= 500 {
		return &StreamInterruptedError{Kind: InterruptServerError, Err: fmt.Errorf("HTTP %d: %s", status, excerpt(body)), RetryAfter: retryAfter}
	}
	return &StreamInterruptedError{Kind: InterruptProtocol, Err: fmt.Errorf("HTTP %d: %s", status, excerpt(body))}
}

func excerpt(b []byte) string {
	const max = 200
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}

// isAbort 报告 err 是否为用户主动中止（外层 ctx 取消）。
func isAbort(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}

// isNetworkErr 报告 err 是否为传输层错误（连接重置、意外 EOF、DNS 等）。
func isNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF)
}
