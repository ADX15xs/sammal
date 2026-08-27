package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"sammal/internal/provider"
	"sammal/internal/session"
)

// reasonChunks 构造一次「思考 → 文本」流。
func reasonChunks(reason, text string) []provider.Chunk {
	var out []provider.Chunk
	for _, r := range reason {
		out = append(out, provider.Chunk{ReasonDelta: string(r)})
	}
	for _, r := range text {
		out = append(out, provider.Chunk{TextDelta: string(r)})
	}
	out = append(out, provider.Chunk{FinishReason: "stop"},
		provider.Chunk{Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 3}})
	return out
}

// 思考增量：事件流含 ReasonDelta/ReasonFinal，日志落 kind=reasoning 的
// chunk，但模型投影不含思考（发给模型的 assistant/message 只有文本）。
func TestReasoningStreamsLoggedButNotProjected(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		reasonChunks("思考过程", "答案"),
	}}
	fx := newFixture(t, fp)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, turnEnded)

	var sawReason, sawReasonFinal bool
	for _, ev := range evs {
		switch ev.(type) {
		case ReasonDeltaEvent:
			sawReason = true
		case ReasonFinalEvent:
			sawReasonFinal = true
		}
	}
	if !sawReason || !sawReasonFinal {
		t.Fatalf("缺少 reasoning 事件: saw=%v final=%v", sawReason, sawReasonFinal)
	}

	// 日志：reasoning chunk 落盘且 kind 正确。
	var reasonText, answerText strings.Builder
	for _, env := range fx.sess.Events() {
		if env.Type != session.TypeAssistantChunk {
			continue
		}
		var d session.AssistantChunkData
		json.Unmarshal(env.Data, &d)
		switch d.Kind {
		case session.ChunkReasoning:
			reasonText.WriteString(d.Delta)
		case session.ChunkText, "":
			answerText.WriteString(d.Delta)
		}
	}
	if reasonText.String() != "思考过程" {
		t.Errorf("日志 reasoning = %q", reasonText.String())
	}
	if answerText.String() != "答案" {
		t.Errorf("日志 text chunk = %q", answerText.String())
	}

	// 模型投影不含思考；I1 重放不受影响。
	msgs := fx.sess.DeriveMessages()
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || provider.ContentText(last.Content) != "答案" {
		t.Fatalf("投影 assistant = %+v", last)
	}
	if strings.Contains(provider.ContentText(last.Content), "思考") {
		t.Error("投影混入思考内容")
	}
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d 重放不一致", i)
		}
	}
}

// 工具环中的思考块：每个 step 的思考在文本前闭合（ReasonFinal 先于
// TextDelta），多 step 后投影仍一致。
func TestReasoningClosesBeforeToolCallTurn(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		append(reasonChunks("先想", ""), toolCallChunks("c1", "read", `{"path":"a.txt"}`)...),
		textChunks("done"),
	}}
	fx := newFixture(t, fp)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	evs := drainEvents(t, events, turnEnded)

	// 每个 step 的思考都以 ReasonFinal 闭合（流尾兜底）。
	var reasonDeltas, reasonFinals int
	var textDelta bool
	for _, ev := range evs {
		switch ev.(type) {
		case ReasonDeltaEvent:
			reasonDeltas++
		case ReasonFinalEvent:
			reasonFinals++
		case TextDeltaEvent:
			textDelta = true
		}
	}
	if reasonDeltas == 0 || reasonFinals == 0 {
		t.Fatalf("reasoning 事件缺失: delta=%d final=%d", reasonDeltas, reasonFinals)
	}
	if !textDelta {
		t.Error("缺少文本增量")
	}

	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("工具环 request %d 重放不一致", i)
		}
	}
}
