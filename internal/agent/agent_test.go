package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sammal/internal/provider"
)

// fakeProvider 是 Provider 的测试替身（引入接口的第二个当前实现）。
type fakeProvider struct {
	streams [][]provider.Chunk // 每次 Stream 消费一档
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
	out = append(out, provider.Chunk{FinishReason: "stop"}, provider.Chunk{Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 3}})
	return out
}

func drainEvents(t *testing.T, events <-chan Event, until func(Event) bool) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
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

func TestRunSingleTurn(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{textChunks("你好，世界")}}
	ag := New(context.Background(), fp, "qwen3:32b", "sys")
	events := ag.Events()
	go ag.Run(context.Background(), "hi")

	evs := drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopCompleted
	})

	var text string
	var final MessageFinalEvent
	for _, ev := range evs {
		switch ev := ev.(type) {
		case TextDeltaEvent:
			text += ev.Text
		case MessageFinalEvent:
			final = ev
		}
	}
	if text != "你好，世界" {
		t.Errorf("streamed text = %q", text)
	}
	if final.Text != "你好，世界" || final.Interrupted {
		t.Errorf("final = %+v", final)
	}
	if len(fp.calls) != 1 {
		t.Fatalf("provider calls = %d", len(fp.calls))
	}
	req := fp.calls[0]
	if req.System != "sys" || req.Model != "qwen3:32b" {
		t.Errorf("req = %+v", req)
	}
	// 请求是流开始前的快照：只含 user 消息。
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hi" {
		t.Errorf("messages = %+v", req.Messages)
	}
}

func TestRunHistoryCarriesAcrossTurns(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		textChunks("first"),
		textChunks("second"),
	}}
	ag := New(context.Background(), fp, "m", "sys")
	events := ag.Events()

	go ag.Run(context.Background(), "q1")
	drainEvents(t, events, func(e Event) bool { _, ok := e.(TurnEndedEvent); return ok })
	go ag.Run(context.Background(), "q2")
	drainEvents(t, events, func(e Event) bool { _, ok := e.(TurnEndedEvent); return ok })

	if len(fp.calls) != 2 {
		t.Fatalf("calls = %d", len(fp.calls))
	}
	second := fp.calls[1]
	// [u q1, a first, u q2]
	if len(second.Messages) != 3 || second.Messages[1].Role != "assistant" || second.Messages[1].Content != "first" {
		t.Fatalf("second turn messages = %+v", second.Messages)
	}
}

func TestRunAbortMarksInterrupted(t *testing.T) {
	slow := []provider.Chunk{
		{TextDelta: "partial"},
	}
	// 假流不主动结束：等待 ctx 取消由 abort 触发。
	blocking := make(chan struct{})
	fp := &blockingProvider{chunks: slow, release: blocking}
	ag := New(context.Background(), fp, "m", "sys")
	events := ag.Events()

	go ag.Run(context.Background(), "q")
	waitForRunning(t, ag)
	ag.Abort()

	evs := drainEvents(t, events, func(e Event) bool {
		te, ok := e.(TurnEndedEvent)
		return ok && te.StopReason == StopAborted
	})
	var final MessageFinalEvent
	for _, ev := range evs {
		if f, ok := ev.(MessageFinalEvent); ok {
			final = f
		}
	}
	if final.Text != "partial" || !final.Interrupted {
		t.Errorf("final = %+v", final)
	}
}

type blockingProvider struct {
	chunks  []provider.Chunk
	release chan struct{}
}

func (b *blockingProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		for _, ck := range b.chunks {
			select {
			case ch <- ck:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

func waitForRunning(t *testing.T, ag *Agent) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ag.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent 未进入 running")
}

func TestSteeringQueuedToNextTurn(t *testing.T) {
	var reqs []provider.Request
	release := make(chan struct{})
	fp := &holdProvider{reqs: &reqs, release: release}
	ag := New(context.Background(), fp, "m", "sys")
	events := ag.Events()

	go ag.Run(context.Background(), "q1")
	waitForRunning(t, ag)
	ag.Steering("插话") // 生成中排队

	close(release)
	drainEvents(t, events, func(e Event) bool { _, ok := e.(TurnEndedEvent); return ok })

	// M0 单步 turn：插话不在本轮吸收，历史中无它。
	if len(reqs) != 1 {
		t.Fatalf("reqs = %d", len(reqs))
	}

	go ag.Run(context.Background(), "q2")
	drainEvents(t, events, func(e Event) bool { _, ok := e.(TurnEndedEvent); return ok })
	if len(reqs) != 2 {
		t.Fatalf("reqs = %d", len(reqs))
	}
	// 插话在第二 turn 开始时吸收，时间序位于 q2 之前。
	last := reqs[1]
	var seq []string
	for _, msg := range last.Messages {
		seq = append(seq, msg.Role+":"+msg.Content)
	}
	joined := strings.Join(seq, " ")
	if !strings.Contains(joined, "user:插话") {
		t.Errorf("steering 消息未进入第二请求：%+v", last.Messages)
	}
	if strings.Index(joined, "user:插话") > strings.Index(joined, "user:q2") {
		t.Errorf("插话应排在 q2 之前：%s", joined)
	}
}

type holdProvider struct {
	reqs    *[]provider.Request
	release chan struct{}
}

func (h *holdProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	*h.reqs = append(*h.reqs, req)
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ch := make(chan provider.Chunk, 2)
	go func() {
		defer close(ch)
		ch <- provider.Chunk{FinishReason: "stop"}
	}()
	return ch, nil
}
