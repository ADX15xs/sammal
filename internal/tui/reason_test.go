package tui

import (
	"strings"
	"testing"
	"time"

	"sammal/internal/agent"
	"sammal/internal/provider"
)

// applyEvent 依次投递事件并返回更新后的 Model（headless 单测辅助）。
func applyEvent(t *testing.T, m Model, evs ...agent.Event) Model {
	t.Helper()
	for _, ev := range evs {
		next, _ := m.applyAgentEvent(ev)
		m = next.(Model)
	}
	return m
}

// 思考流式只展示最新一行；ReasonFinal 撤行；答案文本照常流入。
func TestReasoningStreamsSingleLineThenDrops(t *testing.T) {
	m := New(Deps{ModelName: "m"})
	m.width = 80
	m = applyEvent(t, m,
		agent.TurnStartedEvent{},
		agent.ReasonDeltaEvent{Text: "第一行思考"},
	)
	if !m.thinking {
		t.Fatal("thinking 应为 true")
	}
	lines := m.streamBlockLines(m.width)
	if len(lines) != 1 || !strings.Contains(lines[0], "第一行思考") {
		t.Fatalf("思考流式应单行含最新内容: %q", lines)
	}

	m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: "\n第二行思考"})
	if lines := m.streamBlockLines(m.width); len(lines) != 1 ||
		!strings.Contains(lines[0], "第二行思考") || strings.Contains(lines[0], "第一行") {
		t.Fatalf("应只显示当前行: %q", lines)
	}

	m = applyEvent(t, m,
		agent.ReasonFinalEvent{},
		agent.TextDeltaEvent{Text: "答案"},
	)
	if m.thinking {
		t.Error("ReasonFinal 后 thinking 应为 false")
	}
	if lines := m.streamBlockLines(m.width); len(lines) != 1 || lines[0] != "答案" {
		t.Fatalf("思考行应撤出视窗，仅剩答案: %q", lines)
	}
}

// 逐 token 增长：增量大多不含换行，当前行必须累积显示（而非只闪最后
// 一个 token）——回归 TestReasoningTokenGrowth 修的 bug。
func TestReasoningTokenGrowth(t *testing.T) {
	m := New(Deps{ModelName: "m"})
	m.width = 80
	m = applyEvent(t, m, agent.TurnStartedEvent{})

	tokens := []string{"用户", "在", "问", "天气", "，", "需要", "查询"}
	for _, tk := range tokens {
		m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: tk})
		lines := m.streamBlockLines(m.width)
		if len(lines) != 1 {
			t.Fatalf("token %q: 行数 = %d", tk, len(lines))
		}
		want := ""
		for _, prev := range tokens {
			want += prev
			if prev == tk {
				break
			}
		}
		if !strings.Contains(lines[0], want) {
			t.Fatalf("token %q: 应显示累积行 %q, got %q", tk, want, lines[0])
		}
	}

	// 换行闭合旧行，新行从空开始累积。
	m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: "\n"})
	if lines := m.streamBlockLines(m.width); strings.Contains(lines[0], "查询") {
		t.Fatalf("换行后不应残留上一行内容: %q", lines[0])
	}
	m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: "新的一行"})
	if lines := m.streamBlockLines(m.width); !strings.Contains(lines[0], "新的一行") {
		t.Fatalf("新行应正常累积: %q", lines[0])
	}
}

// 思考行带计时（turn 开始时刻起算）。
func TestReasoningLineShowsElapsed(t *testing.T) {
	m := New(Deps{ModelName: "m"})
	m.width = 80
	m = applyEvent(t, m,
		agent.TurnStartedEvent{},
		agent.ReasonDeltaEvent{Text: "想"},
	)
	// 人为推进起始时刻，避免测试依赖真实等待。
	m.turnStart = time.Now().Add(-75 * time.Second)
	lines := m.streamBlockLines(m.width)
	if len(lines) != 1 || !strings.Contains(lines[0], "1m15s") {
		t.Fatalf("思考行应含计时: %q", lines)
	}
}

