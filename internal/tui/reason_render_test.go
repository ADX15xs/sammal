package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
	"sammal/internal/tool"
)

// 端到端回归：真实 program 渲染「长思考（尾部跟随）→ 工具环 → 再思考」
// 事件流，思考行随 token 出现/消失并与工具行 tea.Println 交错。任何定时
// 动画重绘加入思考阶段都会在此产生 \r\n\r\n 空行 artifact（loop-marquee
// 与扫光两版均实测回归过）——本测试防止这类实现再被引入。
func TestReasoningRenderArtifactFree(t *testing.T) {
	evs := make(chan agent.Event, 64)
	m := New(Deps{
		ModelName: "qwen3:8b",
		Events:    evs,
		Send:      func(string) {},
		Abort:     func() {},
		Slash:     func(string) []string { return nil },
		Models:    func() []string { return nil },
	})
	m.width = 100

	var buf bytes.Buffer
	p := tea.NewProgram(m,
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(&buf),
		tea.WithWindowSize(100, 24),
	)
	go func() {
		evs <- agent.TurnStartedEvent{}
		time.Sleep(300 * time.Millisecond)
		evs <- agent.ReasonDeltaEvent{Text: strings.Repeat("非常", 30) + "长的思考文本用于测试尾部跟随"}
		time.Sleep(600 * time.Millisecond)
		evs <- agent.ReasonFinalEvent{}
		time.Sleep(100 * time.Millisecond)
		evs <- agent.ToolCallEvent{ID: "1", Name: "read", ArgsSummary: "a.txt"}
		time.Sleep(100 * time.Millisecond)
		evs <- agent.ToolResultEvent{ID: "1", Name: "read", Result: tool.Result{Output: "文件内容"}}
		time.Sleep(600 * time.Millisecond)
		evs <- agent.ReasonDeltaEvent{Text: "第二段思考，同样足够长以触发尾部跟随窗口的裁切"}
		time.Sleep(600 * time.Millisecond)
		evs <- agent.ReasonFinalEvent{}
		time.Sleep(100 * time.Millisecond)
		evs <- agent.ToolCallEvent{ID: "2", Name: "grep", ArgsSummary: "query"}
		time.Sleep(200 * time.Millisecond)
		close(evs)
	}()
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		p.Quit()
		t.Fatal("program 未退出")
	}

	out := buf.String()
	for _, pat := range []string{"\r\n\r\n", "\n\n", "\r\r"} {
		if n := strings.Count(out, pat); n > 0 {
			t.Errorf("检测到空行 artifact %q（%d 次）", pat, n)
		}
	}
	// 工具调用行经 insertAbove 一次性整行写入，应完整存在（事件流完整）。
	// 注意不能断言状态栏文字：cursed diff 渲染会把连续文字拆进多个写入段。
	for _, want := range []string{"-> read a.txt", "<- read:", "-> grep query"} {
		if !strings.Contains(out, want) {
			t.Errorf("工具行 %q 缺失，事件流未完整渲染", want)
		}
	}
}
