// Package agent 实现 turn/step 状态机：驱动模型 ↔ 工具直到模型停止调用
// 工具（第 6.2 节）。无 UI，对外唯一接口是事件流；一切模型可见内容
// 落 session 日志（I1）。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sammal/internal/checkpoint"
	"sammal/internal/provider"
	"sammal/internal/session"
	"sammal/internal/tool"
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
type ToolCallEvent struct {
	ID          string
	Name        string
	ArgsSummary string
}
type ToolResultEvent struct {
	ID     string
	Name   string
	Result tool.Result
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
func (ToolCallEvent) eventType()        {}
func (ToolResultEvent) eventType()      {}
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

// Config 装配 Agent 的全部依赖。
type Config struct {
	Root        context.Context
	Provider    provider.Provider
	Session     *session.Session
	Registry    *tool.Registry
	Checkpoints *checkpoint.Store
	System      string
}

type Agent struct {
	root   context.Context
	prov   provider.Provider
	sess   *session.Session
	reg    *tool.Registry
	cp     *checkpoint.Store
	system string

	events chan Event
	inbox  chan string

	gitHintShown bool

	mu         sync.Mutex
	turnCancel context.CancelFunc
}

func New(cfg Config) *Agent {
	return &Agent{
		root:   cfg.Root,
		prov:   cfg.Provider,
		sess:   cfg.Session,
		reg:    cfg.Registry,
		cp:     cfg.Checkpoints,
		system: cfg.System,
		events: make(chan Event, eventsBuffer),
		inbox:  make(chan string, 16),
	}
}

func (a *Agent) Model() string             { return a.sess.Header().Model }
func (a *Agent) Events() <-chan Event      { return a.events }
func (a *Agent) Session() *session.Session { return a.sess }

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

func (a *Agent) setRunning(cancel context.CancelFunc) {
	a.mu.Lock()
	a.turnCancel = cancel
	a.mu.Unlock()
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

// Run 执行一个 turn：吸收收件箱中排队的插话（时间序在新消息之前），
// 追加 user 消息，然后 step 循环（流式 → 工具 → 流式 …）直到模型
// 不再调用工具。所有模型可见内容落日志（I1）。
func (a *Agent) Run(parent context.Context, userMsg string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.setRunning(cancel)
	defer a.setRunning(nil)

	a.absorbInbox()
	if err := a.sess.Append(session.TypeUserMessage, session.UserMessageData{Text: userMsg}); err != nil {
		a.emit(ErrorEvent{Err: fmt.Errorf("日志写入失败：%w", err)})
		return
	}
	a.emit(TurnStartedEvent{})

	stopReason, usage := a.runSteps(ctx)
	if err := a.sess.EndTurn(stopReason); err != nil {
		a.emit(ErrorEvent{Err: fmt.Errorf("日志写入失败：%w", err)})
	}
	a.emit(TurnEndedEvent{StopReason: stopReason, Usage: usage})
}

func (a *Agent) runSteps(ctx context.Context) (string, *provider.Usage) {
	for step := 0; ; step++ {
		a.absorbInbox()

		req := provider.Request{
			Model:    a.sess.Header().Model,
			System:   a.system,
			Messages: a.sess.DeriveMessages(),
			Tools:    a.reg.Defs(),
		}
		hash, err := provider.PrefixHash(req)
		if err != nil {
			a.emit(ErrorEvent{Err: err})
			return StopError, nil
		}
		if err := a.sess.Append(session.TypeRequestHeader, session.RequestHeaderData{
			PrefixHash: hash, MessageCount: len(req.Messages), Model: req.Model,
		}); err != nil {
			a.emit(ErrorEvent{Err: fmt.Errorf("日志写入失败：%w", err)})
			return StopError, nil
		}

		msg, toolCalls, usage, err := a.streamStep(ctx, req)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return StopAborted, usage
			}
			return StopError, usage
		}
		if len(toolCalls) == 0 {
			return StopCompleted, usage
		}
		if aborted := a.executeTools(ctx, msg.ToolCalls); aborted {
			return StopAborted, usage
		}
	}
}

// absorbInbox 清空收件箱：把生成期间的用户插话吸收为 user 消息。
func (a *Agent) absorbInbox() {
	for {
		select {
		case msg := <-a.inbox:
			a.sess.Append(session.TypeUserMessage, session.UserMessageData{Text: msg})
			a.emit(StatusEvent{Text: "已吸收插话，将随下一请求发送"})
		default:
			return
		}
	}
}

