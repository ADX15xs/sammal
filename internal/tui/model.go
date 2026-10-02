// Package tui 是 Bubble Tea v2 内联滚动前端：唯一的可变区是底部
// 输入框 + 当前流式块尾部窗口，内容经 scroll.go 的安全落盘路径渐进追加进
// 原生滚动缓冲区（I4：单渲染路径，永不进 alt-screen）。
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
	"sammal/internal/human"
	"sammal/internal/provider"
	"sammal/internal/skill"
	"sammal/internal/tool"
)

// Deps 是 TUI 与 core 的全部接线：事件流订阅、发送、中止、slash 命令、
// 模型列表、skill 列表与外部编辑器。
type Deps struct {
	ModelName string
	Events    <-chan agent.Event
	Send      func(text string, images []string)
	Abort     func()
	Slash     func(text string) []string
	Models    func() []string
	// Skills 返回可用 skill 列表（/skill 命令与选择器的数据源，每次调用
	// 现扫现显）；nil = 无 skill（SPEC 6.10）。
	Skills    func() []skill.Skill
	EditorCmd func(path string) (*exec.Cmd, error)
	// ContextWindow 当前模型的上下文窗口（token）；0 = 未知，状态栏不显示
	// ctx 百分比。
	ContextWindow int
	// StartupHints 启动即打印的提示行（滚动区常驻，如 api_key_env 缺失警告）。
	StartupHints []string
}

type Model struct {
	deps          Deps
	width         int
	height        int
	editor        Editor
	busy          bool
	stream        *strings.Builder // 当前流式块未落盘尾部（闭合行 + 半行，可变区）
	streamPrinted bool             // 本条消息已有内容落进滚动缓冲区（重试作废标记的依据）
	thinking      bool
	reason        strings.Builder // 思考累积文本（定稿即弃，只取最新行渲染）
	reasonCur     string          // 当前未闭合行的缓存（增量里无换行时也要能显示）
	usage         *provider.Usage
	modelName     string

	turnStart    time.Time // 当前 turn 开始时刻（0 = 无进行中 turn）
	toolCalls    int       // 本轮已执行的工具调用数（生成中显示）
	windowTokens int       // 上下文窗口大小（0 = 不显示 ctx%）
	ctxWarned    bool      // ctx ≥ 压缩阈值时只告警一次
	tickArmed    bool      // 心跳去重：至多一个未触发的 turnTick
	tickN        int       // 心跳计数：驱动等待期 spinner 帧

	history   []string
	histDepth int // 0 = 实时输入；>0 = 正在翻阅的第 N 条历史
	quitArmed bool

	pendingImages []string // 本次提交待携带的图片路径（/attach 累积，Submit 后清空）

	popup            popupKind
	pickerSel        int
	pickerOffset     int    // 选择器可视窗口起始下标（>maxShown 项时跟随选中滚动）
	inputBeforePopup string // Esc 关闭弹窗时还原
}

func New(deps Deps) Model {
	return Model{deps: deps, modelName: deps.ModelName, stream: &strings.Builder{}, windowTokens: deps.ContextWindow, editor: NewEditor()}
}

// InputText 返回当前输入内容（测试用）。
func (m Model) InputText() string { return m.editor.Text() }

