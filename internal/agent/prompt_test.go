package agent

import (
	"strings"
	"testing"
)

// I2 起步：系统提示词序列化确定性——同输入必同字节，golden 锚定形态。
func TestBuildSystemPromptGolden(t *testing.T) {
	facts := PromptFacts{Cwd: "/home/u/proj", OS: "linux", Shell: "bash", Date: "2026-08-23"}
	got := BuildSystemPrompt(facts)
	want := `You are Sammal, a terminal-based coding agent working directly in the user's repository.

Environment:
- Working directory: /home/u/proj
- Platform: linux
- Shell for running commands: bash
- Today: 2026-08-23

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
	facts := PromptFacts{Cwd: `D:\proj`, OS: "windows", Shell: "powershell", Date: "2026-08-23"}
	got := BuildSystemPrompt(facts)
	if !strings.Contains(got, "Do not use bash syntax") {
		t.Error("powershell hint missing")
	}
	if !strings.Contains(got, `D:\proj`) {
		t.Error("cwd missing")
	}
}

// I2：AGENTS.md 内容是 PromptFacts 事实之一，首拍后重建必字节一致；
// 尾部换行不进入提示词（首拍读入与 header 重建共用同一裁剪）。
func TestBuildSystemPromptProjectSection(t *testing.T) {
	facts := PromptFacts{Cwd: "/home/u/proj", OS: "linux", Shell: "bash", Date: "2026-08-29", Project: "Use tabs.\nKeep it short."}
	got := BuildSystemPrompt(facts)
	want := `You are Sammal, a terminal-based coding agent working directly in the user's repository.

Environment:
- Working directory: /home/u/proj
- Platform: linux
- Shell for running commands: bash
- Today: 2026-08-29

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
	if again := BuildSystemPrompt(PromptFacts{Cwd: "/home/u/proj", OS: "linux", Shell: "bash", Date: "2026-08-29", Project: "Use tabs.\nKeep it short.\n\n"}); got != again {
		t.Error("尾部换行应被裁剪，首拍与重建必须字节一致")
	}
}
