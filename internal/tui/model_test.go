package tui

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
)

// headless 冒烟：验证 按键 → Send → 事件流订阅 → 定稿 的装配链路。
func TestTUISmokeHeadless(t *testing.T) {
	events := make(chan agent.Event, 16)
	var mu sync.Mutex
	var sent []string

	deps := Deps{
		ModelName: "test-model",
		Events:    events,
		Send: func(text string, images []string) {
			mu.Lock()
			sent = append(sent, text)
			mu.Unlock()
			events <- agent.TurnStartedEvent{}
			events <- agent.TextDeltaEvent{Text: "回"}
			events <- agent.MessageFinalEvent{Text: "回复内容"}
			events <- agent.TurnEndedEvent{StopReason: agent.StopCompleted, Usage: nil}
		},
		Abort: func() {},
	}

	p := tea.NewProgram(New(deps),
		tea.WithInput(strings.NewReader("hi\r")),
		tea.WithOutput(io.Discard),
	)
	done := make(chan struct{})
	go func() {
		p.Run()
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		var got string
		if n > 0 {
			got = sent[0]
		}
		mu.Unlock()
		if n == 1 && got == "hi" {
			break
		}
		if n > 1 {
			t.Fatalf("sent = %v, 期望单条", sent)
		}
		select {
		case <-deadline:
			t.Fatalf("Send 未被调用，sent = %v", sent)
		case <-time.After(20 * time.Millisecond):
		}
	}

	p.Quit()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("program 未退出")
	}
}

// 防回归：UI 固定件（输入框、状态行、列表前缀、提示标记）不得使用
// 东亚二义宽度字符——同一字符在宽/窄终端渲染列宽不同，光标与换行
// 计算会错位（本次曾被 ⚠ 违反，见 git 历史）。中文内容文案不受限，
// 列表只含横排固定的 UI 符号。
func TestNoAmbiguousUIGlyphs(t *testing.T) {
	violations := []string{}
	for _, file := range []string{"model.go", "input.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range data {
			if _, bad := map[rune]bool{'⚠': true, '›': true, '⋮': true, '●': true, '│': true, '❯': true, '⏎': true}[rune(r)]; bad {
				violations = append(violations, file+": "+string(r))
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("UI 固定件含二义宽度字符: %v", violations)
	}
}
