package tui

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("qwen3-local", "q3l") {
		t.Error("子序列应命中")
	}
	if fuzzyMatch("qwen3-local", "localx") {
		t.Error("超出子序列不应命中")
	}
	if !fuzzyMatch("DeepSeek", "deep") {
		t.Error("大小写不敏感")
	}
	if !fuzzyMatch("anything", "") {
		t.Error("空过滤命中全部")
	}
}

// Ctrl+P 选择器 headless：打开 → 过滤 → Enter 触发 /model 切换。
func TestModelPickerHeadless(t *testing.T) {
	var mu sync.Mutex
	var slashCalls []string
	deps := Deps{
		ModelName: "alpha",
		Send:      func(string, []string) {},
		Abort:     func() {},
		Slash: func(text string) []string {
			mu.Lock()
			slashCalls = append(slashCalls, text)
			mu.Unlock()
			return []string{"switched"}
		},
		Models: func() []string { return []string{"alpha", "beta", "deepseek-cloud"} },
	}

	p := tea.NewProgram(New(deps),
		tea.WithInput(strings.NewReader("\x10bet\r")), // ctrl+p, b,e,t, Enter
		tea.WithOutput(io.Discard),
	)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(slashCalls)
		mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			p.Quit()
			t.Fatalf("选择器未触发切换，slash = %v", slashCalls)
		case <-time.After(20 * time.Millisecond):
		}
	}
	mu.Lock()
	got := slashCalls[0]
	mu.Unlock()
	if got != "/model beta" {
		t.Errorf("slash 调用 = %q, want /model beta", got)
	}
	p.Quit()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("program 未退出")
	}
}

func TestEditorDoneReadsFile(t *testing.T) {
	m := New(Deps{ModelName: "x"})
	path := writeTemp(t, "line one\nline two\n")
	out, _ := m.editorDone(editorDoneMsg{path: path})
	if got := out.(Model).InputText(); got != "line one\nline two" {
		t.Errorf("编辑结果 = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("临时文件应被清理")
	}

	// 空内容视为放弃编辑。
	path = writeTemp(t, "\n")
	m2 := New(Deps{ModelName: "x"})
	m2.input.Insert("原有输入")
	out, _ = m2.editorDone(editorDoneMsg{path: path})
	if got := out.(Model).InputText(); got != "原有输入" {
		t.Errorf("放弃编辑后输入 = %q", got)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "sammal-edit-*")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}
