// Package provider 把 OpenAI 兼容的 SSE 流式响应转成规范化的 Chunk 流，
// 聚合 tool_calls 增量，并带 SSE 停滞看门狗（第 6.1 节）。
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultStallTimeout 看门狗默认 300s 无 chunk 判定停滞。
const DefaultStallTimeout = 300 * time.Second

// Provider 引入理由：httptest 假服务器与 agent 测试替身是第二个当前实现，
// 不是为未来协议预留。
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

// Client 是 OpenAI 兼容端点的流式客户端。零值可用前请先设置 BaseURL。
type Client struct {
	BaseURL      string
	APIKey       string // 可空：本地端点无需鉴权
	HTTP         *http.Client
	StallTimeout time.Duration
}

// NewClient 构造指向 base_url 的客户端；apiKey 来自 api_key_env 解析，可为空。
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		APIKey:       apiKey,
		HTTP:         &http.Client{},
		StallTimeout: DefaultStallTimeout,
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) stallTimeout() time.Duration {
	if c.StallTimeout > 0 {
		return c.StallTimeout
	}
	return DefaultStallTimeout
}

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content          string              `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			Reasoning        string              `json:"reasoning"`
			ToolCalls        []wireToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
}

type wireToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireUsage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Stream 发起流式补全。连接阶段的错误同步返回；流中途的失败以
// Chunk{Err} 作为最后一帧送达后关流。
func (c *Client) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	body, err := MarshalRequest(req)
	if err != nil {
		return nil, fmt.Errorf("构造请求体失败：%w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// 看门狗：无 chunk 超时则 cancel 流 ctx，读端据此分类为停滞。
	sctx, cancel := context.WithCancelCause(ctx)
	httpReq = httpReq.WithContext(sctx)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		cancel(nil)
		if isAbort(ctx, err) {
			return nil, context.Canceled
		}
		return nil, &StreamInterruptedError{Kind: InterruptNetwork, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		cancel(nil)
		return nil, classifyHTTPError(resp.StatusCode, errBody)
	}

	ch := make(chan Chunk, 16)
	go c.pump(sctx, cancel, ctx, resp.Body, ch)
	return ch, nil
}

func (c *Client) pump(sctx context.Context, cancel context.CancelCauseFunc, parent context.Context, body io.ReadCloser, ch chan<- Chunk) {
	defer close(ch)
	defer body.Close()
	defer cancel(nil)

	var lastChunkNs atomic.Int64
	lastChunkNs.Store(time.Now().UnixNano())
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastChunkNs.Load())) > c.stallTimeout() {
					cancel(ErrStalled)
					return
				}
			}
		}
	}()

	send := func(ck Chunk) bool {
		lastChunkNs.Store(time.Now().UnixNano())
		select {
		case ch <- ck:
			return true
		case <-sctx.Done():
			return false
		}
	}

	// 终止帧（流中断错误）用非阻塞投递：此刻 sctx 可能已被取消，
	// select 会在「缓冲可写」与「Done」间随机选择；消费方仍在排空，
	// 缓冲有位即可确定性送达。
	sendFinal := func(err error) {
		select {
		case ch <- Chunk{Err: err}:
		default:
		}
	}

	err := scanSSE(body, func(data []byte) bool {
		if string(data) == "[DONE]" {
			return false
		}
		var wc wireChunk
		if err := json.Unmarshal(data, &wc); err != nil {
			sendFinal(&StreamInterruptedError{Kind: InterruptProtocol, Err: fmt.Errorf("非法 SSE 数据 %q：%w", excerpt(data), err)})
			return false
		}
		return emitWireChunk(wc, send)
	})
	// 流终止的三种形态：[DONE] 正常结束（err==nil）；读体错误（分类为
	// 中断）；用户中止（parent 已取消，此时 scanSSE 可能因 send 提前
	// 退出而返回 nil，与正常结束不可区分，须在此显式补发取消帧）。
	if err != nil {
		if sendErr := classifyStreamErr(parent, sctx, err); sendErr != nil {
			sendFinal(sendErr)
		}
	} else if parent.Err() != nil {
		sendFinal(parent.Err())
	}
	cancel(nil)
	<-watchDone
}

// classifyStreamErr 把读体错误映射为流中断错误；用户中止返回 parent 的
// 取消错误（agent 侧按 context.Canceled 识别）。
func classifyStreamErr(parent, sctx context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(context.Cause(sctx), ErrStalled) {
		return &StreamInterruptedError{Kind: InterruptStall}
	}
	if isNetworkErr(err) {
		return &StreamInterruptedError{Kind: InterruptNetwork, Err: err}
	}
	return &StreamInterruptedError{Kind: InterruptProtocol, Err: err}
}

func emitWireChunk(wc wireChunk, send func(Chunk) bool) bool {
	for _, choice := range wc.Choices {
		reason := choice.Delta.ReasoningContent
		if reason == "" {
			reason = choice.Delta.Reasoning
		}
		if choice.Delta.Content != "" || reason != "" {
			ck := Chunk{TextDelta: choice.Delta.Content, ReasonDelta: reason}
			if choice.FinishReason != nil {
				ck.FinishReason = *choice.FinishReason
			}
			if !send(ck) {
				return false
			}
		}
		for _, d := range choice.Delta.ToolCalls {
			ck := Chunk{ToolCallDelta: &ToolCallDelta{
				Index:     d.Index,
				ID:        d.ID,
				Name:      d.Function.Name,
				ArgsDelta: d.Function.Arguments,
			}}
			if choice.FinishReason != nil {
				ck.FinishReason = *choice.FinishReason
			}
			if !send(ck) {
				return false
			}
		}
		if len(choice.Delta.ToolCalls) == 0 && choice.Delta.Content == "" && reason == "" && choice.FinishReason != nil {
			if !send(Chunk{FinishReason: *choice.FinishReason}) {
				return false
			}
		}
	}
	if wc.Usage != nil {
		u := Usage{
			PromptTokens:          wc.Usage.PromptTokens,
			CompletionTokens:      wc.Usage.CompletionTokens,
			PromptCacheHitTokens:  wc.Usage.PromptCacheHitTokens,
			PromptCacheMissTokens: wc.Usage.PromptCacheMissTokens,
		}
		if wc.Usage.PromptTokensDetails != nil {
			u.CachedTokens = wc.Usage.PromptTokensDetails.CachedTokens
		}
		if !send(Chunk{Usage: &u}) {
			return false
		}
	}
	return true
}

// scanSSE 按 SSE 帧解析：data 行累积，空行触发回调。返回 false 停止。
func scanSSE(r io.Reader, onEvent func(data []byte) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var data [][]byte
	flush := func() bool {
		if len(data) == 0 {
			return true
		}
		joined := bytes.Join(data, []byte{'\n'})
		data = data[:0]
		return onEvent(joined)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, bytes.TrimPrefix([]byte(v), []byte{' '}))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(data) > 0 {
		// 少数端点省略收尾空行；flush 剩余数据。
		if !flush() {
			return nil
		}
	}
	// 干净 EOF 但未见 [DONE]：按异常断流处理，交给上层分类。
	return io.ErrUnexpectedEOF
}
