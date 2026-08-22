// Package tui 是 Bubble Tea v2 内联滚动前端：唯一的可变区是底部
// 输入框 + 当前流式块，定稿内容经 tea.Println 追加进原生滚动缓冲区
// （I4：单渲染路径，永不进 alt-screen）。
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"sammal/internal/agent"
	"sammal/internal/provider"
)

// Deps 是 TUI 与 core 的全部接线：事件流订阅、发送、中止。
type Deps struct {
	ModelName string
	Events    <-chan agent.Event
	Send      func(text string)
	Abort     func()
}

type Model struct {
	deps     Deps
	width    int
	input    InputLine
	busy     bool
	stream   strings.Builder // 当前流式块（可变区）
	thinking bool
	usage    *provider.Usage

	history   []string
	histDepth int // 0 = 实时输入；>0 = 正在翻阅的第 N 条历史
	quitArmed bool
}

func New(deps Deps) Model {
	return Model{deps: deps}
}

func (m Model) Init() tea.Cmd {
	return listenAgent(m.deps.Events)
}

type agentEventMsg struct{ ev agent.Event }
type agentClosedMsg struct{}

func listenAgent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return agentClosedMsg{}
		}
		return agentEventMsg{ev: ev}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.PasteMsg:
		m.input.Insert(msg.Content)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		return m.applyAgentEvent(msg.ev)

	case agentClosedMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.Code == tea.KeyEnter {
		if m.input.Empty() {
			return m, nil
		}
		text := m.input.String()
		m.input.Clear()
		m.rememberInput(text)
		m.busy = true
		m.stream.Reset()
		m.deps.Send(text)
		return m, tea.Println(renderUserEcho(text))
	}
	switch {
	case msg.Code == tea.KeyEscape:
		if m.busy {
			m.deps.Abort()
		}
	case msg.Keystroke() == "ctrl+c":
		if m.busy {
			m.deps.Abort()
			return m, nil
		}
		if m.quitArmed {
			return m, tea.Quit
		}
		if m.input.Empty() {
			return m, tea.Quit
		}
		m.input.Clear()
		m.quitArmed = true
		return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return quitArmExpiredMsg{}
		})
	case msg.Code == tea.KeyBackspace:
		m.input.Backspace()
	case msg.Code == tea.KeyDelete:
		m.input.Delete()
	case msg.Code == tea.KeyLeft:
		m.input.Left()
	case msg.Code == tea.KeyRight:
		m.input.Right()
	case msg.Code == tea.KeyHome:
		m.input.Home()
	case msg.Code == tea.KeyEnd:
		m.input.End()
	case msg.Code == tea.KeyUp:
		if m.input.Empty() && len(m.history) > 0 {
			m.histDepth = min(m.histDepth+1, len(m.history))
			m.loadHistory()
		}
	case msg.Code == tea.KeyDown:
		if m.histDepth > 0 {
			m.histDepth--
			m.loadHistory()
		}
	default:
		if s := msg.Text; s != "" {
			m.input.Insert(s)
		}
	}
	m.quitArmed = false
	return m, nil
}

type quitArmExpiredMsg struct{}

func (m Model) loadHistory() {
	if m.histDepth == 0 {
		m.input.Clear()
		return
	}
	m.input.Clear()
	m.input.Insert(m.history[len(m.history)-m.histDepth])
}

func (m Model) rememberInput(text string) {
	if n := len(m.history); n > 0 && m.history[n-1] == text {
		return
	}
	m.history = append(m.history, text)
	m.histDepth = 0
}

