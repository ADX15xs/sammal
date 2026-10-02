package tui

import (
	"fmt"
	"strings"
)

// editorUnit 是编辑器的原子单元：普通字素簇或粘贴标记。粘贴标记的 text
// 是展示标签（如 [paste #1 +123 lines]），pasteID > 0 指向 pastes 注册表。
type editorUnit struct {
	text    string
	pasteID int
}

// pasteCollapseLines / pasteCollapseChars 超过即折叠为标记的阈值（照搬 Pi
// editor.ts:1288-1303 的经验值）。
const (
	pasteCollapseLines = 10
	pasteCollapseChars = 1000
)

// Editor 是 CJK 安全的多行输入编辑器：字素簇为最小单位，大段粘贴折叠为
// 原子标记，光标以 (row, col) 定位。展示层做多行垂直窗口 + 光标行水平
// 跟随。替代 InputLine 的单行 +NL 压平方案（DEBT row 1）。
type Editor struct {
	lines     [][]editorUnit
	curRow    int
	curCol    int // lines[curRow] 内的 unit 下标，0..len(line)
	pastes    map[int]string
	nextPaste int
}

func NewEditor() Editor {
	return Editor{
		lines:  [][]editorUnit{{}},
		pastes: make(map[int]string),
	}
}

func (e *Editor) lineCount() int { return len(e.lines) }

func (e *Editor) Empty() bool {
	if len(e.lines) != 1 {
		return false
	}
	return len(e.lines[0]) == 0
}

// Text 返回展开后的完整文本（粘贴标记替换为全文），用于提交/回显/历史。
func (e *Editor) Text() string {
	var b strings.Builder
	for r, line := range e.lines {
		if r > 0 {
			b.WriteByte('\n')
		}
		for _, u := range line {
			if u.pasteID > 0 {
				b.WriteString(e.pastes[u.pasteID])
			} else {
				b.WriteString(u.text)
			}
		}
	}
	return b.String()
}

// String 是 Text 的别名，保持与旧 InputLine 调用点签名一致。
func (e *Editor) String() string { return e.Text() }

func (e *Editor) Clear() {
	e.lines = [][]editorUnit{{}}
	e.curRow = 0
	e.curCol = 0
	e.pastes = make(map[int]string)
	e.nextPaste = 0
}

// Set 用 text 整体替换编辑器内容（标记展开为原文，不再折叠）。
func (e *Editor) Set(text string) {
	e.Clear()
	if text != "" {
		e.Insert(text)
	}
}

// Insert 在光标处插入文本，含换行则分拆为多行。
func (e *Editor) Insert(s string) {
	if s == "" {
		return
	}
	parts := strings.Split(s, "\n")
	for _, c := range graphemes(parts[0]) {
		e.insertUnit(editorUnit{text: c})
	}
	for _, part := range parts[1:] {
		e.splitLine()
		for _, c := range graphemes(part) {
			e.insertUnit(editorUnit{text: c})
		}
	}
}

// InsertNewline 在光标处断行。
func (e *Editor) InsertNewline() { e.splitLine() }

// InsertPaste 大段粘贴折叠为标记原子单元；小段走普通 Insert。
func (e *Editor) InsertPaste(text string) {
	if text == "" {
		return
	}
	nl := strings.Count(text, "\n") + 1
	if nl > pasteCollapseLines || len(text) > pasteCollapseChars {
		e.nextPaste++
		id := e.nextPaste
		e.pastes[id] = text
		label := fmt.Sprintf("[paste #%d +%d lines]", id, nl)
		e.insertUnit(editorUnit{text: label, pasteID: id})
		return
	}
	e.Insert(text)
}

func (e *Editor) insertUnit(u editorUnit) {
	line := e.lines[e.curRow]
	line = append(line, editorUnit{})
	copy(line[e.curCol+1:], line[e.curCol:])
	line[e.curCol] = u
	e.lines[e.curRow] = line
	e.curCol++
}

// splitLine 在光标处断行：curCol 及之后的内容移到新行，光标到新行首。
func (e *Editor) splitLine() {
	cur := e.lines[e.curRow]
	rest := make([]editorUnit, len(cur[e.curCol:]))
	copy(rest, cur[e.curCol:])
	e.lines[e.curRow] = cur[:e.curCol]
	e.lines = append(e.lines, nil)
	copy(e.lines[e.curRow+2:], e.lines[e.curRow+1:])
	e.lines[e.curRow+1] = rest
	e.curRow++
	e.curCol = 0
}

func (e *Editor) Backspace() bool {
	if e.curCol > 0 {
		line := e.lines[e.curRow]
		line = append(line[:e.curCol-1], line[e.curCol:]...)
		e.lines[e.curRow] = line
		e.curCol--
		return true
	}
	if e.curRow == 0 {
		return false
	}
	prev := e.lines[e.curRow-1]
	cur := e.lines[e.curRow]
	e.curCol = len(prev)
	e.lines[e.curRow-1] = append(prev, cur...)
	e.lines = append(e.lines[:e.curRow], e.lines[e.curRow+1:]...)
	e.curRow--
	return true
}

