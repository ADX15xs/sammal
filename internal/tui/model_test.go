package tui

import (
	"io"
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
		Send: func(text string) {
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
