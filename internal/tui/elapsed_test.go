package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
)

// printedLines 执行 cmd 并收集其中 tea.Println 的文本。BatchMsg 递归展开；
// printLineMessage 未导出（bubbletea 内部类型），经反射取 messageBody。
// Deps.Events 需传已关闭的 channel：listenAgent 子命令立即返回不阻塞。
func printedLines(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []string
		for _, c := range m {
			out = append(out, printedLines(c)...)
		}
		return out
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Struct && v.Type().Name() == "printLineMessage" {
		if f := v.FieldByName("messageBody"); f.IsValid() {
			return []string{f.String()}
		}
	}
	return nil
}

// 回合正常完成：回答末尾追加一行暗色耗时标记，且 turnStart 归零无残留。
func TestTurnElapsedStampOnCompleted(t *testing.T) {
	events := make(chan agent.Event)
	close(events)
	m := New(Deps{ModelName: "m", Events: events})
	m = applyEvent(t, m,
		agent.TurnStartedEvent{},
		agent.MessageFinalEvent{Text: "回答正文"},
	)
	m.turnStart = time.Now().Add(-90 * time.Second)

	next, cmd := m.applyAgentEvent(agent.TurnEndedEvent{StopReason: agent.StopCompleted})
	m = next.(Model)
	prints := printedLines(cmd)
	if len(prints) != 1 || !strings.Contains(prints[0], "（耗时 1m30s）") {
		t.Fatalf("完成回合应打印耗时标记: %q", prints)
	}
	if !strings.Contains(prints[0], ansiDim) {
		t.Errorf("耗时标记应为暗色旁白: %q", prints[0])
	}
	if !m.turnStart.IsZero() {
		t.Errorf("turnStart 应已归零: %v", m.turnStart)
	}
}

// 中止回合不打耗时标记：只保留既有的（已中止）旁白。
func TestTurnElapsedStampNotOnAborted(t *testing.T) {
	events := make(chan agent.Event)
	close(events)
	m := New(Deps{ModelName: "m", Events: events})
	m = applyEvent(t, m, agent.TurnStartedEvent{})
	m.turnStart = time.Now().Add(-30 * time.Second)

	_, cmd := m.applyAgentEvent(agent.TurnEndedEvent{StopReason: agent.StopAborted})
	prints := printedLines(cmd)
	for _, p := range prints {
		if strings.Contains(p, "耗时") {
			t.Fatalf("中止回合不应打耗时标记: %q", prints)
		}
	}
	if len(prints) != 1 || !strings.Contains(prints[0], "已中止") {
		t.Fatalf("中止旁白应保留: %q", prints)
	}
}

// TurnStarted 之前的失败路径（turnStart 为零）不打标记——无计时起点。
func TestTurnElapsedStampNotBeforeTurnStarted(t *testing.T) {
	events := make(chan agent.Event)
	close(events)
	m := New(Deps{ModelName: "m", Events: events})

	_, cmd := m.applyAgentEvent(agent.TurnEndedEvent{StopReason: agent.StopError})
	if prints := printedLines(cmd); len(prints) != 0 {
		t.Fatalf("无 turnStart 不应打任何标记: %q", prints)
	}
}