func (m Model) Init() tea.Cmd {
	if len(m.deps.StartupHints) > 0 {
		return tea.Batch(listenAgent(m.deps.Events),
			m.printScroll(dim("[!] "+strings.Join(m.deps.StartupHints, "\n  "))))
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
		m.height = msg.Height
		return m, nil

	case tea.PasteMsg:
		m.editor.InsertPaste(msg.Content)
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
		if m.editor.Empty() && len(m.pendingImages) == 0 {
			return m, nil
		}
		text := m.editor.Text()
		m.editor.Clear()
		if text != "" {
			m.rememberInput(text)
		}
		echo := text
		if strings.HasPrefix(text, "/") {
			switch cmd, expanded, out := m.slashSkill(text); cmd {
			case skillPickerOpen:
				return m, nil
			case skillShow:
				return m, m.printLines(out)
			case skillSend:
				text = expanded // 展开正文走普通发送路径，回显仍显示原命令
			default:
				if lines, handled := m.handleSlash(text); handled {
					return m, m.printLines(lines)
				}
				return m, m.printLines(m.deps.Slash(text))
			}
		}
		imgs := m.pendingImages
		m.pendingImages = nil
		m.busy = true
		m.stream.Reset()
		m.deps.Send(text, imgs)
		prompt := renderUserEcho(echo)
		if len(imgs) > 0 {
			prompt += "\n" + dim(fmt.Sprintf("  📎 %s", imagesSummary(imgs)))
		}
		// 提交即挂心跳：TurnStarted 回来之前是用户最紧张的等待期，
		// spinner 帧必须动起来，不能等事件回环才起步。
		return m, tea.Batch(m.printScroll(prompt), m.armTick())
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
		if m.editor.Empty() {
			return m, tea.Quit
		}
		m.editor.Clear()
		m.quitArmed = true
		return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return quitArmExpiredMsg{}
		})
	case msg.Keystroke() == "ctrl+j" || msg.Keystroke() == "alt+enter":
		m.editor.InsertNewline()
	case msg.Code == tea.KeyBackspace:
		m.editor.Backspace()
	case msg.Code == tea.KeyDelete:
		m.editor.Delete()
	case msg.Code == tea.KeyLeft:
		m.editor.Left()
	case msg.Code == tea.KeyRight:
		m.editor.Right()
	case msg.Code == tea.KeyHome:
		m.editor.Home()
	case msg.Code == tea.KeyEnd:
		m.editor.End()
	case msg.Code == tea.KeyUp:
		if m.editor.lineCount() > 1 && m.editor.curRow > 0 {
			m.editor.Up()
		} else if m.editor.Empty() && len(m.history) > 0 {
			m.histDepth = min(m.histDepth+1, len(m.history))
			m.loadHistory()
		}
	case msg.Code == tea.KeyDown:
		if m.editor.lineCount() > 1 && m.editor.curRow < m.editor.lineCount()-1 {
			m.editor.Down()
		} else if m.histDepth > 0 {
			m.histDepth--
			m.loadHistory()
		}
	default:
		if s := msg.Text; s != "" {
			m.editor.Insert(s)
		}
	}
	m.quitArmed = false
	return m, nil
}

type quitArmExpiredMsg struct{}

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
	if err := os.WriteFile(path, []byte(m.editor.Text()), 0o644); err != nil {
		return m, m.printScroll(errStyle("临时文件创建失败：" + err.Error()))
	}
	cmd, err := m.deps.EditorCmd(path)
	if err != nil {
		os.Remove(path)
		return m, m.printScroll(errStyle("未配置编辑器：" + err.Error()))
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

func (m Model) editorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		return m, m.printScroll(errStyle("编辑器异常退出：" + msg.err.Error()))
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		return m, m.printScroll(errStyle("读取编辑结果失败：" + err.Error()))
	}
	content := strings.TrimSuffix(string(data), "\n")
	if content == "" {
		return m, nil // 空内容视为放弃编辑
	}
	m.editor.Set(content)
	return m, nil
}

func (m Model) loadHistory() {
	if m.histDepth == 0 {
		m.editor.Clear()
		return
	}
	m.editor.Set(m.history[len(m.history)-m.histDepth])
}

