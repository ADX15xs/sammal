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
	interject     []interjectEntry // 待随流式批落盘的插话/工具行；不单独 insertAbove（T9 根因）
	scrollQ       *printQueue      // 滚动区落盘批次的全局 FIFO（scroll.go，T9 保序）
	frameHold     *frameHoldState  // 帧高冻结态（队列未排空时禁止帧行数变化，T9 根因对策）
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

// interjectEntry 待落盘的一行（插话回显、工具行或状态提示）。
type interjectEntry struct {
	content string
}

const (
	// interjectSentHint 生成中插话的灰字提示。措辞只承诺「已递交给模型、
	// 正在排队」——不能说「下一请求」，那会被读成「要你再发一条」。
	interjectSentHint = "（消息已发出，请等待模型响应）"
)

func (m *Model) addInterject(content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	m.interject = append(m.interject, interjectEntry{content: content})
}

func New(deps Deps) Model {
	return Model{deps: deps, modelName: deps.ModelName, stream: &strings.Builder{}, windowTokens: deps.ContextWindow, editor: NewEditor(), scrollQ: &printQueue{}, frameHold: &frameHoldState{}}
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
		// 尺寸变化由渲染器的 resize/pendingErase 路径处理，冻结帧让位。
		if m.frameHold != nil {
			m.frameHold.lines = nil
		}
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

	case drainNextMsg:
		return m.drainNext()

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

		// 生成中：Enter 是插话——回显随流落盘（视觉），原文投收件箱（语义，
		// agent.Submit 在 Running() 时转 Steering）。带图插话无承载者（DEBT），
		// 丢弃时必须出声，否则用户以为图已发。
		if m.busy {
			if len(m.pendingImages) > 0 {
				m.pendingImages = nil
				m.addInterject(dim("（生成中不支持带图，图片未发送）"))
			}
			if text == "" {
				flush, _ := m.flushPendingStream(false)
				return m, flush
			}
			// 不做去重：用户按了几次就发几条。内容完全相同的重复插话由
			// 模型按语义自行权衡，投递层只负责忠实投递——按内容+时间窗猜
			// 重复会在人类节奏上与判定线擦边，变成随机吞消息。
			m.deps.Send(text, nil)
			// 回显与提示同属一个 entry：两行永远相邻、同批落盘。提示若留到
			// 段末吸收时再发，会在长输出尾部堆成一片。
			m.addInterject(renderUserEcho(echo) + "\n" + dim(interjectSentHint))
			flush, _ := m.flushPendingStream(false)
			return m, flush
		}

		// 空闲中：正常提交新 turn
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
		// 插话回显随流式块落盘（不单独 insertAbove——舞蹈间的位移错位是
		// T9 根因）：先在尾部窗口显示，随下一次 flush 进滚动区。
		m.addInterject(renderUserEcho(echo))
		if len(imgs) > 0 {
			m.addInterject(dim(fmt.Sprintf("  📎 %s", imagesSummary(imgs))))
		}
		// 提交即挂心跳：TurnStarted 回来之前是用户最紧张的等待期，
		// spinner 帧必须动起来，不能等事件回环才起步。
		flush, _ := m.flushPendingStream(false)
		return m, tea.Batch(flush, m.armTick())
	}
	var repaint tea.Cmd
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
		} else if (m.editor.Empty() || m.histDepth > 0) && len(m.history) > 0 {
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
			// WT 组合态期间终端自行改写行单元，哈希渲染器的行状态模型与
			// 终端现实失步且不自愈（仅中英混排可感知，缓冲层恒正确）。
			// 文本键后强制整屏重绘对齐——探针 c 变体实机验证有效
			// （DEBT handleKey 行、SMOKE T10 变体矩阵）。
			repaint = tea.ClearScreen
		}
	}
	m.quitArmed = false
	return m, repaint
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

