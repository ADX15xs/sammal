package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// BashTool 执行命令。Windows 上无 bash 时自动降级 PowerShell（系统提示词
// 已同步注入「写 PowerShell 语法」提示，第 6.3 节）。bash 副作用不入快照
// （第 6.4 节如实声明）。
type BashTool struct {
	WorkDir string
	Shell   string // "bash" 或 "powershell"，会话创建时探测定格
}

const (
	bashDefaultTimeoutSec = 120
	bashMaxTimeoutSec     = 600
	bashOutputCap         = 200 * 1024
	bashWaitDelay         = time.Second
)

// bashExecutable 避开 Windows 上的 WSL 启动器：PATH 里第一个 bash.exe 常是
// System32/WindowsApps 的 WSL launcher，命令跑进 WSL 发行版后看不到 Windows
// CLI（keenable.exe 等），且 command not found 只进 stderr。优先用 Git/MSYS
// 的 bash.exe；找不到则退回原解析结果，保持旧行为。
func bashExecutable() string {
	if runtime.GOOS != "windows" {
		return "bash"
	}
	p, err := exec.LookPath("bash")
	if err != nil {
		return "bash"
	}
	low := strings.ToLower(filepath.ToSlash(p))
	if !strings.Contains(low, "/system32/") && !strings.Contains(low, "/windowsapps/") {
		return p
	}
	var candidates []string
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Git", "bin", "bash.exe"),
			filepath.Join(base, "Programs", "Git", "bin", "bash.exe"))
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.Contains(strings.ToLower(dir), "git") {
			candidates = append(candidates,
				filepath.Join(dir, "bash.exe"),
				filepath.Join(dir, "usr", "bin", "bash.exe"))
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return p
}

func (t *BashTool) Name() string   { return "bash" }
func (t *BashTool) ReadOnly() bool { return false }
func (t *BashTool) Description() string {
	return "Run a shell command in the working directory. Returns stdout, stderr and exit code. Non-zero exit is not an error by itself."
}

func (t *BashTool) Schema() json.RawMessage {
	// I2 NOTE: shellNote 是唯一允许的动态插值，定格于会话创建。
	// 其他工具 schema 不得引入此模式——新增动态字段会击穿 I2 前缀稳定性。
	shellNote := "commands run in bash"
	if t.Shell == "powershell" {
		shellNote = "IMPORTANT: commands run in PowerShell. Use PowerShell syntax; do not use bash syntax; chain with ; not &&"
	}
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "` + shellNote + `"},
		"timeout_seconds": {"type": "integer", "description": "Kill after N seconds (default 120, max 600)", "minimum": 1}
	},
	"required": ["command"]
}`)
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_seconds"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.Command == "" {
		return Result{Err: "command is required"}, nil
	}
	if a.TimeoutSec < 1 {
		a.TimeoutSec = bashDefaultTimeoutSec
	}
	a.TimeoutSec = min(a.TimeoutSec, bashMaxTimeoutSec)

	tctx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutSec)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch t.Shell {
	case "bash", "sh":
		shell := t.Shell
		if shell == "bash" {
			shell = bashExecutable()
		}
		cmd = exec.CommandContext(tctx, shell, "-c", a.Command)
	case "powershell":
		ps := "powershell"
		if _, err := exec.LookPath("pwsh"); err == nil {
			ps = "pwsh"
		}
		cmd = exec.CommandContext(tctx, ps, "-NoProfile", "-NonInteractive", "-Command", a.Command)
	default:
		return Result{Err: fmt.Sprintf("unknown shell %q", t.Shell)}, nil
	}
	cmd.Dir = t.WorkDir
	// shell 被超时杀死或先于后台任务退出时，持有输出管道的后代进程会让
	// cmd.Run 一直阻塞到管道关闭（实测 sleep 30 配 1s 超时阻塞 30s）：
	// WaitDelay 强制进程退出后 1s 关管道立即返回，孤儿进程留在后台。
	cmd.WaitDelay = bashWaitDelay

	var stdout, stderr limitBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	timedOut := tctx.Err() == context.DeadlineExceeded
	// ErrWaitDelay（管道被 WaitDelay 强关）仅在 shell 自身正常退出时返回，
	// ProcessState 携带真实退出码——按退出码判定，而非把 runErr 一律记 -1。
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		exitCode = -1
	}

	extra := map[string]any{
		"exitCode":   exitCode,
		"stderr":     stderr.String(),
		"durationMs": duration.Milliseconds(),
	}
	output := stdout.String()
	// 失败命令的 stderr 不能只留在 Extra：ForModel/ForTUI 都只读 Output，
	// command not found 会变成一片空白，模型只能反复重试或误装依赖。
	if se := stderr.String(); se != "" && (output == "" || exitCode != 0) {
		if output != "" {
			output += "\n"
		}
		output += "[stderr] " + se
	}
	res := Result{Output: output, Truncated: stdout.truncated || stderr.truncated, Extra: extra}
	if timedOut {
		res.Err = fmt.Sprintf("command timed out after %ds (killed); partial output follows", a.TimeoutSec)
	} else if runErr != nil && exitCode == -1 {
		res.Err = runErr.Error()
	}
	return res, nil
}

// limitBuffer 截断到上限的输出缓冲（超限记 truncated）。
type limitBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	room := bashOutputCap - b.buf.Len()
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf.Write(p[:room])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitBuffer) String() string { return b.buf.String() }
