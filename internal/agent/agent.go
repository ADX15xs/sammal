// Package agent 实现 turn/step 状态机：驱动模型 ↔ 工具直到模型停止调用
// 工具（第 6.2 节）。无 UI，对外唯一接口是事件流。
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sammal/internal/provider"
)

// 事件流类型：core 对外的唯一接口（第 5.3 节），TUI 是订阅者之一。
type Event interface{ eventType() }

type TurnStartedEvent struct{}
type TextDeltaEvent struct{ Text string }
type ReasonDeltaEvent struct{ Text string }
type MessageFinalEvent struct {
	Text        string
	Interrupted bool
	Usage       *provider.Usage
}
type StreamRestartedEvent struct{ Attempt int }
type TurnEndedEvent struct {
	StopReason string // completed | aborted | error
	Usage      *provider.Usage
}
type ErrorEvent struct{ Err error }
type StatusEvent struct{ Text string }

func (TurnStartedEvent) eventType()     {}
func (TextDeltaEvent) eventType()       {}
func (ReasonDeltaEvent) eventType()     {}
func (MessageFinalEvent) eventType()    {}
func (StreamRestartedEvent) eventType() {}
func (TurnEndedEvent) eventType()       {}
func (ErrorEvent) eventType()           {}
func (StatusEvent) eventType()          {}

// StopReason 常量。
const (
	StopCompleted = "completed"
	StopAborted   = "aborted"
	StopError     = "error"
)

const (
	// maxStreamRetries 网络/停滞类断流在 step 边界的重连上限。
	maxStreamRetries = 3
	retryBackoff     = time.Second
	eventsBuffer     = 256
)

// Agent 持有一段对话的模型历史与运行状态。
type Agent struct {
	root   context.Context
	prov   provider.Provider
	model  string
	system string

	history []provider.Message

	events chan Event
	inbox  chan string

	mu         sync.Mutex
	turnCancel context.CancelFunc
}

func New(root context.Context, prov provider.Provider, model, system string) *Agent {
	return &Agent{
		root:   root,
		prov:   prov,
		model:  model,
		system: system,
		events: make(chan Event, eventsBuffer),
		inbox:  make(chan string, 16),
	}
}

// Events 返回事件流订阅端。
func (a *Agent) Events() <-chan Event { return a.events }

// Steering 把生成期间的用户插话排入收件箱；在下一个 step 边界吸收为
// user 消息（不注入进行中的请求）。
func (a *Agent) Steering(text string) {
	select {
	case a.inbox <- text:
	default:
		a.emit(ErrorEvent{Err: errors.New("收件箱已满，插话被丢弃")})
	}
}

// Abort 中止当前生成；无进行中的 turn 时为空操作。
func (a *Agent) Abort() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turnCancel != nil {
		a.turnCancel()
	}
}

// Running 报告是否有 turn 正在进行。
func (a *Agent) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turnCancel != nil
}

// Submit 提交用户输入：空闲时开新 turn；生成中入收件箱，在下一个
// step 边界吸收（第 6.2 节消息收件箱）。
func (a *Agent) Submit(text string) {
	if a.Running() {
		a.Steering(text)
		return
	}
	go a.Run(a.root, text)
}

func (a *Agent) setRunning(cancel context.CancelFunc) {
	a.mu.Lock()
	a.turnCancel = cancel
	a.mu.Unlock()
}

// Run 执行一个 turn：先吸收收件箱中排队的插话（时间序在新消息之前），
// 追加 user 消息，然后 step。M0 无工具，单步终止；结果经事件流透出。
func (a *Agent) Run(parent context.Context, userMsg string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.setRunning(cancel)
	defer a.setRunning(nil)

	a.absorbInbox()
	a.history = append(a.history, provider.Message{Role: "user", Content: userMsg})
	a.emit(TurnStartedEvent{})

	_, usage, err := a.runStep(ctx)
	stopReason := StopCompleted
	switch {
	case errors.Is(err, context.Canceled):
		stopReason = StopAborted
	case err != nil:
		stopReason = StopError
	}
	a.emit(TurnEndedEvent{StopReason: stopReason, Usage: usage})
}

// absorbInbox 清空收件箱：把生成期间的用户插话吸收为 user 消息。
func (a *Agent) absorbInbox() {
	for {
		select {
		case msg := <-a.inbox:
			a.history = append(a.history, provider.Message{Role: "user", Content: msg})
			a.emit(StatusEvent{Text: "已吸收插话，将随下一请求发送"})
		default:
			return
		}
	}
}

