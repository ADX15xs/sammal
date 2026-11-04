// Package tui 流渲染辅助：思考块渲染、尾行窗口、图片摘要（搬迁自 model.go，行为不变）。
package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sammal/internal/human"

	tea "charm.land/bubbletea/v2"
)

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