func (m *Model) rememberInput(text string) {
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
		// 渐进落盘：闭合行超过触发线即把头部行落进滚动缓冲区，帧内只留
		// 尾部窗口。整条回复攒到定稿一次性落盘会在长回答上击穿 insertAbove
		// 的视口不变式（scroll.go 顶部注释），流式期间滚动缓冲也不可搜索。
		if s := m.stream.String(); strings.Count(s, "\n") > streamFlushLines {
			i := strings.LastIndexByte(s, '\n')
			m.stream.Reset()
			m.stream.WriteString(s[i+1:])
			// 头部止于最后一个换行符（不含）：行终止符由落盘通道自补，
			// 带上它会在批尾多出一个空行。
			if c := m.printScroll(s[:i]); c != nil {
				m.streamPrinted = true
				cmd = c
			}
		}

	case agent.ReasonDeltaEvent:
		m.thinking = true
		m.appendReason(ev.Text)
		cmd = m.armTick()

	case agent.ReasonFinalEvent:
		m.thinking = false
		m.resetReason()

	case agent.StreamRestartedEvent:
		// 重连即重新生成：旧增量全部作废。已渐进落盘的部分收不回来，
		// 打一行暗色标记把静默丢弃变成诚实痕迹；帧内未落盘部分直接清掉。
		if m.streamPrinted {
			cmd = m.printScroll(dim("（流中断重连：上方未定稿内容已作废）"))
		}
		m.stream.Reset()
		m.streamPrinted = false
		m.thinking = false
		m.resetReason()

	case agent.ToolCallEvent:
		m.toolCalls++
		cmd = m.printScroll(dim(fmt.Sprintf("-> %s %s", ev.Name, ev.ArgsSummary)))

	case agent.ToolResultEvent:
		cmd = m.printScroll(dim(fmt.Sprintf("<- %s: %s", ev.Name, tool.ForTUI(ev.Result))))

	case agent.MessageFinalEvent:
		m.thinking = false
		m.resetReason()
		if ev.Usage != nil {
			m.usage = ev.Usage // 多 step 工具环中 ctx% 随每个 step 更新
		}
		// 定稿补余：闭合行已随流式渐进落盘，此处只补帧内剩余（含最后的
		// 未闭合行）。ev.Text 与 TextDelta 累积逐字节一致（agent 事件契约，
		// agent.go 定稿处直接写 text.String()），已打印前缀 + 剩余 == ev.Text。
		rest := m.stream.String()
		wasPrinted := m.streamPrinted
		m.stream.Reset()
		m.streamPrinted = false
		switch {
		case ev.Interrupted && rest != "":
			cmd = tea.Sequence(m.printScroll(rest), m.printScroll(dim("（以上内容被中断）")))
		case ev.Interrupted && wasPrinted:
			cmd = m.printScroll(dim("（以上内容被中断）"))
		case ev.Interrupted:
			cmd = m.printScroll(dim("（生成中断，内容未定稿）"))
		default:
			cmd = m.printScroll(rest)
		}

	case agent.TurnEndedEvent:
		m.busy = false
		m.thinking = false
		m.resetReason()
		// 回合耗时落款：只给正常完成的回合——中止/出错的时长无意义，
		// 错误详情已含原因。须在 turnStart 清零前取值；TurnEnded 每轮
		// 只来一次、必晚于最后一个回答定稿，标记因此天然落在回答末尾，
		// 时长覆盖工具环全程。
		var stamp tea.Cmd
		if ev.StopReason == agent.StopCompleted && m.turnStart.After(time.Time{}) {
			stamp = m.printScroll(dim(fmt.Sprintf("（耗时 %s · %s 完成）",
				human.Duration(time.Since(m.turnStart)), time.Now().Format("15:04"))))
		}
		m.turnStart = time.Time{}
		if ev.Usage != nil {
			m.usage = ev.Usage
		}
		var warn tea.Cmd
		if ev.StopReason != agent.StopAborted {
			warn = m.warnContextPressure()
		}
		if ev.StopReason == agent.StopAborted {
			cmd = m.printScroll(dim("（已中止）"))
		}
		// tea.Batch 无顺序保证（bubbletea 文档明示），多个 Println 会竞速；
		// 耗时落款须排在上下文告警之后，故打印段走 Sequence。listenAgent
		// 是长命命令，只能并发挂在 Batch 上。
		cmd = tea.Batch(listenAgent(m.deps.Events), tea.Sequence(cmd, warn, stamp))
		return m, cmd

	case agent.StatusEvent:
		cmd = m.printScroll(dim("| " + ev.Text))

	case agent.ModelSwitchedEvent:
		m.modelName = ev.Name
		m.windowTokens = ev.Window
		m.usage = nil // 新窗口下旧百分比无意义，等首个 usage 重建
		m.ctxWarned = false

	case agent.ErrorEvent:
		cmd = m.printScroll(errStyle(ev.Err.Error()))
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

// armTick 生成中挂一个心跳，驱动计时器与状态栏刷新。tickArmed 去重：
// 事件高频到达时（每个 reasoning 增量都会尝试挂表），保证任意时刻至多
// 一个未触发的 tick，否则定时器指数堆积。延时对齐 turnStart 的整秒边界：
// 自续式「触发后再等 1s」会把每次处理延迟累计进计时器，长回合越走越慢。
func (m Model) armTick() tea.Cmd {
	if !m.busy || m.tickArmed {
		return nil
	}
	m.tickArmed = true
	delay := time.Second
	if m.turnStart.After(time.Time{}) {
		delay = time.Second - time.Since(m.turnStart)%time.Second
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return turnTickMsg{} })
}

