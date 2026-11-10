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

// PromptFacts 是系统提示词的全部输入。会话首拍定值后不再变化（I2：系统
// 提示词在会话内字节级稳定）；resume 时从 SessionHeader 重建。「今天」不进
// 系统提示词——它是每 turn 动态的事实，由 Submit 随消息以 UserMessageData.Date
// 落盘、session 投影时注入消息头部前缀，系统提示词因此保持纯静态。
type PromptFacts struct {
	Cwd     string
	OS      string // runtime.GOOS，会话创建时定格
	Shell   string // "bash" 或 "powershell"，会话创建时探测
	Project string // AGENTS.md 内容，会话创建时定格（SPEC 6.10）
}

// TodayLocal 返回本地时区的当前日期（YYYY-MM-DD）。「今天」必须是用户
// 所在时区的一天——用 UTC 日期会让 UTC+8 的凌晨整整错一天。
func TodayLocal() string { return time.Now().Format("2006-01-02") }

func FactsFromEnv(cwd string) PromptFacts {
	return PromptFacts{
		Cwd:     cwd,
		OS:      runtime.GOOS,
		Shell:   DetectShell(),
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
	return fmt.Sprintf(systemPromptTemplate, f.Cwd, f.OS, f.Shell, project, hint)
}

// ResolveShell 把配置的 shell 归一为「提示词用的类型」+「显式可执行文件」。
// 空值自动探测；bash/sh/powershell（含 pwsh、带 .exe 写法）走内置路径；
// 其余值视为 bash 兼容 shell 的可执行文件（如 niu.exe），提示词按 bash 处理。
func ResolveShell(configured string) (kind, executable string) {
	v := strings.TrimSpace(configured)
	switch strings.ToLower(v) {
	case "":
		return DetectShell(), ""
	case "powershell", "pwsh", "powershell.exe", "pwsh.exe":
		return "powershell", ""
	case "bash", "bash.exe":
		return "bash", ""
	case "sh", "sh.exe":
		return "sh", ""
	default:
		return "bash", v
	}
}

// DetectShell 探测 bash 工具的默认 shell：Windows 默认 PowerShell
// （buildCmd 优先 pwsh），其余平台 PATH 中有 bash 用 bash、否则 sh。
func DetectShell() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "sh"
}
