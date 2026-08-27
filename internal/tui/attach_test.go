package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlashAttach(t *testing.T) {
	dir := t.TempDir()
	img1 := filepath.Join(dir, "a.png")
	img2 := filepath.Join(dir, "b.jpg")
	os.WriteFile(img1, []byte("x"), 0o644)
	os.WriteFile(img2, []byte("y"), 0o644)

	m := Model{}

	// 无图片时列出
	lines := m.slashAttach([]string{"/attach"})
	if len(lines) != 1 || lines[0] != "暂无待发送图片" {
		t.Errorf("empty = %v", lines)
	}

	// 添加合法图片
	lines = m.slashAttach([]string{"/attach", img1, img2})
	if len(lines) != 1 || lines[0] != "已添加 2 张图片" {
		t.Errorf("add = %v", lines)
	}
	if len(m.pendingImages) != 2 {
		t.Errorf("pending = %d, want 2", len(m.pendingImages))
	}

	// 列出
	lines = m.slashAttach([]string{"/attach"})
	if len(lines) != 3 || lines[0] != "待发送图片：" {
		t.Errorf("list = %v", lines)
	}

	// 清空
	lines = m.slashAttach([]string{"/attach", "-clear"})
	if len(lines) != 1 || lines[0] != "已清空所有待发送图片" {
		t.Errorf("clear = %v", lines)
	}
	if len(m.pendingImages) != 0 {
		t.Errorf("pending after clear = %d", len(m.pendingImages))
	}
}

func TestSlashAttachInvalidPaths(t *testing.T) {
	m := Model{}

	// 不存在的文件
	lines := m.slashAttach([]string{"/attach", "/nonexistent.png"})
	if len(lines) != 1 || lines[0] != "1 个路径无效（扩展名不支持或文件不存在）" {
		t.Errorf("missing = %v", lines)
	}

	// 错误扩展名
	os.WriteFile(filepath.Join(t.TempDir(), "x.txt"), []byte("x"), 0o644)
	lines = m.slashAttach([]string{"/attach", "x.txt"})
	if len(lines) != 1 || lines[0] != "1 个路径无效（扩展名不支持或文件不存在）" {
		t.Errorf("wrong ext = %v", lines)
	}

	// 混合：一个有效一个无效
	dir := t.TempDir()
	good := filepath.Join(dir, "ok.png")
	os.WriteFile(good, []byte("x"), 0o644)
	lines = m.slashAttach([]string{"/attach", good, "bad.txt"})
	if len(lines) != 2 || lines[0] != "已添加 1 张图片" || lines[1] != "1 个路径无效（扩展名不支持或文件不存在）" {
		t.Errorf("mixed = %v", lines)
	}
	if len(m.pendingImages) != 1 {
		t.Errorf("pending = %d, want 1", len(m.pendingImages))
	}
}
