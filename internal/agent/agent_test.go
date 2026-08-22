package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sammal/internal/checkpoint"
	"sammal/internal/provider"
	"sammal/internal/session"
	"sammal/internal/tool"
)

// fakeProvider 是 Provider 的测试替身（引入接口的第二个当前实现）。
type fakeProvider struct {
	streams [][]provider.Chunk
	calls   []provider.Request
}

func (f *fakeProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	f.calls = append(f.calls, req)
	if len(f.streams) == 0 {
		return nil, &provider.StreamInterruptedError{Kind: provider.InterruptProtocol, Err: errors.New("no scripted stream")}
	}
	script := f.streams[0]
	f.streams = f.streams[1:]
	ch := make(chan provider.Chunk, len(script)+1)
	go func() {
		defer close(ch)
		for _, ck := range script {
			select {
			case ch <- ck:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func textChunks(s string) []provider.Chunk {
	var out []provider.Chunk
	for _, r := range []rune(s) {
		out = append(out, provider.Chunk{TextDelta: string(r)})
	}
	out = append(out, provider.Chunk{FinishReason: "stop"},
		provider.Chunk{Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 3}})
	return out
}

// toolCallChunks 构造一次工具调用流。
func toolCallChunks(id, name, args string) []provider.Chunk {
	return []provider.Chunk{
		{ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: id, Name: name, ArgsDelta: args}},
		{FinishReason: "tool_calls"},
		{Usage: &provider.Usage{PromptTokens: 20, CompletionTokens: 5}},
	}
}

type fixture struct {
	ag   *Agent
	sess *session.Session
	reg  *tool.Registry
	cp   *checkpoint.Store
	work string
}

// newFixture 装配真实 session + checkpoint + 工具；tools 非空时替换默认六件套。
func newFixture(t *testing.T, fp provider.Provider, tools ...tool.Tool) *fixture {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "data")
	facts := PromptFacts{Cwd: work, OS: "linux", Shell: "sh", Date: "2026-08-23"}
	sess, err := session.Create(root, session.Header{
		ID: session.NewID(), Cwd: work, Model: "test-model",
		Created: "2026-08-23T00:00:00Z", OS: facts.OS, Shell: facts.Shell,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	if len(tools) == 0 {
		tools = []tool.Tool{
			&tool.ReadTool{WorkDir: work},
			&tool.WriteTool{WorkDir: work},
			&tool.EditTool{WorkDir: work},
			&tool.BashTool{WorkDir: work, Shell: "sh"},
			&tool.GrepTool{WorkDir: work},
			&tool.GlobTool{WorkDir: work},
		}
	}
	reg := tool.NewRegistry(tools...)
	cp := checkpoint.New(sess.Dir(), work)
	ag := New(Config{
		Root:        context.Background(),
		Provider:    fp,
		Session:     sess,
		Registry:    reg,
		Checkpoints: cp,
		System:      BuildSystemPrompt(facts),
	})
	return &fixture{ag: ag, sess: sess, reg: reg, cp: cp, work: work}
}

func (f *fixture) system() string {
	h := f.sess.Header()
	return BuildSystemPrompt(PromptFacts{Cwd: h.Cwd, OS: h.OS, Shell: h.Shell, Date: h.Created[:10]})
}

func drainEvents(t *testing.T, events <-chan Event, until func(Event) bool) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			out = append(out, ev)
			if until(ev) {
				return out
			}
		case <-timeout:
			t.Fatalf("事件等待超时，已收到 %d 个", len(out))
		}
	}
}

func turnEnded(e Event) bool {
	_, ok := e.(TurnEndedEvent)
	return ok
}

