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
func TestOpenDropsIncompleteTail(t *testing.T) {
	s := newSession(t)
	s.Append(TypeUserMessage, UserMessageData{Text: "hi"})
	s.Close()

	path := s.Path()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"seq":3,"ts":"...","type":"assistant/mess`) // 半行
	f.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, env := range reopened.Events() {
		if env.Seq == 3 {
			t.Error("残缺行不应载入")
		}
	}
	if reopened.seq != 2 {
		t.Errorf("seq = %d", reopened.seq)
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
	if msgs[0].Content != "q1" || msgs[1].ToolCalls[0].ID != "c1" {
		t.Errorf("msgs = %+v", msgs)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" || msgs[2].Content != "1\ta" {
		t.Errorf("tool msg = %+v", msgs[2])
	}
	if msgs[3].Content != "done" {
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
	if len(msgs) != 1 || msgs[0].Content != "t1u" {
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