// runStep 驱动一个 step：流式请求 → 定稿 assistant 消息。断流（网络/
// 停滞）在 step 边界重连，重连前丢弃本Attempt已产出的部分内容并整段重来。
func (a *Agent) runStep(ctx context.Context) (provider.Message, *provider.Usage, error) {
	var text strings.Builder
	var toolCalls []provider.ToolCall
	var usage *provider.Usage

	for attempt := 0; ; attempt++ {
		text.Reset()
		toolCalls = toolCalls[:0]
		usage = nil

		if attempt > 0 {
			a.emit(StreamRestartedEvent{Attempt: attempt})
		}

		req := provider.Request{
			Model:    a.model,
			System:   a.system,
			Messages: append([]provider.Message(nil), a.history...),
		}
		ch, err := a.prov.Stream(ctx, req)
		if err != nil {
			if retryable(err) && attempt < maxStreamRetries {
				if !a.retryPause(ctx, err, attempt) {
					return a.finalizePartial(text.String(), toolCalls), nil, context.Canceled
				}
				continue
			}
			if errors.Is(err, context.Canceled) {
				return a.finalizePartial(text.String(), toolCalls), nil, context.Canceled
			}
			a.emit(ErrorEvent{Err: err})
			return a.finalizePartial(text.String(), toolCalls), nil, err
		}

		var streamErr error
	consume:
		for ck := range ch {
			switch {
			case ck.Err != nil:
				streamErr = ck.Err
				break consume
			case ck.TextDelta != "":
				text.WriteString(ck.TextDelta)
				a.emit(TextDeltaEvent{Text: ck.TextDelta})
			case ck.ReasonDelta != "":
				a.emit(ReasonDeltaEvent{Text: ck.ReasonDelta})
			case ck.ToolCallDelta != nil:
				toolCalls = appendToolDelta(toolCalls, ck.ToolCallDelta)
			case ck.Usage != nil:
				usage = ck.Usage
			}
		}

		if streamErr == nil {
			// 兜底：无错误帧而流静默关闭（如连接恰在中止瞬间断开），
			// 由 agent 自身的 ctx 状态判定中止。
			if ctx.Err() != nil {
				streamErr = ctx.Err()
			} else {
				msg := provider.Message{Role: "assistant", Content: text.String(), ToolCalls: toolCalls}
				a.history = append(a.history, msg)
				a.emit(MessageFinalEvent{Text: text.String(), Interrupted: false, Usage: usage})
				return msg, usage, nil
			}
		}
		if retryable(streamErr) && attempt < maxStreamRetries {
			if !a.retryPause(ctx, streamErr, attempt) {
				return a.finalizePartial(text.String(), toolCalls), nil, context.Canceled
			}
			continue
		}
		if errors.Is(streamErr, context.Canceled) {
			return a.finalizePartial(text.String(), toolCalls), nil, context.Canceled
		}
		a.emit(ErrorEvent{Err: streamErr})
		return a.finalizePartial(text.String(), toolCalls), nil, streamErr
	}
}

func (a *Agent) retryPause(ctx context.Context, err error, attempt int) bool {
	a.emit(StatusEvent{Text: fmt.Sprintf("流中断：%v；%s 后重连（%d/%d）", err, retryBackoff, attempt+1, maxStreamRetries)})
	t := time.NewTimer(retryBackoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// finalizePartial 把中止/中断时的部分产出定稿为 interrupted 的 assistant
// 消息（I1/I3 的中止语义：模型见过的必须可重放）。无产出则不留痕。
func (a *Agent) finalizePartial(text string, toolCalls []provider.ToolCall) provider.Message {
	if text == "" && len(toolCalls) == 0 {
		return provider.Message{}
	}
	msg := provider.Message{Role: "assistant", Content: text, ToolCalls: toolCalls}
	a.history = append(a.history, msg)
	a.emit(MessageFinalEvent{Text: text, Interrupted: true, Usage: nil})
	return msg
}

func retryable(err error) bool {
	var se *provider.StreamInterruptedError
	return errors.As(err, &se) &&
		(se.Kind == provider.InterruptNetwork || se.Kind == provider.InterruptStall)
}

func appendToolDelta(calls []provider.ToolCall, d *provider.ToolCallDelta) []provider.ToolCall {
	for d.Index >= len(calls) {
		calls = append(calls, provider.ToolCall{Type: provider.ToolTypeFunction})
	}
	c := &calls[d.Index]
	if d.ID != "" {
		c.ID = d.ID
	}
	if d.Name != "" {
		c.Function.Name = d.Name
	}
	c.Function.Arguments += d.ArgsDelta
	return calls
}

func (a *Agent) emit(ev Event) {
	a.events <- ev
}
