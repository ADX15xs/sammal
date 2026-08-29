package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PromptFacts 是系统提示词的全部动态输入。会话首拍定值后不再变化
// （I2：系统提示词在会话内字节级稳定）；resume 时从 SessionHeader 重建。
type PromptFacts struct {
	Cwd     string
	OS      string // runtime.GOOS，会话创建时定格
	Shell   string // "bash" 或 "powershell"，会话创建时探测
	Date    string // YYYY-MM-DD，会话创建日
	Project string // AGENTS.md 内容，会话创建时定格（SPEC 6.10）
}

func FactsFromEnv(cwd string) PromptFacts {
	return PromptFacts{
		Cwd:     cwd,
		OS:      runtime.GOOS,
		Shell:   DetectShell(),
		Date:    time.Now().Format("2006-01-02"),
		Project: ReadAgentsMD(cwd),
	}
}

// ReadAgentsMD 读取 <cwd>/AGENTS.md（项目常驻指令，prompt 简化器的常驻
// 半边）。不存在或不可读返回空串——项目指令是可选事实。
func ReadAgentsMD(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

const systemPromptTemplate = `You are Sammal, a terminal-based coding agent working directly in the user's repository.

Environment:
- Working directory: %s
- Platform: %s
- Shell for running commands: %s
- Today: %s
%s
Working style:
- Be concise. Answer in the user's language.
- For file tasks, prefer precise edits over rewrites.
- Verify your changes by running commands when reasonable.%s`

const powershellHint = `
- IMPORTANT: Commands run in PowerShell. Write PowerShell syntax. Do not use bash syntax, and do not chain commands with && (use ; instead).`

// BuildSystemPrompt 生成系统提示词。输入相同则字节相同（I2 golden 的对象）。
func BuildSystemPrompt(f PromptFacts) string {
	hint := ""
	if f.Shell == "powershell" {
		hint = powershellHint
	}
	project := ""
	if f.Project != "" {
		project = "\nProject instructions (from AGENTS.md):\n" + strings.TrimRight(f.Project, "\n") + "\n"
	}
	return fmt.Sprintf(systemPromptTemplate, f.Cwd, f.OS, f.Shell, f.Date, project, hint)
}

// DetectShell 探测 bash 工具将使用的 shell：PATH 中有 bash 用 bash；
// Windows 上无 bash 则降级 PowerShell（第 6.3 节，Reasonix 验证过的降级）。
func DetectShell() string {
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "sh"
}
