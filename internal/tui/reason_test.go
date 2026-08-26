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
		t.Fatalf("应只显示最新一行: %q", lines)
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
