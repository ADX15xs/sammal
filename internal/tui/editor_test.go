package tui

import (
	"strings"
	"testing"
)

// 多行编辑器核心：Enter/断行、跨行光标移动、行合并。
func TestEditorMultilineEditing(t *testing.T) {
	e := NewEditor()
	e.Insert("hello")

	e.InsertNewline()
	e.Insert("world")
	if got := e.Text(); got != "hello\nworld" {
		t.Fatalf("Text = %q", got)
	}
	if e.curRow != 1 || e.curCol != 5 {
		t.Fatalf("光标 = (%d,%d), want (1,5)", e.curRow, e.curCol)
	}

	// 行首 Backspace 合并两行。
	e.Home()
	e.Backspace()
	if got := e.Text(); got != "helloworld" {
		t.Errorf("合并后 Text = %q", got)
	}
	if e.curRow != 0 || e.curCol != 5 {
		t.Errorf("合并后光标 = (%d,%d), want (0,5)", e.curRow, e.curCol)
	}
}

// Delete 在行尾合并下一行。
func TestEditorDeleteMergesNextLine(t *testing.T) {
	e := NewEditor()
	e.Insert("hello")
	e.InsertNewline()
	e.Insert("world")
	e.Up()     // 到 line 0
	e.End()    // line 0 行尾
	e.Delete() // 合并 line 1 进 line 0
	if got := e.Text(); got != "helloworld" {
		t.Errorf("Delete 合并后 Text = %q", got)
	}
}

// 粘贴含换行的文本正确拆行。
func TestEditorInsertWithNewlines(t *testing.T) {
	e := NewEditor()
	e.Insert("a")
	e.Insert("b\nc\nd")
	if got := e.Text(); got != "ab\nc\nd" {
		t.Fatalf("Text = %q", got)
	}
	if e.curRow != 2 || e.curCol != 1 {
		t.Errorf("光标 = (%d,%d), want (2,1)", e.curRow, e.curCol)
	}
}

// 大段粘贴折叠为原子标记，提交时展开还原。
func TestEditorLargePasteCollapse(t *testing.T) {
	e := NewEditor()
	big := strings.Repeat("0123456789abcdef\n", 20) // 20 行，>10 行触发折叠
	e.InsertPaste(big)

	if e.lineCount() != 1 || len(e.lines[0]) != 1 {
		t.Fatalf("折叠后应单行单单元，lines=%d unit=%d", e.lineCount(), len(e.lines[0]))
	}
	u := e.lines[0][0]
	if u.pasteID == 0 {
		t.Fatal("折叠单元应有 pasteID")
	}
	if !strings.Contains(u.text, "[paste #") {
		t.Errorf("标记文本 = %q", u.text)
	}

	// 展开完整还原。
	if got := e.Text(); got != big {
		t.Errorf("展开后不等价于原文（len=%d want %d）", len(got), len(big))
	}

	// Backspace 整体删除标记单元，不留半个标记。
	e.Backspace()
	if !e.Empty() {
		t.Errorf("删除标记后应空，got %q", e.Text())
	}
}

// 小段粘贴不折叠。
func TestEditorSmallPasteNotCollapsed(t *testing.T) {
	e := NewEditor()
	e.InsertPaste("a\nb\nc")
	if got := e.Text(); got != "a\nb\nc" {
		t.Errorf("Text = %q", got)
	}
	if len(e.lines[0]) == 1 && e.lines[0][0].pasteID > 0 {
		t.Error("小段粘贴不应折叠")
	}
}

// CJK 多行：字素为单位移动，跨行不切半。
func TestEditorCJKMultiline(t *testing.T) {
	e := NewEditor()
	e.Insert("你好")
	e.InsertNewline()
	e.Insert("世界")

	e.Left()
	if e.curRow != 1 || e.curCol != 1 {
		t.Errorf("光标应在「世」后 = (%d,%d)", e.curRow, e.curCol)
	}
	e.Backspace()
	if got := e.Text(); got != "你好\n界" {
		t.Errorf("Backspace 后 = %q", got)
	}

	e.Up()
	if e.curRow != 0 {
		t.Errorf("Up 到上一行 = %d", e.curRow)
	}
	e.clampCol() // 跨到较短行时光标列钳制
	if e.curCol > len(e.lines[e.curRow]) {
		t.Errorf("Col 未钳制")
	}
}

// Render 垂直窗口：光标居中，上下滚动边框。
func TestEditorRenderVerticalWindow(t *testing.T) {
	e := NewEditor()
	for i := 0; i < 10; i++ {
		if i > 0 {
			e.InsertNewline()
		}
		e.Insert(strings.Repeat("l", 3))
	}
	// 光标在第 5 行（下标 4），maxVisibleLines=4 时窗口应包含它。
	e.curRow = 4
	e.End()

	lines, idx, _ := e.Render("> ", 40, 4)
	if idx < 0 || idx >= len(lines) {
		t.Fatalf("光标行 idx=%d 越界（lines=%d）", idx, len(lines))
	}
	// 10 行 > 4 行窗口，顶部必有"↑ N more"或底部"↓ N more"。
	foundBorder := false
	for _, l := range lines {
		if strings.Contains(l, "more") {
			foundBorder = true
		}
	}
	if !foundBorder {
		t.Errorf("应显示滚动边框，lines=%q", lines)
	}
}

// 单行回退：初始编辑器渲染一行移植了旧 InputLine 的语义。
func TestEditorSingleLineRenderBackCompat(t *testing.T) {
	e := NewEditor()
	e.Insert("hello")
	e.End()
	display, col := e.renderLine(0, "> ", 20, true)
	if !strings.Contains(display, "hello") {
		t.Errorf("display = %q", display)
	}
	if col != widthOf("> hello") {
		t.Errorf("cursor col = %d, want %d", col, widthOf("> hello"))
	}
}