func (m *Model) loadHistory() {
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
		// 文本开始：思考行让位（ReasonFinal 已保证，此处兜底）。同样是「下沉」
		// 而非「撤走」，否则异常时序下照样留空洞。
		closeCmd := m.closeThinking()
		m.stream.WriteString(ev.Text)
		// 渐进落盘：闭合行超过触发线（或有待发插话/工具行）即把头部行落进
		// 滚动缓冲区，帧内只留尾部窗口。整条回复攒到定稿一次性落盘会在长
		// 回答上击穿 insertAbove 的视口不变式（scroll.go 顶部注释），流式
		// 期间滚动缓冲也不可搜索。批次入队即置 streamPrinted——排空在飞时
		// printScroll 返回 nil 但内容已在队列里，重连作废标记的判定依据是
		// 「是否入队」而非「是否开了新链」。
		if s := m.stream.String(); strings.Count(s, "\n") > streamFlushLines || len(m.interject) > 0 {
			var flushed bool
			cmd, flushed = m.flushPendingStream(false)
			if flushed {
				m.streamPrinted = true
			}
		}
		cmd = tea.Batch(closeCmd, cmd)

	case agent.ReasonDeltaEvent:
		m.thinking = true
		m.appendReason(ev.Text)
		cmd = m.armTick()

	case agent.ReasonFinalEvent:
		cmd = m.closeThinking()

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
		// 工具行随流式块落盘（不单独 insertAbove）：工具调用 = 消息边界，
		// 未闭合的模型文本语义完整，一并落盘。
		m.addInterject(dim(fmt.Sprintf("-> %s %s", ev.Name, ev.ArgsSummary)))
		cmd, _ = m.flushPendingStream(true)

	case agent.ToolResultEvent:
		m.addInterject(dim(fmt.Sprintf("<- %s: %s", ev.Name, tool.ForTUI(ev.Result))))
		cmd, _ = m.flushPendingStream(true)

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
			cmd = m.printScroll(rest, dim("（以上内容被中断）"))
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
		// 回合收尾落盘（变参一次入队、原子保序：告警先于耗时落款）：中止
		// 打「已中止」；正常完成打耗时落款——须在 turnStart 清零前取值，
		// TurnEnded 每轮只来一次、必晚于最后一个回答定稿，标记因此天然落
		// 在回答末尾，时长覆盖工具环全程；中止/出错不打耗时（时长无意义，
		// 错误详情已含原因）。usage 先行入账，ctx 告警按当轮真实占比判定。
		if ev.Usage != nil {
			m.usage = ev.Usage
		}
		var texts []string
		if ev.StopReason != agent.StopAborted {
			if w := m.contextPressureText(); w != "" {
				texts = append(texts, w)
			}
			if ev.StopReason == agent.StopCompleted && m.turnStart.After(time.Time{}) {
				texts = append(texts, dim(fmt.Sprintf("（耗时 %s · %s 完成）",
					human.Duration(time.Since(m.turnStart)), time.Now().Format("15:04"))))
			}
		} else {
			texts = append(texts, dim("（已中止）"))
		}
		m.turnStart = time.Time{}
		// listenAgent 是长命命令，只能并发挂在 Batch 上；打印段走排空链。
		cmd = tea.Batch(listenAgent(m.deps.Events), m.printScroll(texts...))
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

// thinkingLine 思考行的纯文本（单行、不超终端宽）。closed=true 表示思考已结束，
// 下沉进滚动区时用它——措辞不再说「思考中」。
func (m Model) thinkingLine(width int, closed bool) string {
	line := "- 思考中"
	if closed {
		line = "- 思考"
	}
	if m.turnStart.After(time.Time{}) {
		line = fmt.Sprintf("%s %s", line, human.Duration(time.Since(m.turnStart)))
	}
	cur := m.reasonLine()
	if cur == "" {
		return clipLine(line, width-1)
	}
	prefix := line + " | "
	body := strings.ReplaceAll(cur, "\t", " ")
	// 前缀（"思考中 12s"）固定做锚点，正文超宽走尾部跟随：最新 token
	// 永远可见、无省略号。不做定时动画（见 turnTick）。
	if bodyW := width - 1 - widthOf(prefix); bodyW > 0 {
		return prefix + tailWindow(body, bodyW)
	}
	return clipLine(prefix+body, width-1)
}

// closeThinking 结束思考：先把当前显示的那一行下沉进滚动区，再撤帧内思考行。
// 滚动区 +1 / 帧 −1 正好抵消，思考行原地保留；直接撤行会在滚动区留一个空洞
// （实机表现为用户回显与答案首行之间多一条空行）。
// 只落这一行、不落全文：落盘增量与思考长度无关，/resume 也不受影响。
func (m *Model) closeThinking() tea.Cmd {
	if !m.thinking {
		return nil
	}
	line := m.thinkingLine(m.width, true)
	if strings.TrimSpace(line) == "" {
		line = "- 思考"
	}
	cmd := m.printScroll(dim(line))
	m.thinking = false
	m.resetReason()
	return cmd
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
	curY := len(lines) - len(edLines) + curLineIdx

	// 帧高冻结（T9 根因对策）：队列未排空期间禁止帧行数变化——frameArea
	// 高度变化会令渲染器 Erase→ED 全量重绘，而 ED 从 insertAbove 遗留在
	// 历史区的真实光标处向下擦，吞掉在屏历史（缺行 + 残句碎片）。沿用上
	// 一帧行集与光标，内容照常累积，排空后的下一次 flush 再落地新帧——
	// 那时光标下方恰是本该让位的旧帧区，擦除无损。
	if m.frameHold != nil {
		busy := m.scrollQ.draining || len(m.scrollQ.pending) > 0
		sameHeight := m.frameHold.lines != nil && len(m.frameHold.lines) == len(lines)
		switch {
		case !busy || sameHeight:
			m.frameHold.lines, m.frameHold.curX, m.frameHold.curY = lines, cursorCol, curY
		default:
			lines, cursorCol, curY = m.frameHold.lines, m.frameHold.curX, m.frameHold.curY
		}
	}

	v := tea.NewView(strings.Join(lines, "\n"))
	v.Cursor = &tea.Cursor{
		Position: tea.Position{X: cursorCol, Y: curY},
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
		// 数字就是「还活着」的口子。结束时不撤走而是下沉进滚动区
		// （closeThinking），全文仍在日志里。
		lines = append(lines, dim(m.thinkingLine(width, false)))
	}
	text := m.stream.String()
	if text == "" && len(m.interject) == 0 {
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
	// 待发插话/工具行随块渲染（在尾部文本之后——它们晚于这些文本到达），
	// 随下一次 flush 进滚动区；不单独 insertAbove（T9 根因）。
	for _, e := range m.interject {
		lines = append(lines, e.content)
	}
	return lines
}

// flushPendingStream 把流式内容与待发插话/工具行按时间序并入落盘队列。
// closePartial=true 时未闭合行一并落盘（工具调用 = 消息边界，语义完整）；
// 否则未闭合行留在尾部窗口继续生长。插话/工具行不再单独 insertAbove——
// 舞蹈间的位移从不被渲染器模型记账，背靠背舞蹈互相错位（T9 根因）。
func (m *Model) flushPendingStream(closePartial bool) (tea.Cmd, bool) {
	s := m.stream.String()
	var texts []string
	flushed := false
	if s != "" {
		i := strings.LastIndexByte(s, '\n')
		switch {
		case i >= 0:
			texts = append(texts, s[:i])
			if closePartial && i+1 < len(s) {
				texts = append(texts, s[i+1:])
			}
			m.stream.Reset()
			if !closePartial {
				m.stream.WriteString(s[i+1:])
			}
			flushed = true
		case closePartial:
			texts = append(texts, s)
			m.stream.Reset()
			flushed = true
		}
	}
	if len(m.interject) > 0 {
		lines := make([]string, len(m.interject))
		for i, e := range m.interject {
			lines[i] = e.content
		}
		texts = append(texts, strings.Join(lines, "\n"))
		m.interject = nil
	}
	if len(texts) == 0 {
		return nil, flushed
	}
	return m.printScroll(texts...), flushed
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
