package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sammal/internal/provider"
	"sammal/internal/tool"
)

func newSession(t *testing.T) *Session {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	s, err := Create(root, Header{
		ID: "s1", Cwd: "/w", Model: "m1", Created: "2026-08-23T00:00:00Z", OS: "linux", Shell: "bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateOpenRoundtrip(t *testing.T) {
	s := newSession(t)
	s.Append(TypeUserMessage, UserMessageData{Text: "hi"})
	s.Append(TypeAssistantMessage, AssistantMessageData{Text: "hello"})
	s.EndTurn(StopReasonForTest())

	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Header().ID != "s1" || reopened.Header().Model != "m1" {
		t.Errorf("header = %+v", reopened.Header())
	}
	if len(reopened.Events()) != 4 {
		t.Fatalf("events = %d", len(reopened.Events()))
	}
	if reopened.Turn() != 2 {
		t.Errorf("turn = %d, want 2", reopened.Turn())
	}

	// 重开后继续追加。
	if err := reopened.Append(TypeUserMessage, UserMessageData{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path())
	if strings.Count(string(data), "\n") != 5 {
		t.Errorf("日志行数 = %d", strings.Count(string(data), "\n"))
	}
}

func StopReasonForTest() string { return "completed" }

// 尾部不完整行（崩溃残迹）被丢弃，有效尾部保留（I3 的基础）。
// 日志以闭合 turn 结尾，隔离「残缺行丢弃」与「未闭合 turn 恢复」两个行为。
func TestOpenDropsIncompleteTail(t *testing.T) {
	s := newSession(t)
	s.Append(TypeUserMessage, UserMessageData{Text: "hi"})
	s.EndTurn("completed")
	s.Close()

	path := s.Path()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"seq":4,"ts":"...","type":"assistant/mess`) // 半行
	f.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, env := range reopened.Events() {
		if env.Seq == 4 {
			t.Error("残缺行不应载入")
		}
	}
	if reopened.seq != 3 {
		t.Errorf("seq = %d", reopened.seq)
	}
}

// I3 崩溃恢复：kill -9 场景——悬挂 chunk、悬挂 tool/call、缺失 turn/end，
// 重开后全部获得合成闭合事件，投影与「未崩溃的等价日志」一致。
func TestCrashRecoverySynthesizesClosures(t *testing.T) {
	// 先构造「崩溃版」日志。
	crashed := newSession(t)
	crashed.Append(TypeUserMessage, UserMessageData{Text: "q"})
	crashed.Append(TypeAssistantMessage, AssistantMessageData{
		Text: "calling",
		ToolCalls: []provider.ToolCall{{
			ID: "c1", Type: provider.ToolTypeFunction,
			Function: provider.FunctionCall{Name: "read", Arguments: `{"path":"a"}`},
		}},
	})
	crashed.Append(TypeToolCall, ToolCallData{ID: "c1", Name: "read", Args: []byte(`{"path":"a"}`)})
	// 悬挂 chunk：第二 step 流到一半
	crashed.Append(TypeAssistantChunk, AssistantChunkData{Delta: "par"})
	crashed.Append(TypeAssistantChunk, AssistantChunkData{Delta: "tial"})
	crashed.Close()

	// 「等价净版」：同样的前缀 + 显式中断语义。
	clean := newSession(t)
	clean.Append(TypeUserMessage, UserMessageData{Text: "q"})
	clean.Append(TypeAssistantMessage, AssistantMessageData{
		Text: "calling",
		ToolCalls: []provider.ToolCall{{
			ID: "c1", Type: provider.ToolTypeFunction,
			Function: provider.FunctionCall{Name: "read", Arguments: `{"path":"a"}`},
		}},
	})
	clean.Append(TypeToolCall, ToolCallData{ID: "c1", Name: "read", Args: []byte(`{"path":"a"}`)})
	clean.Append(TypeToolResult, ToolResultData{
		ID: "c1", Canonical: tool.Result{Err: "interrupted by crash"}, Synthetic: true,
	})
	clean.Append(TypeAssistantMessage, AssistantMessageData{Text: "partial", Interrupted: true, Synthetic: true})
	clean.EndTurn("crash-recovered")
	clean.Close()

	recovered, err := Open(crashed.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()

	got := recovered.DeriveMessages()
	want := clean.DeriveMessages()
	if len(got) != len(want) {
		t.Fatalf("恢复后投影 = %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i].Role != want[i].Role || provider.ContentText(got[i].Content) != provider.ContentText(want[i].Content) || got[i].ToolCallID != want[i].ToolCallID {
			t.Errorf("消息 %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if recovered.Turn() != clean.Turn() {
		t.Errorf("turn: got %d want %d", recovered.Turn(), clean.Turn())
	}

	// 合成事件已落盘：再次重开是幂等的（不再追加）。
	count := len(recovered.Events())
	recovered.Close()
	again, err := Open(crashed.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(again.Events()) != count {
		t.Errorf("恢复不幂等: %d != %d", len(again.Events()), count)
	}
}

func TestDeriveMessagesProjection(t *testing.T) {
	s := newSession(t)
	s.Append(TypeUserMessage, UserMessageData{Text: "q1"})
	s.Append(TypeAssistantMessage, AssistantMessageData{
		Text: "calling",
		ToolCalls: []provider.ToolCall{{
			ID: "c1", Type: provider.ToolTypeFunction,
			Function: provider.FunctionCall{Name: "read", Arguments: `{"path":"a"}`},
		}},
	})
	s.Append(TypeToolCall, ToolCallData{ID: "c1", Name: "read", Args: []byte(`{"path":"a"}`)})
	s.Append(TypeToolResult, ToolResultData{ID: "c1", Canonical: tool.Result{Output: "1\ta"}})
	// assistant/chunk 是 UI 保真事件，不参与投影。
	s.Append(TypeAssistantChunk, AssistantChunkData{Delta: "noise"})
	s.Append(TypeAssistantMessage, AssistantMessageData{Text: "done"})

	msgs := s.DeriveMessages()
	if len(msgs) != 4 {
		t.Fatalf("msgs = %d: %+v", len(msgs), msgs)
	}
	if provider.ContentText(msgs[0].Content) != "q1" || msgs[1].ToolCalls[0].ID != "c1" {
		t.Errorf("msgs = %+v", msgs)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" || provider.ContentText(msgs[2].Content) != "1\ta" {
		t.Errorf("tool msg = %+v", msgs[2])
	}
	if provider.ContentText(msgs[3].Content) != "done" {
		t.Errorf("last msg = %+v", msgs[3])
	}
}

func TestTruncateBeforeTurn(t *testing.T) {
	s := newSession(t)
	s.Append(TypeUserMessage, UserMessageData{Text: "t1u"})
	s.EndTurn("completed")
	s.Append(TypeUserMessage, UserMessageData{Text: "t2u"})
	s.Append(TypeAssistantMessage, AssistantMessageData{Text: "t2a"})
	s.EndTurn("completed")
	s.Append(TypeUserMessage, UserMessageData{Text: "t3u"})
	s.EndTurn("completed")

	if err := s.TruncateBeforeTurn(2); err != nil {
		t.Fatal(err)
	}
	if s.Turn() != 2 {
		t.Errorf("turn = %d", s.Turn())
	}
	msgs := s.DeriveMessages()
	if len(msgs) != 1 || provider.ContentText(msgs[0].Content) != "t1u" {
		t.Fatalf("msgs = %+v", msgs)
	}
	// 截断后可继续追加，seq 单调不回退（截断后事件唯一编号的前提）。
	if err := s.Append(TypeUserMessage, UserMessageData{Text: "t2u-new"}); err != nil {
		t.Fatal(err)
	}
	last := s.Events()[len(s.Events())-1]
	if last.Seq != 4 {
		t.Errorf("seq = %d, want 4（截断保留 3 个事件后续写）", last.Seq)
	}
}

func TestNormalizeCwd(t *testing.T) {
	if got := NormalizeCwd(`D:\github-clone\sammal`); strings.ContainsAny(got, `:\/`) {
		t.Errorf("NormalizeCwd = %q", got)
	}
	if got := NormalizeCwd("/home/u/proj"); got != "-home-u-proj" {
		t.Errorf("NormalizeCwd = %q", got)
	}
}

// reasoning chunk 只落盘不投影：DeriveMessages 与崩溃恢复都不消费它。
func TestReasoningChunksExcludedFromProjection(t *testing.T) {
	s := newSession(t)
	s.Append(TypeUserMessage, UserMessageData{Text: "q"})
	s.Append(TypeAssistantChunk, AssistantChunkData{Delta: "想一半", Kind: ChunkReasoning})
	s.Append(TypeAssistantChunk, AssistantChunkData{Delta: "答案", Kind: ChunkText})
	s.Append(TypeAssistantMessage, AssistantMessageData{Text: "答案"})
	s.EndTurn(StopReasonForTest())

	msgs := s.DeriveMessages()
	if len(msgs) != 2 {
		t.Fatalf("msgs = %d: %+v", len(msgs), msgs)
	}
	if provider.ContentText(msgs[0].Content) != "q" || provider.ContentText(msgs[1].Content) != "答案" {
		t.Errorf("投影内容 = %+v", msgs)
	}

	// 崩溃恢复：悬挂的 reasoning chunk 不得被合成为 interrupted 消息。
	crashed := newSession(t)
	crashed.Append(TypeUserMessage, UserMessageData{Text: "q2"})
	crashed.Append(TypeAssistantChunk, AssistantChunkData{Delta: "只有思考", Kind: ChunkReasoning})
	crashed.Close()

	recovered, err := Open(crashed.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	for _, env := range recovered.Events() {
		if env.Type == TypeAssistantMessage {
			t.Errorf("纯思考悬挂不应合成 assistant/message: %+v", env)
		}
	}
}