// M1 核心验收：写文件→读回→定稿的闭环（模型侧脚本化）。
func TestRunToolLoopEndToEnd(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "write", `{"path":"out.txt","content":"v1"}`),
		toolCallChunks("c2", "read", `{"path":"out.txt"}`),
		textChunks("done"),
	}}
	fx := newFixture(t, fp)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "create out.txt with v1")
	evs := drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopCompleted
	})

	data, err := os.ReadFile(filepath.Join(fx.work, "out.txt"))
	if err != nil || string(data) != "v1" {
		t.Fatalf("file = %q err = %v", data, err)
	}

	var toolResults []ToolResultEvent
	var finalText string
	for _, ev := range evs {
		switch ev := ev.(type) {
		case ToolResultEvent:
			toolResults = append(toolResults, ev)
		case MessageFinalEvent:
			if !ev.Interrupted {
				finalText = ev.Text
			}
		}
	}
	if finalText != "done" {
		t.Errorf("final = %q", finalText)
	}
	if len(toolResults) != 2 {
		t.Fatalf("tool results = %d", len(toolResults))
	}
	if toolResults[0].Result.Err != "" || toolResults[1].Result.Output == "" {
		t.Errorf("tool results = %+v", toolResults)
	}
	if len(fp.calls) != 3 {
		t.Fatalf("provider calls = %d", len(fp.calls))
	}

	msgs := fx.sess.DeriveMessages()
	// [user, assistant(tool), tool, assistant(tool), tool, assistant]
	if len(msgs) != 6 {
		t.Fatalf("derived messages = %d: %+v", len(msgs), msgs)
	}
	if msgs[1].ToolCalls[0].Function.Name != "write" || msgs[2].Role != "tool" {
		t.Errorf("messages = %+v", msgs)
	}
}

// I1 golden 请求重放：日志重建的请求与留痕 prefixHash 逐字节一致。
func TestI1GoldenRequestReplay(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "write", `{"path":"a.txt","content":"hello"}`),
		toolCallChunks("c2", "read", `{"path":"a.txt"}`),
		textChunks("fin"),
	}}
	fx := newFixture(t, fp)
	go fx.ag.Run(context.Background(), "do it")
	drainEvents(t, fx.ag.Events(), turnEnded)

	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 {
		t.Fatalf("request headers = %d", len(pairs))
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: logged %s != rebuilt %s", i, p[0], p[1])
		}
	}

	// 从磁盘重开（resume 路径）再重放：留痕哈希不变。
	reopened, err := session.Open(fx.sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pairs2, err := reopened.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs2) != len(pairs) {
		t.Fatalf("reopened headers = %d", len(pairs2))
	}
	for i := range pairs2 {
		if pairs2[i][0] != pairs[i][0] {
			t.Errorf("重开后留痕哈希变化：request %d", i)
		}
	}
}

// I2 跨轮前缀稳定性。规格表述「相邻请求公共前缀 = 上一请求全长」在
// JSON 嵌套结构下按字节字面不成立（messages 数组闭括号处必然分叉）；
// 其序列化等价形式是：system、tools 以及历史消息逐条序列化字节一致
// ——这正是服务端 token 前缀（KV 缓存）所覆盖的内容。
func TestI2AdjacentTurnPrefix(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		textChunks("r1"),
		textChunks("r2"),
	}}
	fx := newFixture(t, fp)
	go fx.ag.Run(context.Background(), "q1")
	drainEvents(t, fx.ag.Events(), turnEnded)
	go fx.ag.Run(context.Background(), "q2")
	drainEvents(t, fx.ag.Events(), turnEnded)

	r1, r2 := fp.calls[0], fp.calls[1]
	if r1.System != r2.System {
		t.Error("system 提示词跨轮变化")
	}
	tb1, _ := json.Marshal(r1.Tools)
	tb2, _ := json.Marshal(r2.Tools)
	if string(tb1) != string(tb2) {
		t.Error("工具目录跨轮变化")
	}
	if len(r2.Messages) < len(r1.Messages) {
		t.Fatalf("消息历史不应缩短: %d < %d", len(r2.Messages), len(r1.Messages))
	}
	for i := range r1.Messages {
		m1, _ := json.Marshal(r1.Messages[i])
		m2, _ := json.Marshal(r2.Messages[i])
		if string(m1) != string(m2) {
			t.Errorf("消息 %d 跨轮序列化漂移:\n %s\n %s", i, m1, m2)
		}
	}
	// 请求体字节级确定性：同一请求重复序列化一致。
	x1, _ := provider.MarshalRequest(r1)
	x2, _ := provider.MarshalRequest(r1)
	if string(x1) != string(x2) {
		t.Error("MarshalRequest 不确定")
	}
}

// slowTool 在执行期间阻塞，用于触发工具阶段的中止。
type slowTool struct {
	delay   time.Duration
	started chan struct{}
	once    sync.Once
}

func newSlowTool(d time.Duration) *slowTool {
	return &slowTool{delay: d, started: make(chan struct{})}
}

func (s *slowTool) Name() string            { return "write" }
func (s *slowTool) Description() string     { return "slow write for tests" }
func (s *slowTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *slowTool) ReadOnly() bool          { return false }
func (s *slowTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-time.After(s.delay):
		return tool.Result{Output: "written"}, nil
	case <-ctx.Done():
		return tool.Result{Err: "aborted during execution"}, nil
	}
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("等待超时")
	}
}