// turnTick 心跳：busy 期间自续，空闲时终止。思考行的刷新由 token 到达
// 驱动（尾部跟随）；思考期间勿在此加更高频的定时重绘——会与 tea.Println
// 交错产生空行 artifact。
func (m Model) turnTick() (tea.Model, tea.Cmd) {
	m.tickArmed = false
	if !m.busy {
		return m, nil
	}
	m.tickN++
	return m, m.armTick()
}

// spinnerFrames 等待期 spinner 的帧序列：心跳每秒推进一帧，给「还活着」
// 一个可见信号（turn 开始后由跳动的计时数字接管）。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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

// View 渲染唯一可变区：当前流式块 → 状态行 → 输入行。
// 光标用终端原生 bar 形状绘制（CJK 安全：不遮盖双宽字素）。
func (m Model) View() tea.View {
	width := m.width
	if width < 20 {
		width = 20
	}

	var lines []string
	lines = append(lines, m.streamBlockLines(width)...)
	switch m.popup {
	case popupModelPicker:
		lines = append(lines, m.modelPickerLines(width)...)
	case popupSkillPicker:
		lines = append(lines, m.skillPickerLines(width)...)
	}

	status := m.statusLine()
	lines = append(lines, status)

	prompt := "> "
	if m.busy {
		prompt = "> "
	}
	edLines, curLineIdx, cursorCol := m.editor.Render(prompt, width, m.editorMaxLines())
	lines = append(lines, edLines...)

	v := tea.NewView(strings.Join(lines, "\n"))
	v.Cursor = &tea.Cursor{
		Position: tea.Position{X: cursorCol, Y: len(lines) - len(edLines) + curLineIdx},
		Shape:    tea.CursorBar,
		Blink:    true,
	}
	return v
}

// editorMaxLines 多行输入框最大可视行数：终端高的 1/4，下限 3。
func (m Model) editorMaxLines() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	n := h / 4
	if n < 3 {
		n = 3
	}
	return n
}

// streamBlockLines 当前流式块的尾部若干行（原地整块重绘是显式策略）。
func (m Model) streamBlockLines(width int) []string {
	var lines []string
	if m.thinking {
		// 思考只展示最新一行（dsh 方案）：恒定一行的自绘面 + 跳动的计时
		// 数字就是「还活着」的口子。定稿即从视窗丢弃，全文在日志里。
		line := "- 思考中"
		if m.turnStart.After(time.Time{}) {
			line = fmt.Sprintf("- 思考中 %s", human.Duration(time.Since(m.turnStart)))
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
