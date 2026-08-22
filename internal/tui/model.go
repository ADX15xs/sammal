// Package tui 是 Bubble Tea v2 内联滚动前端：唯一的可变区是底部
// 输入框 + 当前流式块，定稿内容经 tea.Println 追加进原生滚动缓冲区
// （I4：单渲染路径，永不进 alt-screen）。
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"sammal/internal/agent"
	"sammal/internal/provider"
	"sammal/internal/tool"
)

// Deps 是 TUI 与 core 的全部接线：事件流订阅、发送、中止、slash 命令、
// 模型列表与外部编辑器。
type Deps struct {
	ModelName string
	Events    <-chan agent.Event
	Send      func(text string)
	Abort     func()
	Slash     func(text string) []string
	Models    func() []string
	EditorCmd func(path string) (*exec.Cmd, error)
	// StartupHints 启动即打印的提示行（滚动区常驻，如 api_key_env 缺失警告）。
	StartupHints []string
}

// popupKind 弹窗状态集中管理（第 6.7 节：避开 Reasonix 的 nil 链互斥）。
type popupKind int

const (
	popupNone popupKind = iota
	popupModelPicker
)

type Model struct {
	deps      Deps
	width     int
	input     InputLine
	busy      bool
	stream    strings.Builder // 当前流式块（可变区）
	thinking  bool
	usage     *provider.Usage
	modelName string

	history   []string
	histDepth int // 0 = 实时输入；>0 = 正在翻阅的第 N 条历史
	quitArmed bool

	popup            popupKind
	pickerSel        int
	inputBeforePopup string // Esc 关闭弹窗时还原
}

func New(deps Deps) Model {
	return Model{deps: deps, modelName: deps.ModelName}
}

// InputText 返回当前输入内容（测试用）。
func (m Model) InputText() string { return m.input.String() }

func (m Model) Init() tea.Cmd {
	if len(m.deps.StartupHints) > 0 {
		return tea.Batch(listenAgent(m.deps.Events),
			tea.Println(dim("⚠ "+strings.Join(m.deps.StartupHints, "\n  "))))
	}
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

	case editorDoneMsg:
		return m.editorDone(msg)

	case agentClosedMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.popup != popupNone {
		return m.handlePopupKey(msg)
	}
	if msg.Keystroke() == "ctrl+p" {
		return m.openModelPicker()
	}
	if msg.Keystroke() == "ctrl+e" {
		return m.openEditor()
	}
	if msg.Code == tea.KeyEnter {
		if m.input.Empty() {
			return m, nil
		}
		text := m.input.String()
		m.input.Clear()
		m.rememberInput(text)
		if strings.HasPrefix(text, "/") {
			lines := m.deps.Slash(text)
			return m, printLines(lines)
		}
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

// openModelPicker 打开 Ctrl+P 选择器：输入框转为过滤器，原输入 Esc 时还原。
func (m Model) openModelPicker() (tea.Model, tea.Cmd) {
	m.popup = popupModelPicker
	m.pickerSel = 0
	m.inputBeforePopup = m.input.String()
	m.input.Clear()
	return m, nil
}

func (m Model) handlePopupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEscape:
		m.popup = popupNone
		m.input.Clear()
		m.input.Insert(m.inputBeforePopup)
		return m, nil
	case msg.Code == tea.KeyUp:
		if m.pickerSel > 0 {
			m.pickerSel--
		}
		return m, nil
	case msg.Code == tea.KeyDown:
		if m.pickerSel < len(m.filteredModels())-1 {
			m.pickerSel++
		}
		return m, nil
	case msg.Code == tea.KeyEnter:
		models := m.filteredModels()
		if len(models) == 0 {
			return m, nil
		}
		chosen := models[min(m.pickerSel, len(models)-1)]
		m.popup = popupNone
		m.input.Clear()
		lines := m.deps.Slash("/model " + chosen)
		return m, printLines(lines)
	case msg.Code == tea.KeyBackspace:
		m.input.Backspace()
		m.pickerSel = 0
		return m, nil
	default:
		if s := msg.Text; s != "" {
			m.input.Insert(s)
			m.pickerSel = 0
		}
		return m, nil
	}
}