func (e *Editor) Delete() bool {
	if e.curCol < len(e.lines[e.curRow]) {
		line := e.lines[e.curRow]
		line = append(line[:e.curCol], line[e.curCol+1:]...)
		e.lines[e.curRow] = line
		return true
	}
	if e.curRow >= len(e.lines)-1 {
		return false
	}
	cur := e.lines[e.curRow]
	next := e.lines[e.curRow+1]
	e.lines[e.curRow] = append(cur, next...)
	e.lines = append(e.lines[:e.curRow+1], e.lines[e.curRow+2:]...)
	return true
}

func (e *Editor) Left() bool {
	if e.curCol > 0 {
		e.curCol--
		return true
	}
	if e.curRow > 0 {
		e.curRow--
		e.curCol = len(e.lines[e.curRow])
		return true
	}
	return false
}

func (e *Editor) Right() bool {
	if e.curCol < len(e.lines[e.curRow]) {
		e.curCol++
		return true
	}
	if e.curRow < len(e.lines)-1 {
		e.curRow++
		e.curCol = 0
		return true
	}
	return false
}

func (e *Editor) Up() bool {
	if e.curRow > 0 {
		e.curRow--
		e.clampCol()
		return true
	}
	return false
}

func (e *Editor) Down() bool {
	if e.curRow < len(e.lines)-1 {
		e.curRow++
		e.clampCol()
		return true
	}
	return false
}

func (e *Editor) clampCol() {
	max := len(e.lines[e.curRow])
	if e.curCol > max {
		e.curCol = max
	}
}

func (e *Editor) Home() { e.curCol = 0 }

func (e *Editor) End() { e.curCol = len(e.lines[e.curRow]) }

// Render 渲染多行展示：垂直窗口以光标为中心（超 maxVisibleLines 时上下
// 滚动边框），光标行做水平窗口保持光标可见。返回展示行、光标所在行下标、
// 光标列。首行加 prefix，续行用等宽空格对齐。
func (e *Editor) Render(prefix string, maxWidth, maxVisibleLines int) (lines []string, curLineIdx int, cursorCol int) {
	if maxVisibleLines < 1 {
		maxVisibleLines = 1
	}
	total := len(e.lines)

	startRow := 0
	shown := total
	if total > maxVisibleLines {
		startRow = e.curRow - maxVisibleLines/2
		if startRow < 0 {
			startRow = 0
		}
		if startRow+maxVisibleLines > total {
			startRow = total - maxVisibleLines
		}
		shown = maxVisibleLines
	}

	if startRow > 0 {
		lines = append(lines, dim(fmt.Sprintf("  ↑ %d more", startRow)))
	}

	for i := 0; i < shown; i++ {
		row := startRow + i
		prefixStr := "  "
		if row == 0 {
			prefixStr = prefix
		}
		display, col := e.renderLine(row, prefixStr, maxWidth, row == e.curRow)
		lines = append(lines, display)
		if row == e.curRow {
			curLineIdx = len(lines) - 1
			cursorCol = col
		}
	}

	if startRow+shown < total {
		lines = append(lines, dim(fmt.Sprintf("  ↓ %d more", total-startRow-shown)))
	}
	return lines, curLineIdx, cursorCol
}

// renderLine 渲染单行：光标行做水平窗口（光标可见），其余行仅截断。
func (e *Editor) renderLine(row int, prefixStr string, maxWidth int, isCursor bool) (string, int) {
	line := e.lines[row]

	type uinfo struct {
		text  string
		width int
	}
	infos := make([]uinfo, len(line))
	for i, u := range line {
		infos[i] = uinfo{u.text, widthOf(u.text)}
	}

	if !isCursor {
		var b strings.Builder
		b.WriteString(prefixStr)
		used := widthOf(prefixStr)
		for _, u := range infos {
			if used+u.width > maxWidth-1 {
				if b.Len() > len(prefixStr) {
					b.WriteString("...")
				}
				break
			}
			b.WriteString(u.text)
			used += u.width
		}
		return b.String(), 0
	}

	budget := maxWidth - widthOf(prefixStr) - 4
	if budget < 1 {
		budget = 1
	}
	end := min(e.curCol, len(infos))
	start := end
	used := 0
	for start > 0 && used+infos[start-1].width <= budget {
		used += infos[start-1].width
		start--
	}
	for end < len(infos) && used+infos[end].width <= budget {
		used += infos[end].width
		end++
	}
	ellipsis := ""
	if start > 0 {
		ellipsis = "..."
	}
	var body, beforeCursor strings.Builder
	for i := start; i < end; i++ {
		body.WriteString(infos[i].text)
	}
	for i := start; i < min(e.curCol, end); i++ {
		beforeCursor.WriteString(infos[i].text)
	}
	display := prefixStr + ellipsis + body.String()
	col := widthOf(prefixStr) + widthOf(ellipsis) + widthOf(beforeCursor.String())
	return display, col
}