// 中止语义：未执行的工具调用获得合成错误结果，日志可重放。
func TestAbortDuringToolsSyntheticResults(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{
			{ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"x.txt","content":"a"}`}},
			{ToolCallDelta: &provider.ToolCallDelta{Index: 1, ID: "c2", Name: "write", ArgsDelta: `{"path":"y.txt","content":"b"}`}},
			{FinishReason: "tool_calls"},
		},
		// 第二 step 不会发生（中止在工具执行阶段）
	}}
	slow := newSlowTool(5 * time.Second)
	fx := newFixture(t, fp, slow)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "go")
	waitFor(t, slow.started)
	fx.ag.Abort()
	drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopAborted
	})

	// 日志重放：两个 tool/result 都存在，第二个是合成的。
	var results []session.ToolResultData
	for _, env := range fx.sess.Events() {
		if env.Type == session.TypeToolResult {
			var d session.ToolResultData
			if err := json.Unmarshal(env.Data, &d); err != nil {
				t.Fatal(err)
			}
			results = append(results, d)
		}
	}
	if len(results) != 2 {
		t.Fatalf("tool results = %d", len(results))
	}
	if !results[1].Synthetic || results[1].Canonical.Err == "" {
		t.Errorf("第二个结果应为合成错误: %+v", results[1])
	}
	if _, err := os.Stat(filepath.Join(fx.work, "y.txt")); err == nil {
		t.Error("y.txt 不应被写入")
	}

	// 合成结果参与投影，重放哈希仍一致。
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("中止后 request %d 重放不一致", i)
		}
	}
}

// 收件箱 steering：多 step turn 中，插话在下一个 step 边界吸收。
func TestSteeringAbsorbedAtStepBoundary(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "write", `{"path":"s.txt","content":"s"}`),
		textChunks("after steering"),
	}}
	slow := newSlowTool(300 * time.Millisecond)
	fx := newFixture(t, fp, slow)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "q1")
	waitFor(t, slow.started) // 工具执行期是确定的插话窗口
	fx.ag.Steering("use PowerShell syntax")
	drainEvents(t, events, turnEnded)

	if len(fp.calls) < 2 {
		t.Fatalf("calls = %d", len(fp.calls))
	}
	found := false
	for _, msg := range fp.calls[1].Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "PowerShell") {
			found = true
		}
	}
	if !found {
		t.Errorf("插话未进入第二请求: %+v", fp.calls[1].Messages)
	}
}

// /rewind 同时回滚文件与对话。
func TestRewindRollsBackFilesAndConversation(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "write", `{"path":"r.txt","content":"v2"}`),
		textChunks("turn1 done"),
		textChunks("turn2 done"),
	}}
	fx := newFixture(t, fp)
	orig := filepath.Join(fx.work, "r.txt")
	os.WriteFile(orig, []byte("v1"), 0o644)

	go fx.ag.Run(context.Background(), "edit r.txt")
	drainEvents(t, fx.ag.Events(), turnEnded)
	go fx.ag.Run(context.Background(), "anything")
	drainEvents(t, fx.ag.Events(), turnEnded)

	if data, _ := os.ReadFile(orig); string(data) != "v2" {
		t.Fatalf("file = %q", data)
	}

	out := fx.ag.Slash("/rewind 1")
	if len(out) != 1 || !strings.Contains(out[0], "已回滚") {
		t.Fatalf("/rewind 输出 = %v", out)
	}
	if data, _ := os.ReadFile(orig); string(data) != "v1" {
		t.Fatalf("回滚后 file = %q, want v1", data)
	}
	for _, msg := range fx.sess.DeriveMessages() {
		if msg.Role == "user" && strings.Contains(msg.Content, "edit r.txt") {
			t.Error("turn 1 的对话未截断")
		}
	}
	turns, _ := fx.cp.Turns()
	if len(turns) != 0 {
		t.Errorf("快照目录未清理: %v", turns)
	}
}

func TestSlashHelp(t *testing.T) {
	fx := newFixture(t, &fakeProvider{})
	out := fx.ag.Slash("/help")
	if len(out) == 0 || !strings.Contains(out[0], "/rewind") {
		t.Errorf("/help = %v", out)
	}
	out = fx.ag.Slash("/nope")
	if len(out) == 0 || !strings.Contains(out[0], "未知命令") {
		t.Errorf("未知命令输出 = %v", out)
	}
}
