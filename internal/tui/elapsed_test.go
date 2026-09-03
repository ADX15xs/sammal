package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
	"sammal/internal/provider"
)

// printedLines 执行 cmd 并收集其中 tea.Println 的文本。BatchMsg 递归展开
// （顺序无保证），sequenceMsg 按序展开；printLineMessage 未导出（bubbletea
// 内部类型），经反射取 messageBody。Deps.Events 需传已关闭的 channel：
// listenAgent 子命令立即返回不阻塞。
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
	if v.Kind() == reflect.Slice && v.Type().Name() == "sequenceMsg" {
		var out []string
		for i := 0; i < v.Len(); i++ {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				out = append(out, printedLines(c)...)
			}
		}
		return out
	}
	if v.Kind() == reflect.Struct && v.Type().Name() == "printLineMessage" {
		if f := v.FieldByName("messageBody"); f.IsValid() {
			return []string{f.String()}
		}
	}
	return nil
}

// isSequenced 报告 cmd 里是否含 tea.Sequence 组。tea.Batch 无顺序保证，
// 只断言打印内容的声明顺序会漏掉竞速本身，故直接断言顺序机制。
func isSequenced(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case nil:
		return false
	case tea.BatchMsg:
		for _, c := range msg {
			if isSequenced(c) {
				return true
			}
		}
		return false
	default:
		return reflect.ValueOf(msg).Type().Name() == "sequenceMsg"
	}
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
	if len(prints) != 1 || !strings.Contains(prints[0], "（耗时 1m30s · ") || !strings.Contains(prints[0], "完成）") {
		t.Fatalf("完成回合应打印耗时与完成时刻: %q", prints)
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

// 同一回合既触发上下文告警又打耗时标记：告警须排在落款之前。tea.Batch
// 无顺序保证，两者因此走 Sequence——此用例即固定该顺序，防止回退成 Batch。
func TestContextWarnPrecedesElapsedStamp(t *testing.T) {
	events := make(chan agent.Event)
	close(events)
	m := New(Deps{ModelName: "m", ContextWindow: 1000, Events: events})
	m = applyEvent(t, m, agent.TurnStartedEvent{})
	m.turnStart = time.Now().Add(-90 * time.Second)

	next, cmd := m.applyAgentEvent(agent.TurnEndedEvent{
		StopReason: agent.StopCompleted,
		Usage:      &provider.Usage{PromptTokens: 900}, // 90% ≥ 压缩触发线 80%
	})
	_ = next.(Model)
	prints := printedLines(cmd)
	warn, stamp := -1, -1
	for i, p := range prints {
		if strings.Contains(p, "上下文已达窗口") {
			warn = i
		}
		if strings.Contains(p, "耗时") {
			stamp = i
		}
	}
	if warn < 0 || stamp < 0 {
		t.Fatalf("应同时含告警与耗时落款: %q", prints)
	}
	if warn > stamp {
		t.Errorf("告警应排在落款之前: %q", prints)
	}
	if !isSequenced(cmd) {
		t.Error("打印段应走 tea.Sequence：tea.Batch 不保证 Println 顺序")
	}
}

// 等待期（已提交、TurnStarted 未到）的心跳推进 spinner 帧；空闲时心跳终止。
func TestWaitingSpinnerAdvances(t *testing.T) {
	events := make(chan agent.Event)
	close(events)
	m := New(Deps{ModelName: "m", Events: events})
	m.busy = true

	s0 := stripANSI(m.statusLine())
	if !strings.Contains(s0, "生成中") {
		t.Fatalf("等待期应显示生成中: %q", s0)
	}
	next, cmd := m.turnTick()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("busy 期间心跳应自续")
	}
	s1 := stripANSI(m.statusLine())
	if s0 == s1 {
		t.Errorf("spinner 帧应随心跳推进: %q", s0)
	}

	m.busy = false
	next, cmd = m.turnTick()
	m = next.(Model)
	if cmd != nil {
		t.Fatalf("空闲时心跳应终止: %v", cmd)
	}
}