// streamStep 驱动一次流式请求：日志留痕 chunk 增量与定稿
// assistant/message。断流（网络/停滞）在 step 边界重连。
func (a *Agent) streamStep(ctx context.Context, req provider.Request) (provider.Message, []provider.ToolCall, *provider.Usage, error) {
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

		ch, err := a.prov.Stream(ctx, req)
		if err != nil {
			if retryable(err) && attempt < maxStreamRetries {
				if !a.retryPause(ctx, err, attempt) {
					return a.finalizePartial(text.String(), toolCalls), toolCalls, usage, context.Canceled
				}
				continue
			}
			if errors.Is(err, context.Canceled) {
				return a.finalizePartial(text.String(), toolCalls), toolCalls, usage, context.Canceled
			}
			a.emit(ErrorEvent{Err: err})
			return a.finalizePartial(text.String(), toolCalls), toolCalls, usage, err
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
				a.sess.Append(session.TypeAssistantChunk, session.AssistantChunkData{Delta: ck.TextDelta})
			case ck.ReasonDelta != "":
				a.emit(ReasonDeltaEvent{Text: ck.ReasonDelta})
			case ck.ToolCallDelta != nil:
				toolCalls = appendToolDelta(toolCalls, ck.ToolCallDelta)
			case ck.Usage != nil:
				usage = ck.Usage
			}
		}

		if streamErr == nil {
			// 兜底：无错误帧而流静默关闭时，由 agent 自身 ctx 判定中止。
			if ctx.Err() != nil {
				streamErr = ctx.Err()
			} else {
				msg := provider.Message{Role: "assistant", Content: text.String(), ToolCalls: toolCalls}
				if err := a.sess.Append(session.TypeAssistantMessage, session.AssistantMessageData{
					Text: text.String(), ToolCalls: toolCalls,
				}); err != nil {
					a.emit(ErrorEvent{Err: fmt.Errorf("日志写入失败：%w", err)})
					return msg, toolCalls, usage, err
				}
				a.emit(MessageFinalEvent{Text: text.String(), Interrupted: false, Usage: usage})
				return msg, toolCalls, usage, nil
			}
		}
		if retryable(streamErr) && attempt < maxStreamRetries {
			if !a.retryPause(ctx, streamErr, attempt) {
				return a.finalizePartial(text.String(), toolCalls), toolCalls, usage, context.Canceled
			}
			continue
		}
		if errors.Is(streamErr, context.Canceled) {
			return a.finalizePartial(text.String(), toolCalls), toolCalls, usage, context.Canceled
		}
		a.emit(ErrorEvent{Err: streamErr})
		return a.finalizePartial(text.String(), toolCalls), toolCalls, usage, streamErr
	}
}

// finalizePartial 把中止/中断时的部分产出定稿为 interrupted 的 assistant
// 消息（I1/I3 的中止语义：模型见过的必须可重放）。无产出则不留痕。
func (a *Agent) finalizePartial(text string, toolCalls []provider.ToolCall) provider.Message {
	if text == "" && len(toolCalls) == 0 {
		return provider.Message{}
	}
	msg := provider.Message{Role: "assistant", Content: text, ToolCalls: toolCalls}
	a.sess.Append(session.TypeAssistantMessage, session.AssistantMessageData{
		Text: text, ToolCalls: toolCalls, Interrupted: true,
	})
	a.emit(MessageFinalEvent{Text: text, Interrupted: true, Usage: nil})
	return msg
}

// executeTools 顺序执行工具调用（v1 不并行）；每个 call/result 落日志。
// 中止时未执行的调用写入合成错误结果（"aborted before execution"），
// 保证 I1/I3 可重放。返回是否被中止。
func (a *Agent) executeTools(ctx context.Context, calls []provider.ToolCall) bool {
	for _, tc := range calls {
		if ctx.Err() != nil {
			a.logToolResult(session.ToolResultData{
				ID:        tc.ID,
				Canonical: tool.Result{Err: "aborted before execution"},
				Synthetic: true,
			}, tc.Function.Name)
			continue
		}

		args := json.RawMessage(tc.Function.Arguments)
		a.sess.Append(session.TypeToolCall, session.ToolCallData{ID: tc.ID, Name: tc.Function.Name, Args: args})
		a.emit(ToolCallEvent{ID: tc.ID, Name: tc.Function.Name, ArgsSummary: argsSummary(args)})

		res := a.runTool(ctx, tc.Function.Name, args)
		a.logToolResult(session.ToolResultData{ID: tc.ID, Canonical: res}, tc.Function.Name)
	}
	return ctx.Err() != nil
}