// tailWindow 纯函数矩阵：尾部跟随窗口的确定性断言。
func TestTailWindow(t *testing.T) {
	// 不超宽：原样返回。
	if got := tailWindow("abc", 10); got != "abc" {
		t.Fatalf("不超宽应原样返回: %q", got)
	}
	// 超宽：精确取尾部 4 列。
	if got := tailWindow("abcdefgh", 4); got != "efgh" {
		t.Fatalf("超宽应取尾段: %q", got)
	}
	// CJK：宽度 4 = 两个汉字，窗口边缘不截半宽字。
	if got := tailWindow("思考文本测试", 4); got != "测试" {
		t.Fatalf("CJK 应按整字取尾: %q", got)
	}
	// CJK 与 ASCII 混排：3 列窗口从尾部贪心装填，考(2列)放不下则只取 ASCII。
	if got := tailWindow("ab思考cd", 3); got != "cd" {
		t.Fatalf("混排应按显示宽度从尾贪心取: %q", got)
	}
	// width<=0：空串。
	if got := tailWindow("abc", 0); got != "" {
		t.Fatalf("width=0 应返回空: %q", got)
	}
}

// 思考行超宽时尾部跟随：前缀（思考中）固定，正文保留最新 token，总宽不超终端。
func TestReasoningTailFollowsLatest(t *testing.T) {
	m := New(Deps{ModelName: "m"})
	m.width = 30
	m = applyEvent(t, m, agent.TurnStartedEvent{})

	// 分多个 token 流式到达，每个 token 后渲染都应以它结尾。
	tokens := []string{"思考", "进行", "中，", "最新", "内容", "在此"}
	for i, tk := range tokens {
		m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: tk})
		lines := m.streamBlockLines(m.width)
		if len(lines) != 1 {
			t.Fatalf("token %d: 应只有 1 行: %v", i, lines)
		}
		plain := stripANSI(lines[0])
		if !strings.HasPrefix(plain, "- 思考中") {
			t.Fatalf("token %d: 前缀应固定: %q", i, plain)
		}
		if !strings.HasSuffix(plain, tk) {
			t.Fatalf("token %d: 应跟随最新 token %q: %q", i, tk, plain)
		}
		if widthOf(plain) > m.width-1 {
			t.Fatalf("token %d: 总宽不应超终端: %d > %d: %q", i, widthOf(plain), m.width-1, plain)
		}
	}

	// 换行闭合旧行后窗口重开，新行从头累积。
	m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: "\n新行"})
	plain := stripANSI(m.streamBlockLines(m.width)[0])
	if !strings.HasSuffix(plain, "新行") || strings.Contains(plain, "在此") {
		t.Fatalf("换行后应只显示新行: %q", plain)
	}
}

// 工具环回归：思考行出现/消失与工具行打印交错时，View 行数稳定、无空行。
func TestReasoningToolLoopStable(t *testing.T) {
	m := New(Deps{ModelName: "m"})
	m.width = 40
	long := "这是一段很长的思考内容用于触发尾部跟随窗口的裁切逻辑"

	// 事件流：两轮「思考→定稿→工具调用→工具结果」。
	evs := []agent.Event{
		agent.TurnStartedEvent{},
		agent.ReasonDeltaEvent{Text: long},
		agent.ReasonFinalEvent{},
		agent.ToolCallEvent{ID: "1", Name: "read", ArgsSummary: "a.txt"},
		agent.ToolResultEvent{ID: "1", Name: "read"},
		agent.ReasonDeltaEvent{Text: long},
		agent.ReasonFinalEvent{},
		agent.ToolCallEvent{ID: "2", Name: "grep", ArgsSummary: "query"},
	}
	for i, ev := range evs {
		m = applyEvent(t, m, ev)
		lines := m.streamBlockLines(m.width)
		if len(lines) > 3 {
			t.Fatalf("事件 %d: View 行数超限: %v", i, lines)
		}
		for j, ln := range lines {
			if stripANSI(ln) == "" {
				t.Fatalf("事件 %d: 第 %d 行为空串", i, j)
			}
		}
	}
}

