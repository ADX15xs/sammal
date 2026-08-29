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
	"sammal/internal/compaction"
	"sammal/internal/provider"
	"sammal/internal/tool"
)

// Deps 是 TUI 与 core 的全部接线：事件流订阅、发送、中止、slash 命令、
// 模型列表与外部编辑器。
type Deps struct {
	ModelName string
	Events    <-chan agent.Event
	Send      func(text string, images []string)
	Abort     func()
	Slash     func(text string) []string
	Models    func() []string
	EditorCmd func(path string) (*exec.Cmd, error)
	// ContextWindow 当前模型的上下文窗口（token）；0 = 未知，状态栏不显示
	// ctx 百分比。
	ContextWindow int
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
	stream    *strings.Builder // 当前流式块（可变区）
	thinking  bool
	reason    strings.Builder // 思考累积文本（定稿即弃，只取最新行渲染）
	reasonCur string          // 当前未闭合行的缓存（增量里无换行时也要能显示）
	usage     *provider.Usage
	modelName string

	turnStart    time.Time // 当前 turn 开始时刻（0 = 无进行中 turn）
	toolCalls    int       // 本轮已执行的工具调用数（生成中显示）
	windowTokens int       // 上下文窗口大小（0 = 不显示 ctx%）
	ctxWarned    bool      // ctx ≥ 压缩阈值时只告警一次
	tickArmed    bool      // 心跳去重：至多一个未触发的 turnTick

	history   []string
	histDepth int // 0 = 实时输入；>0 = 正在翻阅的第 N 条历史
	quitArmed bool

	pendingImages []string // 本次提交待携带的图片路径（/attach 累积，Submit 后清空）

	popup            popupKind
	pickerSel        int
	inputBeforePopup string // Esc 关闭弹窗时还原
}

func New(deps Deps) Model {
	return Model{deps: deps, modelName: deps.ModelName, stream: &strings.Builder{}, windowTokens: deps.ContextWindow}
}

// InputText 返回当前输入内容（测试用）。
func (m Model) InputText() string { return m.input.String() }

func (m Model) Init() tea.Cmd {
	if len(m.deps.StartupHints) > 0 {
		return tea.Batch(listenAgent(m.deps.Events),
			tea.Println(dim("[!] "+strings.Join(m.deps.StartupHints, "\n  "))))
	}
	return listenAgent(m.deps.Events)
}

type agentEventMsg struct{ ev agent.Event }
type agentClosedMsg struct{}
type turnTickMsg struct{} // 生成中的每秒心跳：刷新计时器/状态栏

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

	case turnTickMsg:
		return m.turnTick()

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
		if m.input.Empty() && len(m.pendingImages) == 0 {
			return m, nil
		}
		text := m.input.String()
		m.input.Clear()
		if text != "" {
			m.rememberInput(text)
		}
		if strings.HasPrefix(text, "/") {
			if lines, handled := m.handleSlash(text); handled {
				return m, printLines(lines)
			}
			lines := m.deps.Slash(text)
			return m, printLines(lines)
		}
		imgs := m.pendingImages
		m.pendingImages = nil
		m.busy = true
		m.stream.Reset()
		m.deps.Send(text, imgs)
		prompt := renderUserEcho(text)
		if len(imgs) > 0 {
			prompt += "\n" + dim(fmt.Sprintf("  📎 %s", imagesSummary(imgs)))
		}
		return m, tea.Println(prompt)
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

// handleSlash 处理不需要 agent 参与的 TUI 专属斜杠命令，返回 (输出行, 已处理)。
// /attach 是典型例子：它操作 TUI 的 pendingImages，无需请求模型。
func (m *Model) handleSlash(input string) ([]string, bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil, false
	}
	switch fields[0] {
	case "/attach":
		return m.slashAttach(fields), true
	}
	return nil, false
}