func (a *Agent) runTool(ctx context.Context, name string, args json.RawMessage) tool.Result {
	tl, ok := a.reg.Get(name)
	if !ok {
		return tool.Result{Err: fmt.Sprintf("unknown tool %q", name)}
	}
	a.captureBeforeWrite(tl, args)
	res, err := tl.Execute(ctx, args)
	if err != nil {
		return tool.Result{Err: fmt.Sprintf("tool infrastructure error: %v", err)}
	}
	return res
}

func (a *Agent) logToolResult(d session.ToolResultData, name string) {
	a.sess.Append(session.TypeToolResult, d)
	a.emit(ToolResultEvent{ID: d.ID, Name: name, Result: d.Canonical})
}

// captureBeforeWrite 在写类工具首次落盘前捕获快照；工作目录不是 git
// 仓库时发出一次性建议（第 6.4 节）。
func (a *Agent) captureBeforeWrite(tl tool.Tool, args json.RawMessage) {
	if tl.ReadOnly() || a.cp == nil {
		return
	}
	var arg struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &arg) != nil || arg.Path == "" {
		return
	}
	first, err := a.cp.CaptureBeforeWrite(a.sess.Turn(), arg.Path)
	if err != nil {
		a.emit(StatusEvent{Text: fmt.Sprintf("快照捕获失败（%v），本次写入不可 rewind", err)})
		return
	}
	if first && !a.gitHintShown && !insideGitRepo(a.sess.Header().Cwd) {
		a.gitHintShown = true
		a.emit(StatusEvent{Text: "工作目录不是 git 仓库：建议 git init 获得完整兜底（/rewind 仅回滚文件写操作，不含 bash 副作用）"})
	}
}

func insideGitRepo(dir string) bool {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func argsSummary(args json.RawMessage) string {
	s := string(args)
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// Rewind 同时回滚（a）快照覆盖的文件（b）会话日志截断到该 turn 之前
// （第 6.4 节）。生成中不可用。
func (a *Agent) Rewind(turn int) (string, error) {
	if a.Running() {
		return "", errors.New("生成中不能 rewind，请先 Esc 中止")
	}
	if turn < 1 || turn >= a.sess.Turn() {
		return "", fmt.Errorf("turn %d 不存在（当前可回滚范围 1..%d）", turn, a.sess.Turn()-1)
	}
	n, err := a.cp.RewindToBefore(turn)
	if err != nil {
		return "", fmt.Errorf("文件回滚失败：%w", err)
	}
	a.cp.ForgetFrom(turn)
	if err := a.sess.TruncateBeforeTurn(turn); err != nil {
		return "", fmt.Errorf("日志截断失败：%w", err)
	}
	return fmt.Sprintf("已回滚 turn %d：恢复 %d 个文件，会话日志截断到该 turn 之前（bash 副作用不在回滚范围）", turn, n), nil
}

// Slash 处理 slash 命令，返回给用户看的输出行（第 8.2 节最小命令集）。
func (a *Agent) Slash(input string) []string {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "/help":
		return []string{
			"/rewind [n]   回滚代码与对话到 turn n 之前；无参列出可回滚的 turn",
			"/help         命令自述",
		}
	case "/rewind":
		if len(fields) == 1 {
			return a.listRewindable()
		}
		var n int
		if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil {
			return []string{"用法：/rewind <turn-n>"}
		}
		msg, err := a.Rewind(n)
		if err != nil {
			return []string{"rewind 失败：" + err.Error()}
		}
		return []string{msg}
	default:
		return []string{fmt.Sprintf("未知命令 %s，/help 查看可用命令", fields[0])}
	}
}

func (a *Agent) listRewindable() []string {
	turns, err := a.cp.Turns()
	if err != nil || len(turns) == 0 {
		return []string{"没有可回滚的快照（写文件后才会产生）"}
	}
	lines := []string{"可回滚的 turn（/rewind <n>）："}
	for _, t := range turns {
		lines = append(lines, fmt.Sprintf("  turn %d", t))
	}
	return lines
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