// MessageFinal 也更新 usage：多 step 工具环中 ctx% 随 step 刷新。
func TestUsageUpdatesAtMessageFinal(t *testing.T) {
	m := New(Deps{ModelName: "m", ContextWindow: 1000})
	m = applyEvent(t, m,
		agent.TurnStartedEvent{},
		agent.MessageFinalEvent{Text: "", Usage: &provider.Usage{PromptTokens: 500}},
	)
	if m.usage == nil || m.usage.PromptTokens != 500 {
		t.Fatalf("usage 未随 MessageFinal 更新: %+v", m.usage)
	}
	status := stripANSI(m.statusLine())
	if !strings.Contains(status, "ctx 50%") {
		t.Errorf("状态栏应含 ctx 50%%: %q", status)
	}
}

// ctx% 变色预警：≥70% 黄、≥80% 红、低于阈值无色。
func TestCtxPartColorThresholds(t *testing.T) {
	m := New(Deps{ModelName: "m", ContextWindow: 1000})

	m = applyEvent(t, m, agent.MessageFinalEvent{
		Text: "", Usage: &provider.Usage{PromptTokens: 650},
	})
	if s := m.ctxPart(); strings.Contains(s, ansiYellow) {
		t.Errorf("65%% 不应变黄: %q", s)
	}

	m.usage = &provider.Usage{PromptTokens: 750}
	if s := m.ctxPart(); !strings.Contains(s, ansiYellow) {
		t.Errorf("75%% 应变黄: %q", s)
	}

	m.usage = &provider.Usage{PromptTokens: 850}
	if s := m.ctxPart(); !strings.Contains(s, ansiRed) {
		t.Errorf("85%% 应变红: %q", s)
	}
}

