package tui

import (
	"strings"
	"testing"
)

func TestInputLineBasicEditing(t *testing.T) {
	var l InputLine
	l.Insert("hello")
	if got := l.String(); got != "hello" {
		t.Fatalf("String = %q", got)
	}
	if l.cursor != 5 {
		t.Fatalf("cursor = %d", l.cursor)
	}
	l.Left()
	l.Left()
	l.Insert("X")
	if got := l.String(); got != "helXlo" {
		t.Errorf("after insert = %q", got)
	}
	l.Backspace()
	if got := l.String(); got != "hello" {
		t.Errorf("after backspace = %q", got)
	}
	l.Home()
	l.Delete()
	if got := l.String(); got != "ello" {
		t.Errorf("after delete = %q", got)
	}
	l.End()
	if l.cursor != 4 {
		t.Errorf("End cursor = %d", l.cursor)
	}
}

// CJK 核心：字素为单位移动与删除，光标列宽按双宽计。
func TestInputLineCJK(t *testing.T) {
	var l InputLine
	l.Insert("你好world")

	l.Home()
	l.Right() // 光标在「你」后
	if l.cursor != 1 {
		t.Fatalf("cursor after 你 = %d", l.cursor)
	}
	_, col := l.Render("> ", 80)
	if col != 2+2 {
		t.Fatalf("cursor col = %d, want 4（前缀 2 + 你 双宽）", col)
	}

	l.Backspace() // 删除「你」
	if got := l.String(); got != "好world" {
		t.Errorf("after CJK backspace = %q", got)
	}

	// 组合 emoji 按一个字素处理。
	var e InputLine
	e.Insert("a👍b")
	e.Home()
	e.Right()
	e.Right()
	if e.cursor != 2 {
		t.Errorf("emoji cursor = %d", e.cursor)
	}
	e.Backspace()
	if got := e.String(); got != "ab" {
		t.Errorf("after emoji backspace = %q", got)
	}
}

func TestInputLineRenderWindowing(t *testing.T) {
	var l InputLine
	l.Insert("abcdefghij")
	l.End()
	display, col := l.Render("> ", 8) // 预算 2：仅能显示尾部两个字符
	if !strings.Contains(display, "ij") || strings.Contains(display, "hij") {
		t.Errorf("display = %q, 应只含尾部 ij", display)
	}
	if col < 2 || col > 7 {
		t.Errorf("cursor col = %d 超出窗口", col)
	}
}

func TestInputLineMultilinePaste(t *testing.T) {
	var l InputLine
	l.Insert("first\nsecond\nthird")
	if got := l.String(); got != "first\nsecond\nthird" {
		t.Fatalf("String = %q", got)
	}
	display, _ := l.Render("> ", 80)
	if !strings.Contains(display, "first") || strings.Contains(display, "second") {
		t.Errorf("display = %q, 只应显示首行", display)
	}
	if !strings.Contains(display, "+3L") {
		t.Errorf("display = %q, 应标注 3 行", display)
	}
}
