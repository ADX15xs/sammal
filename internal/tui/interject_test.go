package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
	"sammal/internal/tool"
)

// T9 短期方案（调研报告「实施路径」）：插话/工具行从 interject 队列随流式
// 块一起落盘（不单独 insertAbove——舞蹈间的位移错位是根因），且不得重复
// 渲染。Enter 回显恰好在滚动区出现一次；派发后 interject 清空、自绘帧内
// 不再残留（避免「回显重复出现两次」）。
func TestInterjectDispatchedOnceWithStreamFlush(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.editor.Insert("hi")

	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	// Enter 路径：回显入 interject 并随 flush 落盘一次
	prints := printedLines(cmd)
	joined := strings.Join(prints, "\n")
	if len(prints) != 1 || !strings.Contains(joined, "> hi") {
		t.Fatalf("回响应落盘一次: %q", prints)
	}
	if len(m.interject) != 0 {
		t.Fatalf("回显已随 flush 派发，interject 应清空，实际 %d 条", len(m.interject))
	}
	// 帧内不得残留回显（滚动区已有一次，帧内再现即重复）
	if v := m.View().Content; strings.Contains(v, "hi") {
		t.Fatalf("回显不应残留在自绘帧内（已进滚动区）：%q", v)
	}
	// 排空链收尾（测试无事件循环，手动回环 drainNextMsg）
	for m.scrollQ.draining {
		next, c := m.Update(drainNextMsg{})
		m = next.(Model)
		_ = c
	}

	// 工具调用/结果行：入 interject 后立即随 flush 派发（消息边界），不残留
	m, cmd = ev(t, m, agent.ToolCallEvent{ID: "c1", Name: "bash", ArgsSummary: `{"cmd":"ls"}`})
	joined = strings.Join(printedLines(cmd), "\n")
	if !strings.Contains(joined, "-> bash") {
		t.Fatalf("工具调用行应随 flush 落盘: %q", joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("工具行应已派发，interject 残留 %d 条", len(m.interject))
	}
	for m.scrollQ.draining {
		next, c := m.Update(drainNextMsg{})
		m = next.(Model)
		_ = c
	}

	m, cmd = ev(t, m, agent.ToolResultEvent{ID: "c1", Name: "bash", Result: tool.Result{Output: `{"output":"a.txt"}`}})
	joined = strings.Join(printedLines(cmd), "\n")
	if !strings.Contains(joined, "<- bash") {
		t.Fatalf("工具结果行应随 flush 落盘: %q", joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("工具结果应已派发，interject 残留 %d 条", len(m.interject))
	}
}

// 生成中（busy）插话回显同样只落盘一次，且不打断进行中的流式尾部窗口。
func TestInterjectEchoWhileBusyFlushedOnce(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30

	// 模拟生成中：流式尾部已有内容，用户回车插话
	m.busy = true
	m.stream.WriteString("正在输出第一行\n半")

	m.editor.Insert("插话")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	// 回显并入流式批：流式闭合行 + 回显 同一队列派发，回显恰好一次
	prints := printedLines(cmd)
	joined := strings.Join(prints, "\n")
	if !strings.Contains(joined, "> 插话") {
		t.Fatalf("插话回显应随流式批落盘: %q", joined)
	}
	if strings.Count(joined, "插话") != 1 {
		t.Fatalf("插话回显出现 %d 次（应为 1）：%q", strings.Count(joined, "插话"), joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("插话已派发，interject 应清空，实际 %d 条", len(m.interject))
	}
}
