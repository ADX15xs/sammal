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
type ModelSwitchedEvent struct{ Name string }

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
func (ModelSwitchedEvent) eventType()   {}

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

// ModelSpec 是一个可切换模型的运行装配（/model 与 Ctrl+P 的数据源）。
type ModelSpec struct {
	Name    string // 配置键（用户可见名）
	ModelID string // 发给端点的 model 字符串
	Client  provider.Provider
	Window  int
}

// Config 装配 Agent 的全部依赖。
type Config struct {
	Root          context.Context
	Provider      provider.Provider
	Session       *session.Session
	Registry      *tool.Registry
	Checkpoints   *checkpoint.Store
	System        string
	DataRoot      string // /new /resume /branch 创建/打开会话的根目录
	ContextWindow int    // compaction 触发阈值依赖（0 = 不压缩）
	Models        []ModelSpec
}

type Agent struct {
	root     context.Context
	prov     provider.Provider
	sess     *session.Session
	reg      *tool.Registry
	cp       *checkpoint.Store
	system   string
	dataRoot string
	window   int

	model      string // 当前请求使用的 model ID
	modelName  string // 当前模型配置键
	models     map[string]ModelSpec
	modelNames []string

	events chan Event
	inbox  chan string

	gitHintShown bool

	mu         sync.Mutex
	turnCancel context.CancelFunc
}

func New(cfg Config) *Agent {
	a := &Agent{
		root:      cfg.Root,
		prov:      cfg.Provider,
		sess:      cfg.Session,
		reg:       cfg.Registry,
		cp:        cfg.Checkpoints,
		system:    cfg.System,
		dataRoot:  cfg.DataRoot,
		window:    cfg.ContextWindow,
		model:     cfg.Session.Header().Model,
		modelName: firstModelName(cfg),
		events:    make(chan Event, eventsBuffer),
		inbox:     make(chan string, 16),
	}
	a.models = make(map[string]ModelSpec, len(cfg.Models))
	for _, m := range cfg.Models {
		a.models[m.Name] = m
		a.modelNames = append(a.modelNames, m.Name)
	}
	return a
}

func firstModelName(cfg Config) string {
	if len(cfg.Models) > 0 {
		return cfg.Models[0].Name
	}
	return cfg.Session.Header().Model
}

func (a *Agent) Model() string             { return a.model }
func (a *Agent) Events() <-chan Event      { return a.events }
func (a *Agent) Session() *session.Session { return a.sess }

// ModelNames 返回可切换的模型名（含当前），按配置序。
func (a *Agent) ModelNames() []string { return a.modelNames }

// switchModel 切换模型：历史完整保留（carried），provider 与窗口随切；
// 模型隔离意味着 KV 缓存必然重建——如实提示（M3 切换语义）。
func (a *Agent) switchModel(name string) ([]string, error) {
	if a.Running() {
		return nil, errors.New("生成中不能切换模型，请先 Esc 中止")
	}
	spec, ok := a.models[name]
	if !ok {
		return nil, fmt.Errorf("未定义的模型 %q", name)
	}
	if name == a.modelName {
		return []string{fmt.Sprintf("已在使用 %s", name)}, nil
	}
	a.prov = spec.Client
	a.window = spec.Window
	a.modelName = name
	a.model = spec.ModelID
	a.emit(ModelSwitchedEvent{Name: name})
	return []string{fmt.Sprintf("已切换到 %s：历史完整保留；模型隔离，KV 缓存已重建", name)}, nil
}

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
		if step == 0 {
			// 压缩只在 turn 开始触发：turn 内的工具环正在消费自己产出的
			// 上下文，中途换前缀会破坏 step 语义（无 max-steps 的兜底）。
			a.autoCompact(ctx)
		}

		req := provider.Request{
			Model:    a.model,
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
