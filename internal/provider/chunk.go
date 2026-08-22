package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

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
)

// ErrStalled 是看门狗超时的 cancel cause。
var ErrStalled = errors.New("stream stalled: no chunk within watchdog timeout")

// StreamInterruptedError 流中断；Network/Stall 可在 step 边界重连，
// Protocol 上抛用户，ContextOverflow 触发压缩后重试。
type StreamInterruptedError struct {
	Kind InterruptKind
	Err  error
}

func (e *StreamInterruptedError) Error() string {
	kind := map[InterruptKind]string{
		InterruptNetwork:         "网络错误",
		InterruptStall:           "流停滞",
		InterruptProtocol:        "协议错误",
		InterruptContextOverflow: "上下文溢出",
	}[e.Kind]
	if e.Err != nil {
		return fmt.Sprintf("流中断（%s）：%s", kind, e.Err)
	}
	return fmt.Sprintf("流中断（%s）", kind)
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

func classifyHTTPError(status int, body []byte) error {
	lower := strings.ToLower(string(body))
	for _, m := range overflowMarkers {
		if status == 400 && strings.Contains(lower, m) {
			return &StreamInterruptedError{Kind: InterruptContextOverflow, Err: fmt.Errorf("HTTP %d: %s", status, excerpt(body))}
		}
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