// slashAttach 处理 /attach 命令：
// - /attach <path...>  注册图片路径（校验扩展名 + 文件存在）
// - /attach（无参数）  列出当前 pending 图片
// - /attach -clear     清空 pending 图片
func (m *Model) slashAttach(fields []string) []string {
	if len(fields) == 1 {
		return m.listPendingImages()
	}
	switch fields[1] {
	case "-clear":
		m.pendingImages = nil
		return []string{"已清空所有待发送图片"}
	default:
		var ok, bad int
		for _, p := range fields[1:] {
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" && ext != ".webp" {
				bad++
				continue
			}
			if _, err := os.Stat(p); err != nil {
				bad++
				continue
			}
			m.pendingImages = append(m.pendingImages, p)
			ok++
		}
		lines := []string{}
		if ok > 0 {
			lines = append(lines, fmt.Sprintf("已添加 %d 张图片", ok))
		}
		if bad > 0 {
			lines = append(lines, fmt.Sprintf("%d 个路径无效（扩展名不支持或文件不存在）", bad))
		}
		return lines
	}
}

func (m *Model) listPendingImages() []string {
	if len(m.pendingImages) == 0 {
		return []string{"暂无待发送图片"}
	}
	lines := []string{"待发送图片："}
	for _, p := range m.pendingImages {
		if info, err := os.Stat(p); err == nil {
			lines = append(lines, fmt.Sprintf("  %s（%s）", filepath.Base(p), humanBytes(info.Size())))
		} else {
			lines = append(lines, "  "+filepath.Base(p))
		}
	}
	return lines
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
	}
}

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
		m.turnStart = time.Now()
		m.toolCalls = 0
		m.ctxWarned = false
		cmd = m.armTick()

	case agent.TextDeltaEvent:
		if m.thinking {
			// 文本开始：思考行即刻让位（ReasonFinal 已保证，此处兜底）。
			m.thinking = false
			m.resetReason()
		}
		m.stream.WriteString(ev.Text)

	case agent.ReasonDeltaEvent:
		m.thinking = true
		m.appendReason(ev.Text)
		cmd = m.armTick()

	case agent.ReasonFinalEvent:
		m.thinking = false
		m.resetReason()

	case agent.StreamRestartedEvent:
		m.stream.Reset()
		m.thinking = false
		m.resetReason()

	case agent.ToolCallEvent:
		m.toolCalls++
		cmd = tea.Println(dim(fmt.Sprintf("-> %s %s", ev.Name, ev.ArgsSummary)))

	case agent.ToolResultEvent:
		cmd = tea.Println(dim(fmt.Sprintf("<- %s: %s", ev.Name, tool.ForTUI(ev.Result))))

	case agent.MessageFinalEvent:
		m.stream.Reset()
		m.thinking = false
		m.resetReason()
		if ev.Usage != nil {
			m.usage = ev.Usage // 多 step 工具环中 ctx% 随每个 step 更新
		}
		if ev.Interrupted {
			cmd = tea.Println(dim(renderInterrupted(ev.Text)))
		} else {
			cmd = tea.Println(ev.Text)
		}

	case agent.TurnEndedEvent:
		m.busy = false
		m.thinking = false
		m.resetReason()
		m.turnStart = time.Time{}
		if ev.Usage != nil {
			m.usage = ev.Usage
		}
		var warn tea.Cmd
		if ev.StopReason != agent.StopAborted {
			warn = m.warnContextPressure()
		}
		if ev.StopReason == agent.StopAborted {
			cmd = tea.Println(dim("（已中止）"))
		}
		cmd = tea.Batch(listenAgent(m.deps.Events), cmd, warn)
		return m, cmd

	case agent.StatusEvent:
		cmd = tea.Println(dim("| " + ev.Text))

	case agent.ModelSwitchedEvent:
		m.modelName = ev.Name
		m.windowTokens = ev.Window
		m.usage = nil // 新窗口下旧百分比无意义，等首个 usage 重建
		m.ctxWarned = false

	case agent.ErrorEvent:
		cmd = tea.Println(errStyle(ev.Err.Error()))
	}
	return m, tea.Batch(listenAgent(m.deps.Events), cmd)
}

// appendReason 追加思考增量并维护「当前行」缓存：增量按 token 到达，
// 大多不含换行——只有换行时才把缓存行落定、开新行。渲染取当前行，
// 所以同一行内的每个 token 都能看到流畅的逐词增长（而非只闪最后一个）。
func (m *Model) appendReason(delta string) {
	for {
		if i := strings.IndexByte(delta, '\n'); i >= 0 {
			m.reasonCur = "" // 行闭合，开新行
			delta = delta[i+1:]
			continue
		}
		m.reasonCur += delta
		return
	}
}

func (m *Model) resetReason() {
	m.reason.Reset()
	m.reasonCur = ""
}