func (m Model) applyAgentEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	switch ev := ev.(type) {
	case agent.TurnStartedEvent:
		m.busy = true

	case agent.TextDeltaEvent:
		m.stream.WriteString(ev.Text)

	case agent.ReasonDeltaEvent:
		m.thinking = true

	case agent.StreamRestartedEvent:
		m.stream.Reset()
		m.thinking = false

	case agent.MessageFinalEvent:
		m.stream.Reset()
		m.thinking = false
		if ev.Interrupted {
			return m, tea.Println(dim(renderInterrupted(ev.Text)))
		}
		return m, tea.Println(ev.Text)

	case agent.TurnEndedEvent:
		m.busy = false
		m.thinking = false
		if ev.Usage != nil {
			m.usage = ev.Usage
		}
		switch ev.StopReason {
		case agent.StopAborted:
			return m, tea.Println(dim("（已中止）"))
		case agent.StopError:
			return m, nil
		}

	case agent.StatusEvent:
		return m, tea.Println(dim("· " + ev.Text))

	case agent.ErrorEvent:
		return m, tea.Println(errStyle(ev.Err.Error()))
	}
	return m, nil
}

const (
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
	ansiRed   = "\x1b[31m"
	ansiReset = "\x1b[0m"
)

func dim(s string) string      { return ansiDim + s + ansiReset }
func errStyle(s string) string { return ansiRed + s + ansiReset }

// renderUserEcho 用户消息的滚动区回显：首行加前缀，续行原样。
func renderUserEcho(text string) string {
	lines := strings.Split(text, "\n")
	lines[0] = ansiCyan + "> " + lines[0] + ansiReset
	return strings.Join(lines, "\n")
}

func renderInterrupted(text string) string {
	if text == "" {
		return "（生成中断，内容未定稿）"
	}
	return text + "\n" + dim("（以上内容被中断）")
}

// View 渲染唯一可变区：当前流式块 → 状态行 → 输入行。
// 光标用终端原生 bar 形状绘制（CJK 安全：不遮盖双宽字素）。
func (m Model) View() tea.View {
	width := m.width
	if width < 20 {
		width = 20
	}

	var lines []string
	lines = append(lines, m.streamBlockLines(width)...)

	status := m.statusLine()
	lines = append(lines, status)

	prompt := "> "
	if m.busy {
		prompt = "> "
	}
	display, cursorCol := m.input.Render(prompt, width)
	lines = append(lines, display)

	v := tea.NewView(strings.Join(lines, "\n"))
	v.Cursor = &tea.Cursor{
		Position: tea.Position{X: cursorCol, Y: len(lines) - 1},
		Shape:    tea.CursorBar,
		Blink:    true,
	}
	return v
}

// streamBlockLines 当前流式块的尾部若干行（原地整块重绘是显式策略）。
func (m Model) streamBlockLines(width int) []string {
	var lines []string
	if m.thinking {
		lines = append(lines, dim("- 思考中..."))
	}
	text := m.stream.String()
	if text == "" {
		return lines
	}
	all := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	const maxLines = 8
	tail := all
	ellipsis := false
	if len(tail) > maxLines {
		tail = tail[len(tail)-maxLines:]
		ellipsis = true
	}
	if ellipsis {
		lines = append(lines, dim("  ..."))
	}
	for _, ln := range tail {
		lines = append(lines, clipLine(ln, width))
	}
	return lines
}

func (m Model) statusLine() string {
	parts := []string{m.deps.ModelName}
	if m.usage != nil {
		parts = append(parts, fmt.Sprintf("in %d out %d", m.usage.PromptTokens, m.usage.CompletionTokens))
		if r := m.usage.CacheHitRatio(); r >= 0 {
			parts = append(parts, fmt.Sprintf("cache %d%%", int(r*100)))
		}
	}
	if m.busy {
		parts = append(parts, "* 生成中")
	}
	return dim(" " + strings.Join(parts, " | "))
}

// clipLine 超宽行截断到 width（按显示宽度，避免宽字符截半）。
func clipLine(s string, width int) string {
	if width <= 1 {
		return ""
	}
	var b strings.Builder
	used := 0
	state := -1
	for len(s) > 0 {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		w := widthOf(cluster)
		if used+w > width-1 {
			break
		}
		b.WriteString(cluster)
		used += w
	}
	if b.Len() == 0 && s != "" {
		return ""
	}
	if s != "" {
		b.WriteString("...")
	}
	return b.String()
}
