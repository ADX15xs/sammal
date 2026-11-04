package tui

import (
	"strings"
	"testing"

	"sammal/internal/tool"
)

// M5 人侧通道：/jobs 直读注册表、/jobs kill 不经模型。
func TestSlashJobs(t *testing.T) {
	reg := tool.NewJobRegistry()
	m := New(Deps{Jobs: reg})

	lines, handled := m.handleSlash("/jobs")
	if !handled || !strings.Contains(strings.Join(lines, "\n"), "没有后台 job") {
		t.Fatalf("空列表应如实报告：%v %v", handled, lines)
	}

	if lines, handled := m.handleSlash("/jobs kill j9"); !handled || !strings.Contains(strings.Join(lines, "\n"), "未知 job") {
		t.Fatalf("未知 id 应报错：%v %v", handled, lines)
	}

	// 非法子命令回退列表视图。
	if lines, _ := m.handleSlash("/jobs wrong"); !strings.Contains(strings.Join(lines, "\n"), "没有后台 job") {
		t.Fatalf("非法子命令应回退列表：%v", lines)
	}
}