// 窄终端下状态栏从右往左丢弃：工具数先丢，计时器随后，模型名永不丢。
func TestStatusLineDropOrder(t *testing.T) {
	m := New(Deps{ModelName: "very-long-model-name-xyz", ContextWindow: 100000})
	m.busy = true
	m.turnStart = time.Now()
	m.toolCalls = 3
	m.usage = &provider.Usage{PromptTokens: 1234, CompletionTokens: 567, CachedTokens: 100}

	// 宽度足够：全段都在。
	m.width = 200
	full := stripANSI(m.statusLine())
	for _, want := range []string{"工具 3", "in 1234 out 567", "cache"} {
		if !strings.Contains(full, want) {
			t.Errorf("宽终端缺段 %q: %q", want, full)
		}
	}

	// 极窄：只剩模型名 + 计时标记。
	m.width = 30
	narrow := stripANSI(m.statusLine())
	if !strings.Contains(narrow, "very-long-model-name-xyz") {
		t.Errorf("模型名不应被丢弃: %q", narrow)
	}
	if strings.Contains(narrow, "工具") {
		t.Errorf("窄终端应先丢工具数: %q", narrow)
	}

	// 空闲段（无 turnStart）的生成中标记永不被裁剪丢弃。
	m.busy = true
	m.turnStart = time.Time{}
	m.width = 16 // 只够 " model | * 生成中"
	idle := stripANSI(m.statusLine())
	if !strings.Contains(idle, "* 生成中") {
		t.Errorf("极窄下生成中标记不应被丢弃: %q", idle)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// 全状态矩阵：驱动 Model 走完 turn 生命周期的每个阶段，断言各阶段
// 状态栏的段构成（headless，无需真实终端）。
func TestStatusLineLifecycleMatrix(t *testing.T) {
	const win = 1000
	newModel := func() Model {
		return New(Deps{ModelName: "qwen3", ContextWindow: win})
	}
	wide := func(m Model) Model { m.width = 120; return m } // 全段可见的宽度

	// 阶段 1：冷启动（无 usage、空闲）——只有模型名。
	m := wide(newModel())
	if s := stripANSI(m.statusLine()); strings.TrimSpace(s) != "qwen3" {
		t.Fatalf("冷启动 = %q, 只要模型名", s)
	}

	// 阶段 2：Enter 后 TurnStarted 前——生成中标记（无计时）。
	m.busy = true
	s := stripANSI(m.statusLine())
	if !strings.Contains(s, "* 生成中") {
		t.Fatalf("Enter 后 = %q, 应含 * 生成中", s)
	}
	if strings.Contains(s, "ctx") || strings.Contains(s, "工具") {
		t.Fatalf("尚无数据不应出现 ctx/工具段: %q", s)
	}

	// 阶段 3：TurnStarted——计时器出现。
	m = applyEvent(t, m, agent.TurnStartedEvent{})
	if !strings.HasSuffix(stripANSI(m.statusLine()), "0s") {
		t.Fatalf("TurnStarted 后应显示计时: %q", stripANSI(m.statusLine()))
	}

	// 阶段 4：思考流式中——思考行带计时（视窗内），状态栏不变。
	m = applyEvent(t, m, agent.ReasonDeltaEvent{Text: "想"})
	lines := m.streamBlockLines(80)
	if len(lines) != 1 || !strings.Contains(lines[0], "想") {
		t.Fatalf("思考行缺失: %v", lines)
	}

	// 阶段 5：第一个 step 定稿——in/out 与 ctx 出现；工具数未出现。
	m = applyEvent(t, m,
		agent.MessageFinalEvent{Text: "", Usage: &provider.Usage{PromptTokens: 500}},
	)
	s = stripANSI(m.statusLine())
	for _, want := range []string{"in 500 out 0", "ctx 50%"} {
		if !strings.Contains(s, want) {
			t.Errorf("首 step 后缺 %q: %q", want, s)
		}
	}
	if strings.Contains(s, "工具") {
		t.Errorf("无工具调用不应有工具段: %q", s)
	}

	// 阶段 6：工具环中——工具计数随 ToolCallEvent 增长。
	m = applyEvent(t, m,
		agent.ToolCallEvent{Name: "read"},
		agent.ToolCallEvent{Name: "grep"},
	)
	if s := stripANSI(m.statusLine()); !strings.Contains(s, "工具 2") {
		t.Fatalf("两次调用后 = %q, 应含 工具 2", s)
	}
	// 工具环中途 ctx% 已按新 step 的 usage 刷新（MessageFinal 更新过）。
	m.usage = &provider.Usage{PromptTokens: 900}
	if s := m.ctxPart(); !strings.Contains(s, ansiRed) {
		t.Errorf("90%% 应红色预警: %q", s)
	}

	// 阶段 7：turn 结束——busy 段与工具段消失，usage 保留（阶段 6 设的 900）。
	m = applyEvent(t, m, agent.TurnEndedEvent{StopReason: agent.StopCompleted})
	s = stripANSI(m.statusLine())
	if strings.Contains(s, "生成中") || strings.Contains(s, "工具") || strings.HasSuffix(strings.TrimSpace(s), "0s") {
		t.Errorf("结束后仍残留 busy 段: %q", s)
	}
	if !strings.Contains(s, "ctx 90%") {
		t.Errorf("结束后 usage 应保留: %q", s)
	}

	// 阶段 8：切模型——usage 重置回纯模型名，等新窗口首个 usage。
	m = applyEvent(t, m, agent.ModelSwitchedEvent{Name: "glm4", Window: 2000})
	m = wide(m)
	if s := stripANSI(m.statusLine()); strings.TrimSpace(s) != "glm4" {
		t.Fatalf("切换后 = %q, usage 应重置", s)
	}
}