// filteredModels 按输入做子序列模糊过滤（大小写不敏感）。
func (m Model) filteredModels() []string {
	if m.deps.Models == nil {
		return nil
	}
	filter := m.input.String()
	var out []string
	for _, name := range m.deps.Models() {
		if fuzzyMatch(name, filter) {
			out = append(out, name)
		}
	}
	return out
}

func fuzzyMatch(s, sub string) bool {
	s, sub = strings.ToLower(s), strings.ToLower(sub)
	if sub == "" {
		return true
	}
	i := 0
	for j := 0; j < len(s) && i < len(sub); j++ {
		if s[j] == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

type editorDoneMsg struct {
	path string
	err  error
}

// openEditor 用 $VISUAL/$EDITOR 编辑当前输入（多行/粘贴大段的主路径）。
func (m Model) openEditor() (tea.Model, tea.Cmd) {
	if m.deps.EditorCmd == nil {
		return m, nil
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("sammal-%d.md", time.Now().UnixMilli()))
	if err := os.WriteFile(path, []byte(m.input.String()), 0o644); err != nil {
		return m, tea.Println(errStyle("临时文件创建失败：" + err.Error()))
	}
	cmd, err := m.deps.EditorCmd(path)
	if err != nil {
		os.Remove(path)
		return m, tea.Println(errStyle("未配置编辑器：" + err.Error()))
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

func (m Model) editorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		return m, tea.Println(errStyle("编辑器异常退出：" + msg.err.Error()))
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		return m, tea.Println(errStyle("读取编辑结果失败：" + err.Error()))
	}
	content := strings.TrimSuffix(string(data), "\n")
	if content == "" {
		return m, nil // 空内容视为放弃编辑
	}
	m.input.Clear()
	m.input.Insert(content)
	return m, nil
}

func printLines(lines []string) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	return tea.Println(strings.Join(lines, "\n"))
}

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

// applyAgentEvent 处理一个 agent 事件。每个事件后必须重新挂起监听
// （listenAgent 是一次性 Cmd），否则事件流在首个事件后中断。
func (m Model) applyAgentEvent(ev agent.Event) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
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

	case agent.ToolCallEvent:
		cmd = tea.Println(dim(fmt.Sprintf("-> %s %s", ev.Name, ev.ArgsSummary)))

	case agent.ToolResultEvent:
		cmd = tea.Println(dim(fmt.Sprintf("<- %s: %s", ev.Name, tool.ForTUI(ev.Result))))

	case agent.MessageFinalEvent:
		m.stream.Reset()
		m.thinking = false
		if ev.Interrupted {
			cmd = tea.Println(dim(renderInterrupted(ev.Text)))
		} else {
			cmd = tea.Println(ev.Text)
		}

	case agent.TurnEndedEvent:
		m.busy = false
		m.thinking = false
		if ev.Usage != nil {
			m.usage = ev.Usage
		}
		if ev.StopReason == agent.StopAborted {
			cmd = tea.Println(dim("（已中止）"))
		}

	case agent.StatusEvent:
		cmd = tea.Println(dim("| " + ev.Text))

	case agent.ModelSwitchedEvent:
		m.modelName = ev.Name

	case agent.ErrorEvent:
		cmd = tea.Println(errStyle(ev.Err.Error()))
	}
	return m, tea.Batch(listenAgent(m.deps.Events), cmd)
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
	if m.popup == popupModelPicker {
		lines = append(lines, m.pickerLines(width)...)
	}

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

// pickerLines 模型选择器列表（内嵌于可变区，自带模糊过滤，不依赖 fzf）。
func (m Model) pickerLines(width int) []string {
	lines := []string{dim(" 选择模型（输入过滤 | Enter 确认 | Esc 取消）")}
	models := m.filteredModels()
	const maxShown = 8
	for i, name := range models {
		if i >= maxShown {
			lines = append(lines, dim(fmt.Sprintf("   ... 共 %d 个", len(models))))
			break
		}
		mark := " "
		if name == m.modelName {
			mark = "*"
		}
		entry := " " + mark + " " + name
		if i == m.pickerSel {
			entry = ansiCyan + "> " + strings.TrimLeft(entry, " *") + ansiReset
		}
		lines = append(lines, clipLine(entry, width))
	}
	if len(models) == 0 {
		lines = append(lines, dim("   （无匹配模型）"))
	}
	return lines
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