// reasonLine 当前正在书写的思考行（无内容时回退到累积文本的最后一行，
// 覆盖首块增量前有历史行的边界）。
func (m Model) reasonLine() string {
	if m.reasonCur != "" {
		return m.reasonCur
	}
	return lastLine(m.reason.String())
}

// armTick 生成中挂一个每秒心跳，驱动计时器与状态栏刷新。tickArmed 去重：
// 事件高频到达时（每个 reasoning 增量都会尝试挂表），保证任意时刻至多
// 一个未触发的 tick，否则定时器指数堆积。
func (m Model) armTick() tea.Cmd {
	if !m.busy || m.tickArmed {
		return nil
	}
	m.tickArmed = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return turnTickMsg{} })
}

// turnTick 心跳：busy 期间自续，空闲时终止。思考行的刷新由 token 到达
// 驱动（尾部跟随）；思考期间勿在此加更高频的定时重绘——会与 tea.Println
// 交错产生空行 artifact。
func (m Model) turnTick() (tea.Model, tea.Cmd) {
	m.tickArmed = false
	if !m.busy {
		return m, nil
	}
	return m, m.armTick()
}

// warnContextPressure 投影逼近压缩触发线时提示一次（把「下一轮要压缩、
// 会变慢且缓存重建」提前解释给用户）。TurnEnded 时判定，一轮只报一次。
func (m Model) warnContextPressure() tea.Cmd {
	if m.windowTokens <= 0 || m.usage == nil {
		return nil
	}
	if r := float64(m.usage.PromptTokens) / float64(m.windowTokens); r >= compaction.TriggerRatio && !m.ctxWarned {
		m.ctxWarned = true
		return tea.Println(dim(fmt.Sprintf("| 上下文已达窗口 %d%%（压缩触发线 %d%%）：下一轮可能自动压缩并重建 KV 缓存",
			int(r*100), int(compaction.TriggerRatio*100))))
	}
	return nil
}

// lastLine 返回文本最后一个非空行（思考流式只展示最新一行）。
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

const (
	ansiDim    = "\x1b[2m"
	ansiCyan   = "\x1b[36m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiReset  = "\x1b[0m"

	// warnRatio ctx% 变黄预警线（压缩触发线 compaction.TriggerRatio 变红）。
	warnRatio = 0.7
)

func dim(s string) string      { return ansiDim + s + ansiReset }
func errStyle(s string) string { return ansiRed + s + ansiReset }

// renderUserEcho 用户消息的滚动区回显：首行加前缀，续行原样。
func renderUserEcho(text string) string {
	lines := strings.Split(text, "\n")
	lines[0] = ansiCyan + "> " + lines[0] + ansiReset
	return strings.Join(lines, "\n")
}

// imagesSummary 把图片路径集合渲染为简短摘要（文件名 + 数量）。
func imagesSummary(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return filepath.Base(paths[0])
	}
	return fmt.Sprintf("%s +%d more", filepath.Base(paths[0]), len(paths)-1)
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
		// 思考只展示最新一行（dsh 方案）：恒定一行的自绘面 + 跳动的计时
		// 数字就是「还活着」的口子。定稿即从视窗丢弃，全文在日志里。
		line := "- 思考中"
		if m.turnStart.After(time.Time{}) {
			line = fmt.Sprintf("- 思考中 %s", formatElapsed(time.Since(m.turnStart)))
		}
		if cur := m.reasonLine(); cur != "" {
			// 前缀（"思考中 12s"）固定做锚点，正文超宽走尾部跟随：
			// 最新 token 永远可见、无省略号。不做定时动画（见 turnTick）。
			prefix := line + " | "
			body := strings.ReplaceAll(cur, "\t", " ")
			if bodyW := width - 1 - widthOf(prefix); bodyW > 0 {
				lines = append(lines, dim(prefix+tailWindow(body, bodyW)))
			} else {
				lines = append(lines, dim(clipLine(prefix+body, width-1)))
			}
		} else {
			lines = append(lines, dim(clipLine(line, width-1)))
		}
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

// formatElapsed 把时长渲染为紧凑形式（38s / 2m14s / 1h02m）。
func formatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

// statusSeg 是状态栏的一个显示段。
type statusSeg struct {
	text string // 已着色的最终文本
	pri  int    // 丢弃优先级：越大越先丢；负值 = 永不丢（模型名、生成中标记）
}

// statusLine 状态栏。空间不足时按优先级从右往左丢弃：ctx → in/out →
// cache → 计时器 → 工具数（模型名与生成中标记永不丢）。
func (m Model) statusLine() string {
	segs := []statusSeg{{text: m.modelName}}
	if m.usage != nil {
		segs = append(segs,
			statusSeg{text: fmt.Sprintf("in %d out %d", m.usage.PromptTokens, m.usage.CompletionTokens), pri: 2},
			statusSeg{text: m.cachePart(), pri: 3},
			statusSeg{text: m.ctxPart(), pri: 1},
		)
	}
	if m.busy && m.toolCalls > 0 {
		segs = append(segs, statusSeg{text: fmt.Sprintf("工具 %d", m.toolCalls), pri: 5})
	}
	if m.busy && m.turnStart.After(time.Time{}) {
		segs = append(segs, statusSeg{text: "* " + formatElapsed(time.Since(m.turnStart)), pri: 4})
	} else if m.busy {
		segs = append(segs, statusSeg{text: "* 生成中", pri: -1}) // 负优先级 = 永不丢
	}

	width := m.width
	if width < 20 {
		width = 20
	}
	const separator = " | "
	segs = dropToFit(segs, width-3) // 行首空格 + 安全边距
	return dim(" " + strings.Join(segTexts(segs), separator))
}

// dropToFit 超预算时按优先级从右往左逐段丢弃（负优先级段不可丢），
// 直到塞下或只剩不可丢段——宁可溢出不丢语义。
func dropToFit(segs []statusSeg, budget int) []statusSeg {
	const separator = " | "
	for widthOf(strings.Join(segTexts(segs), separator)) > budget && len(segs) > 1 {
		drop := -1
		for i := len(segs) - 1; i >= 1; i-- { // segs[0] 模型名永不丢；并列时丢更靠右的
			if segs[i].pri < 0 {
				continue // 负优先级段不可丢弃
			}
			if drop == -1 || segs[i].pri >= segs[drop].pri {
				drop = i
			}
		}
		if drop == -1 {
			break
		}
		segs = append(segs[:drop], segs[drop+1:]...)
	}
	return segs
}

func segTexts(segs []statusSeg) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.text
	}
	return out
}

