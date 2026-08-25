package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"sammal/internal/provider"
	"sammal/internal/session"
	"sammal/internal/tool"
)

// failLogTool 在第 n 次执行时关闭日志文件，模拟磁盘满等持久性写故障。
type failLogTool struct {
	sess   *session.Session
	calls  int
	failOn int
}

func (f *failLogTool) Name() string   { return "faillog" }
func (f *failLogTool) ReadOnly() bool { return false }
func (t *failLogTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
}
func (f *failLogTool) Description() string { return "test tool" }
func (f *failLogTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	f.calls++
	if f.calls == f.failOn {
		f.sess.Close() // 后续 Append 必然失败（file already closed）
	}
	return tool.Result{Output: "ok"}, nil
}

// 日志写失败必须快速失败（I1）：磁盘满等持久故障无法在 turn 内自愈，
// 带病续跑只会产出不可重放的状态。同批未执行的调用补合成结果。
func TestLogWriteFailureAbortsTurnWithSyntheticResults(t *testing.T) {
	ft := &failLogTool{failOn: 1}
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "faillog", ArgsDelta: `{}`},
		}, {
			ToolCallDelta: &provider.ToolCallDelta{Index: 1, ID: "c2", Name: "faillog", ArgsDelta: `{}`},
		},
			{FinishReason: "tool_calls"},
		},
	}}
	fx := newFixture(t, fp, ft)
	ft.sess = fx.sess
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "trigger log failure")
	evs := drainEvents(t, events, turnEnded)

	var sawErr bool
	for _, ev := range evs {
		if _, ok := ev.(ErrorEvent); ok {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("未收到 ErrorEvent（日志写失败被吞）")
	}
	if te := evs[len(evs)-1].(TurnEndedEvent); te.StopReason != StopError {
		t.Errorf("stopReason = %q, want %q", te.StopReason, StopError)
	}
	if ft.calls != 1 {
		t.Errorf("工具执行次数 = %d, want 1（结果落盘失败后同批余量不再执行）", ft.calls)
	}

	msgs := fx.sess.DeriveMessages()
	// [user, assistant(c1+c2)]——两次结果都未落成，投影不得伪造配对
	if len(msgs) != 2 {
		t.Fatalf("derived messages = %d: %+v", len(msgs), msgs)
	}
	if len(msgs[1].ToolCalls) != 2 {
		t.Errorf("assistant 工具调用丢失：%+v", msgs[1])
	}
}

// 提交前日志已损坏（如磁盘满导致首条 user 消息写不进去）：turn 以 error
// 结束并仍发 TurnEnded，消费端 busy 得以解除。
func TestSubmitWithBrokenLogEndsWithError(t *testing.T) {
	fp := &fakeProvider{}
	fx := newFixture(t, fp)
	events := fx.ag.Events()

	if err := fx.sess.Close(); err != nil {
		t.Fatal(err)
	}

	go fx.ag.Run(context.Background(), "hello")
	evs := drainEvents(t, events, turnEnded)

	if te := evs[len(evs)-1].(TurnEndedEvent); te.StopReason != StopError {
		t.Errorf("stopReason = %q, want %q", te.StopReason, StopError)
	}
	if len(fp.calls) != 0 {
		t.Errorf("日志已坏不应发起模型请求，实际 %d 次", len(fp.calls))
	}
}

// 消费端停止后 emit 必须随 root 取消返回，否则 Submit/Slash 等同步调用方
// 会把自己冻死在无缓冲发送上。
func TestEmitReturnsAfterRootCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &Agent{root: ctx, events: make(chan Event)}

	done := make(chan struct{})
	go func() {
		a.emit(TurnStartedEvent{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emit 在 root 取消后仍阻塞")
	}
}
