package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// 生成中的斜杠命令仍是命令（2026-11-12 缺陷回归）：/jobs 只读注册表，
// 不得被当插话投给模型，也不得打「消息已发出」。
func TestSlashJobsDuringGeneration(t *testing.T) {
	var sent []string
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(text string, _ []string) { sent = append(sent, text) },
		Slash:     func(string) []string { t.Fatal("TUI 专属命令不应下发给 core"); return nil },
		Jobs:      tool.NewJobRegistry(),
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.editor.Insert("/jobs")

	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	prints := printedLines(cmd)
	joined := strings.Join(prints, "\n")
	if len(sent) != 0 {
		t.Fatalf("生成中的 /jobs 不应投给模型：%q", sent)
	}
	if !strings.Contains(joined, "没有后台 job") {
		t.Fatalf("/jobs 应照常列出注册表：%q", prints)
	}
	if strings.Contains(joined, interjectSentHint) {
		t.Fatalf("命令不是插话，不应出现「消息已发出」：%q", prints)
	}
}

// 生成中敲改状态的 core 命令：由 core 的 idleGuard 回绝，同样不进收件箱。
func TestCoreSlashDuringGenerationNotInterjected(t *testing.T) {
	var sent []string
	var slashCalls []string
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(text string, _ []string) { sent = append(sent, text) },
		Slash: func(text string) []string {
			slashCalls = append(slashCalls, text)
			return []string{"生成中不能 /new，请先 Esc 中止"}
		},
	})
	m.width, m.height = 80, 30
	m.busy = true
	m.editor.Insert("/new")

	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	prints := printedLines(cmd)
	if len(slashCalls) != 1 || slashCalls[0] != "/new" {
		t.Fatalf("应把 /new 交给 core 判定：%v", slashCalls)
	}
	if len(sent) != 0 {
		t.Fatalf("生成中的 /new 不应投给模型：%q", sent)
	}
	if !strings.Contains(strings.Join(prints, "\n"), "生成中不能 /new") {
		t.Fatalf("应如实回绝：%q", prints)
	}
}

// printCmd 取 Batch 中的打印子命令：Update 对 jobSettledMsg 返回
// 打印 + 重听两路，listenJobs 在测试空通道上会阻塞，不可执行。
func printCmd(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("应返回 打印+重听 两路 Batch：%T", cmd)
	}
	return batch[0]
}

// M5 二期：结算即时通知滚动区，每 job 恰一行（I7 形状）。
func TestJobSettledNotification(t *testing.T) {
	m := New(Deps{ModelName: "m", Events: closedEvents(), Send: func(string, []string) {}, Jobs: tool.NewJobRegistry()})
	m.width, m.height = 80, 30

	next, cmd := m.Update(jobSettledMsg{info: tool.JobInfo{ID: "j1", Command: "sleep 30", Status: tool.JobKilled, DurationMs: 2000}})
	m = next.(Model)
	prints := printedLines(printCmd(t, cmd))
	if len(prints) != 1 || !strings.Contains(prints[0], "[job j1] 已击杀：sleep 30（耗时 2s）") {
		t.Fatalf("结算应落盘一行通知: %q", prints)
	}
	m, _ = drainAll(t, m) // 排空链收尾：draining 复位，否则下一次 printScroll 返回 nil

	// 另一 job 的结算再落一行；同格式，不残留帧内。
	next, cmd = m.Update(jobSettledMsg{info: tool.JobInfo{ID: "j2", Command: "npm run dev", Status: tool.JobDone, DurationMs: 62000}})
	m = next.(Model)
	prints = printedLines(printCmd(t, cmd))
	if len(prints) != 1 || !strings.Contains(prints[0], "[job j2] 完成：npm run dev（耗时 1m02s）") {
		t.Fatalf("第二次结算应各落一行: %q", prints)
	}
}