// cachePart 缓存命中率段（无数据返回空串）。
func (m Model) cachePart() string {
	if r := m.usage.CacheHitRatio(); r >= 0 {
		return fmt.Sprintf("cache %d%%", int(r*100))
	}
	return ""
}

// ctxPart 上下文窗口占用百分比；逼近压缩触发线时变色预警。
// 无 usage 或未知窗口时返回空串（调用方过滤）。
// 变色段自带完整包裹（color+reset），嵌入外层 dim 文本时会终止 dim——
// 有意为之：预警色必须盖过 dim。
func (m Model) ctxPart() string {
	if m.windowTokens <= 0 || m.usage == nil || m.usage.PromptTokens <= 0 {
		return ""
	}
	r := float64(m.usage.PromptTokens) / float64(m.windowTokens)
	pct := fmt.Sprintf("ctx %d%%", int(r*100))
	switch {
	case r >= compaction.TriggerRatio:
		return ansiRed + pct + ansiReset
	case r >= warnRatio:
		return ansiYellow + pct + ansiReset
	default:
		return pct
	}
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

// clustersOf 按字素簇切分文本（与 clipLine 同一宽度语义，宽字符/组合字符
// 不会被切半）。
func clustersOf(text string) []string {
	var out []string
	state := -1
	rest := text
	for len(rest) > 0 {
		var c string
		c, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		out = append(out, c)
	}
	return out
}

// tailWindow 取 text 尾部至多 width 显示宽的片段——dsh 思考行的尾部跟随
// （overflow:hidden + scrollLeft 推到最右）：永远显示最新 token，超宽时
// 头部被裁掉、无省略号（running 态 text-overflow: clip）。文本不超宽时
// 原样返回。前缀计时进位会让窗口宽度每秒 ±1 列，属可接受的轻微抖动。
func tailWindow(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if widthOf(text) <= width {
		return text
	}
	clusters := clustersOf(text)
	used, start := 0, len(clusters)
	for start > 0 && used+widthOf(clusters[start-1]) <= width {
		start--
		used += widthOf(clusters[start])
	}
	return strings.Join(clusters[start:], "")
}
