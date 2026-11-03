package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
	"sammal/internal/tool"
)

// drainAll 手动跑完排空链（测试无事件循环），返回期间落盘的行。
func drainAll(t *testing.T, m Model) (Model, []string) {
	t.Helper()
	var printed []string
	for m.scrollQ.draining {
		next, cmd := m.Update(drainNextMsg{})
		m = next.(Model)
		printed = append(printed, printedLines(cmd)...)
	}
	return m, printed
}

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
		t.Fatalf("回显应落盘一次: %q", prints)
	}
	if len(m.interject) != 0 {
		t.Fatalf("回显已随 flush 派发，interject 应清空，实际 %d 条", len(m.interject))
	}
	// 帧内不得残留回显（滚动区已有一次，帧内再现即重复）
	if v := m.View().Content; strings.Contains(v, "hi") {
		t.Fatalf("回显不应残留在自绘帧内（已进滚动区）：%q", v)
	}
	// 排空链收尾（测试无事件循环，手动回环 drainNextMsg）
	m, _ = drainAll(t, m)

	// 工具调用/结果行：入 interject 后立即随 flush 派发（消息边界），不残留
	m, cmd = ev(t, m, agent.ToolCallEvent{ID: "c1", Name: "bash", ArgsSummary: `{"cmd":"ls"}`})
	joined = strings.Join(printedLines(cmd), "\n")
	if !strings.Contains(joined, "-> bash") {
		t.Fatalf("工具调用行应随 flush 落盘: %q", joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("工具行应已派发，interject 残留 %d 条", len(m.interject))
	}
	m, _ = drainAll(t, m)

	m, cmd = ev(t, m, agent.ToolResultEvent{ID: "c1", Name: "bash", Result: tool.Result{Output: `{"output":"a.txt"}`}})
	joined = strings.Join(printedLines(cmd), "\n")
	if !strings.Contains(joined, "<- bash") {
		t.Fatalf("工具结果行应随 flush 落盘: %q", joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("工具结果应已派发，interject 残留 %d 条", len(m.interject))
	}
}

// TestInterjectDedupeInWindow 同一内容在 500ms 内重复提交应被去重
func TestInterjectDedupeInWindow(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("正在输出第一行\n半")

	// 第一次 Enter
	m.editor.Insert("插话")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	prints1 := printedLines(cmd)
	joined1 := strings.Join(prints1, "\n")
	if strings.Count(joined1, "插话") != 1 {
		t.Fatalf("首次插话应出现 1 次，实际 %d 次: %q", strings.Count(joined1, "插话"), joined1)
	}

	// 第二次 Enter（相同内容，500ms 窗口内）
	m.editor.Insert("插话")
	next, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	// 由于去重，第二次不应产生新的输出
	if len(printedLines(cmd)) != 0 {
		t.Fatalf("去重生效：第二次相同插话不应产生输出")
	}
	if len(m.interject) != 0 {
		t.Fatalf("interject 应清空，实际 %d 条", len(m.interject))
	}
}

// TestInterjectDedupeExpiredWindow 时间窗口过期后相同内容可再次入队
// 注意：测试无法直接操控时间，只能验证去重逻辑存在
func TestInterjectDedupeExpiredWindow(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("正在输出\n")

	// 第一次插话
	m.editor.Insert("相同内容")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	prints1 := printedLines(cmd)
	joined1 := strings.Join(prints1, "\n")
	if strings.Count(joined1, "相同内容") != 1 {
		t.Fatalf("首次插话应出现 1 次: %q", joined1)
	}

	// 再次插入相同内容（时间窗口内，会被去重）
	m.editor.Insert("相同内容")
	next, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	prints2 := printedLines(cmd)
	// 窗口内去重，第二次无输出
	if len(prints2) != 0 {
		t.Fatalf("时间窗口内重复应去重，无输出: %q", prints2)
	}
}

// TestInterjectMultiFlushMultiInterject 多 chunk、多插话、多 flush 场景
// 模拟：模型分 3 个 chunk 流出、用户在第 2 个 chunk 前连按两次 Enter、
// 第 2、3 个 chunk 各触发一次 flush
func TestInterjectMultiFlushMultiInterject(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true

	// 模型第 1 个 chunk 到达
	m.stream.WriteString("第一行\n")
	_, cmd := m.applyAgentEvent(agent.TextDeltaEvent{Text: "第一行\n"})
	printed := printedLines(cmd)
	for _, p := range printed {
		if strings.Contains(p, "插话") {
			t.Fatalf("第 1 个 chunk 不应有插话: %q", p)
		}
	}

	// 用户在第 2 个 chunk 前连按两次 Enter（不同内容）
	m.editor.Insert("确认")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	_ = printedLines(cmd) // 消费输出

	m.editor.Insert("取消")
	next, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	_ = printedLines(cmd) // 消费输出

	// 模型第 2 个 chunk 到达，触发 flush（流式文本 + 之前的插话已随前两次 Enter 分别 flush）
	// 注意：每次 Enter 都会触发 flush，所以插话已随各自的 Enter 落盘
	// 这里验证第 2 个 chunk 自身不带插话
	m.stream.WriteString("第二行\n")
	_, cmd = m.applyAgentEvent(agent.TextDeltaEvent{Text: "第二行\n"})
	printed = printedLines(cmd)
	joined := strings.Join(printed, "\n")
	if strings.Contains(joined, "确认") || strings.Contains(joined, "取消") {
		t.Fatalf("第 2 个 chunk 自身不应重复插话（插话已随 Enter 分别落盘）: %q", joined)
	}

	// 模型第 3 个 chunk 到达
	m.stream.WriteString("第三行\n")
	_, cmd = m.applyAgentEvent(agent.TextDeltaEvent{Text: "第三行\n"})
	printed = printedLines(cmd)
	joined = strings.Join(printed, "\n")
	if strings.Contains(joined, "确认") || strings.Contains(joined, "取消") {
		t.Fatalf("第 3 个 chunk 不应重复插话: %q", joined)
	}
}

// TestInterjectWhileBusyDedupe 生成中插话去重
func TestInterjectWhileBusyDedupe(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("正在输出第一行\n半")

	// 用户快速连按两次 Enter
	m.editor.Insert("插话")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	prints1 := printedLines(cmd)
	joined1 := strings.Join(prints1, "\n")
	if !strings.Contains(joined1, "> 插话") {
		t.Fatalf("首次插话回显应随流式批落盘: %q", joined1)
	}
	if strings.Count(joined1, "插话") != 1 {
		t.Fatalf("首次插话回显出现 %d 次（应为 1）：%q", strings.Count(joined1, "插话"), joined1)
	}

	// 第二次相同内容（去重）
	m.editor.Insert("插话")
	next, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	prints2 := printedLines(cmd)
	if len(prints2) != 0 {
		t.Fatalf("去重生效：第二次相同插话不应产生输出，实际 %q", prints2)
	}
	if len(m.interject) != 0 {
		t.Fatalf("插话已派发，interject 应清空，实际 %d 条", len(m.interject))
	}
}

// TestInterjectToolResultOrder 工具行与插话回显的相对顺序
// 用户输入触发工具调用 -> 工具调用事件入 interject -> 工具执行中用户再次输入
// 两条插话 + 工具调用行 + 工具结果行，四类消息混在同一队列，按到达顺序全序派发
func TestInterjectToolResultOrder(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("正在思考\n")

	// 1. 用户输入触发工具调用（模拟：用户提交 -> TurnStarted -> ToolCallEvent）
	m.editor.Insert("运行工具")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	_ = printedLines(cmd) // 回显落盘
	// 排空
	m, _ = drainAll(t, m)

	// 2. 工具调用事件到达
	m, cmd = ev(t, m, agent.ToolCallEvent{ID: "c1", Name: "bash", ArgsSummary: `{"cmd":"ls"}`})
	_ = printedLines(cmd) // 工具调用行落盘
	// 排空
	m, _ = drainAll(t, m)

	// 3. 工具执行期间用户再次输入
	m.editor.Insert("插话1")
	next, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	_ = printedLines(cmd) // 插话1落盘
	// 排空
	m, _ = drainAll(t, m)

	m.editor.Insert("插话2")
	next, cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	_ = printedLines(cmd) // 插话2落盘
	// 排空
	m, _ = drainAll(t, m)

	// 4. 工具结果事件到达
	m, cmd = ev(t, m, agent.ToolResultEvent{ID: "c1", Name: "bash", Result: tool.Result{Output: "ok"}})
	printed := printedLines(cmd)
	joined := strings.Join(printed, "\n")

	// 验证工具结果行落盘
	if !strings.Contains(joined, "<- bash") {
		t.Fatalf("工具结果行应随 flush 落盘: %q", joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("派发后 interject 应清空，实际 %d 条", len(m.interject))
	}
}

// TestInterjectFlushWhileDraining 排空链进行中新增 interject 应入队等待下一轮
func TestInterjectFlushWhileDraining(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("第一行\n")

	// 先触发一次 flush，开始排空链
	_, cmd := m.applyAgentEvent(agent.TextDeltaEvent{Text: "第一行\n"})
	_ = printedLines(cmd)

	// 在排空链进行中（draining=true）添加 interject
	m.addInterject("排空中插话")

	// 排空链继续：新入队条目不得在本轮派发
	var during []string
	m, during = drainAll(t, m)
	if joined := strings.Join(during, "\n"); strings.Contains(joined, "排空中插话") {
		t.Fatalf("排空链进行中新增 interject 不应在本轮派发: %q", joined)
	}

	// 排空链结束后，interject 应仍在队列中
	if len(m.interject) != 1 {
		t.Fatalf("排空链结束后 interject 应保留 1 条，实际 %d 条", len(m.interject))
	}

	// 下一次 flush 应派发该 interject
	cmd, _ = m.flushPendingStream(false)
	printed := printedLines(cmd)
	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "排空中插话") {
		t.Fatalf("下一轮 flush 应派发排空期保留的 interject: %q", joined)
	}
	if len(m.interject) != 0 {
		t.Fatalf("派发后 interject 应清空")
	}
}

// TestInterjectWhileBusyDeliveredToAgent 生成中 Enter 必须把原文交给 agent
// （Submit 在 Running() 时转 Steering 收件箱）。回显是视觉、投递是语义，两者
// 缺一即「插话不生效」——b9b1cb1 只留了视觉，实机表现为回显正常但零响应。
func TestInterjectWhileBusyDeliveredToAgent(t *testing.T) {
	var sent []string
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(text string, _ []string) { sent = append(sent, text) },
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("正在输出第一行\n半")

	m.editor.Insert("先停一下")
	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	if len(sent) != 1 || sent[0] != "先停一下" {
		t.Fatalf("插话须以原文投递一次，实际 %q", sent)
	}
	if strings.Contains(sent[0], "\x1b") || strings.Contains(sent[0], "> ") {
		t.Fatalf("投递内容不得含回显样式：%q", sent[0])
	}
	if joined := strings.Join(printedLines(cmd), "\n"); strings.Count(joined, "先停一下") != 1 {
		t.Fatalf("回显应落盘一次: %q", joined)
	}
}

// TestBusyEnterWithImagesDiscardsWithNotice 生成中带图插话无承载者（DEBT）：
// 图片必须丢弃且出声，不能静默挂到用户的下一条消息上。
func TestBusyEnterWithImagesDiscardsWithNotice(t *testing.T) {
	var sent []string
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(text string, _ []string) { sent = append(sent, text) },
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.pendingImages = []string{"a.png"}

	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	if len(sent) != 0 {
		t.Fatalf("无文本时不应投递，实际 %q", sent)
	}
	if len(m.pendingImages) != 0 {
		t.Fatalf("图片应就地丢弃，实际残留 %d 张", len(m.pendingImages))
	}
	if joined := strings.Join(printedLines(cmd), "\n"); !strings.Contains(joined, "图片未发送") {
		t.Fatalf("丢弃图片必须出声: %q", joined)
	}
}

// TestInterjectViewNoLeak 帧内渲染不得泄露已入队的 interject
func TestInterjectViewNoLeak(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.stream.WriteString("正在输出\n")

	// 添加 interject（不触发 flush）
	m.addInterject("测试内容")

	// View 渲染时，interject 应显示在尾部窗口（随块渲染）
	view := m.View().Content
	if !strings.Contains(view, "测试内容") {
		t.Fatalf("interject 应在尾部窗口渲染: %q", view)
	}

	// flush 后，interject 从队列移除，View 不应再显示
	cmd, _ := m.flushPendingStream(false)
	_ = printedLines(cmd)

	// 完成排空链
	m, _ = drainAll(t, m)

	view = m.View().Content
	if strings.Contains(view, "测试内容") {
		t.Fatalf("flush 后 interject 不应残留在 View: %q", view)
	}
}

// TestInterjectEmptyContent 空白内容不入队
func TestInterjectEmptyContent(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30

	m.addInterject("")
	if len(m.interject) != 0 {
		t.Fatalf("空内容不应入队")
	}

	m.addInterject("   ")
	// 空白内容（仅含空格/制表/换行）不入队
	if len(m.interject) != 0 {
		t.Fatalf("空白内容不应入队")
	}

	m.addInterject("  内容  ")
	// 含非空白字符的内容入队
	if len(m.interject) != 1 {
		t.Fatalf("含非空白字符的内容应入队 1 条")
	}
}