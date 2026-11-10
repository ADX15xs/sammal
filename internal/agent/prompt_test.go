package agent

import (
	"runtime"
	"strings"
	"testing"
)

// I2 起步：系统提示词序列化确定性——同输入必同字节，golden 锚定形态。
func TestBuildSystemPromptGolden(t *testing.T) {
	facts := PromptFacts{Cwd: "/home/u/proj", OS: "linux", Shell: "bash"}
	got := BuildSystemPrompt(facts)
	want := `You are Sammal, a terminal-based coding agent working directly in the user's repository.

Environment:
- Working directory: /home/u/proj
- Platform: linux
- Shell for running commands: bash

Working style:
- Be concise. Answer in the user's language.
- For file tasks, prefer precise edits over rewrites.
- Verify your changes by running commands when reasonable.`
	if got != want {
		t.Errorf("system prompt golden mismatch:\n got: %q\nwant: %q", got, want)
	}
	again := BuildSystemPrompt(facts)
	if got != again {
		t.Error("system prompt not deterministic")
	}
}

func TestBuildSystemPromptPowerShellHint(t *testing.T) {
	facts := PromptFacts{Cwd: `D:\proj`, OS: "windows", Shell: "powershell"}
	got := BuildSystemPrompt(facts)
	if !strings.Contains(got, "Do not use bash syntax") {
		t.Error("powershell hint missing")
	}
	if !strings.Contains(got, `D:\proj`) {
		t.Error("cwd missing")
	}
}

func TestResolveShell(t *testing.T) {
	cases := []struct {
		configured string
		kind       string
		executable string
	}{
		{"powershell", "powershell", ""},
		{"pwsh", "powershell", ""},
		{"powershell.exe", "powershell", ""},
		{"PWSH.EXE", "powershell", ""},
		{"bash", "bash", ""},
		{"BASH.EXE", "bash", ""},
		{"sh", "sh", ""},
		{"sh.exe", "sh", ""},
		{" NIU.EXE ", "bash", "NIU.EXE"},
		{`C:\Tools\niu.exe`, "bash", `C:\Tools\niu.exe`},
	}
	for _, c := range cases {
		kind, executable := ResolveShell(c.configured)
		if kind != c.kind || executable != c.executable {
			t.Errorf("ResolveShell(%q) = %q, %q；期望 %q, %q", c.configured, kind, executable, c.kind, c.executable)
		}
	}
	// 空值 = 平台探测，可执行文件留空走自动解析。
	if kind, executable := ResolveShell(""); kind != DetectShell() || executable != "" {
		t.Errorf(`ResolveShell("") = %q, %q；期望 %q, ""`, kind, executable, DetectShell())
	}
}

// Windows 默认 PowerShell，不再因 PATH 中有 bash 而选用 bash。
func TestDetectShellWindowsDefault(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 有 PowerShell 默认行为")
	}
	if got := DetectShell(); got != "powershell" {
		t.Errorf("Windows 默认 shell = %q，期望 powershell", got)
	}
}

// I2：AGENTS.md 内容是 PromptFacts 事实之一，首拍后重建必字节一致；
// 尾部换行不进入提示词（首拍读入与 header 重建共用同一裁剪）。
func TestBuildSystemPromptProjectSection(t *testing.T) {
	facts := PromptFacts{Cwd: "/home/u/proj", OS: "linux", Shell: "bash", Project: "Use tabs.\nKeep it short."}
	got := BuildSystemPrompt(facts)
	want := `You are Sammal, a terminal-based coding agent working directly in the user's repository.

Environment:
- Working directory: /home/u/proj
- Platform: linux
- Shell for running commands: bash

Project instructions (from AGENTS.md):
Use tabs.
Keep it short.

Working style:
- Be concise. Answer in the user's language.
- For file tasks, prefer precise edits over rewrites.
- Verify your changes by running commands when reasonable.`
	if got != want {
		t.Errorf("project section mismatch:\n got: %q\nwant: %q", got, want)
	}
	if again := BuildSystemPrompt(PromptFacts{Cwd: "/home/u/proj", OS: "linux", Shell: "bash", Project: "Use tabs.\nKeep it short.\n\n"}); got != again {
		t.Error("尾部换行应被裁剪，首拍与重建必须字节一致")
	}
}
