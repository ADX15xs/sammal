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
//
// Jobs 非 nil 时走 M5 后台路径：run_in_background 立即返回 job id；前台
// 命令超时不杀、晋升为后台 job（promoteOnTimeout）。Jobs 为 nil（测试裸
// 构造）保持旧语义：超时即杀。
type BashTool struct {
	WorkDir string
	Shell   string // "bash" 或 "powershell"，会话创建时探测定格
	Jobs    *JobRegistry
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
	return "Run a shell command in the working directory. Returns stdout, stderr and exit code. Non-zero exit is not an error by itself. Supports background jobs: run_in_background returns a job id at once, and a foreground command that outlives its timeout is moved to a background job instead of killed."
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
		"timeout_seconds": {"type": "integer", "description": "Foreground time limit in seconds (default 120, max 600). On expiry a long command is moved to a background job (not killed); its result arrives with the user's next message", "minimum": 1},
		"run_in_background": {"type": "boolean", "description": "Start as a background job: returns a job id at once and keeps running across turns. Collect output with job_output; list or stop with /jobs (or job_kill). Choose this for servers/watchers and other commands whose deliverable is that they keep running, or when the user should keep the terminal now and the result can arrive with their next message"}
	},
	"required": ["command"]
}`)
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Command         string `json:"command"`
		TimeoutSec      int    `json:"timeout_seconds"`
		RunInBackground bool   `json:"run_in_background"`
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

	if t.Jobs != nil {
		return t.executeWithJobs(ctx, a.Command, a.TimeoutSec, a.RunInBackground), nil
	}
	return t.executeLegacy(ctx, a.Command, a.TimeoutSec), nil
}

// buildCmd 构造命令（双 shell）。后台路径不使用 CommandContext——晋升后
// 的 job 必须脱离 turn ctx 存活，击杀一律走 Process.Kill。
func (t *BashTool) buildCmd(command string) *exec.Cmd {
	var cmd *exec.Cmd
	switch t.Shell {
	case "bash", "sh":
		shell := t.Shell
		if shell == "bash" {
			shell = bashExecutable()
		}
		cmd = exec.Command(shell, "-c", command)
	case "powershell":
		ps := "powershell"
		if _, err := exec.LookPath("pwsh"); err == nil {
			ps = "pwsh"
		}
		cmd = exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", command)
	}
	if cmd != nil {
		cmd.Dir = t.WorkDir
		// shell 被杀死或先于后台任务退出时，持有输出管道的后代进程会让
		// Wait 阻塞到管道关闭：WaitDelay 强关管道立即返回（2026-10-23）。
		cmd.WaitDelay = bashWaitDelay
	}
	return cmd
}

// mergeStderrIntoOutput 失败命令的 stderr 不能只留在 Extra：ForModel/ForTUI
// 都只读 Output，command not found 会变成一片空白，模型只能反复重试或误装
// 依赖。后台路径与旧路径共用同一形状（I5）。
func mergeStderrIntoOutput(output, stderr string, exitCode int) string {
	if stderr != "" && (output == "" || exitCode != 0) {
		if output != "" {
			output += "\n"
		}
		output += "[stderr] " + stderr
	}
	return output
}

// executeWithJobs 后台路径：后台立返；前台超时晋升。进程等待的唯一所有者
// 是注册表的 wait goroutine，晋升只是补注册。
func (t *BashTool) executeWithJobs(ctx context.Context, command string, timeoutSec int, runInBackground bool) Result {
	stdout, stderr := newJobBuffer(), newJobBuffer()
	cmd := t.buildCmd(command)
	if cmd == nil {
		return Result{Err: fmt.Sprintf("unknown shell %q", t.Shell)}
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if runInBackground {
		if existing := t.Jobs.findRunning(command); existing != nil {
			return Result{Output: fmt.Sprintf("identical command already running as background job %s; collect its output with job_output", existing.id)}
		}
		if t.Jobs.ActiveCount() >= maxBackgroundJobs {
			return Result{Err: fmt.Sprintf("后台 job 已达上限 %d：先 /jobs kill 或 job_kill 释放", maxBackgroundJobs)}
		}
		if err := cmd.Start(); err != nil {
			return Result{Err: err.Error()}
		}
		j := t.Jobs.Spawn(command, cmd, stdout, stderr)
		id := t.Jobs.Register(j)
		return Result{Output: fmt.Sprintf("background job %s started: %s\nCollect output with job_output (id %q); /jobs lists all; /jobs kill %s or job_kill stops it. Its completion arrives with the user's next message.", id, FirstLine(command), id, id)}
	}

	if err := cmd.Start(); err != nil {
		return Result{Err: err.Error()}
	}
	j := t.Jobs.Spawn(command, cmd, stdout, stderr)

	// turn 中止要能杀死仍在前台跑的命令：挂 watcher 监听 ctx；晋升后以
	// stop 收刀——job 脱离 turn 生命周期，后续中止不波及。
	stopAbort := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			j.kill()
		case <-j.done:
		case <-stopAbort:
		}
	}()

	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()
	select {
	case <-j.done:
		return jobForegroundResult(j, stdout, stderr)
	case <-timer.C:
		close(stopAbort)
		id := t.Jobs.Register(j)
		soFar := mergeStderrIntoOutput(stdout.snapshot(), stderr.snapshot(), 1)
		msg := fmt.Sprintf("[still running after %ds; moved to background job %s]\n%s", timeoutSec, id, soFar)
		return Result{
			Output:    msg,
			Truncated: stdout.truncated(),
			Extra: map[string]any{
				"stderr":     stderr.snapshot(),
				"durationMs": time.Since(j.started).Milliseconds(),
			},
		}
	}
}

// jobForegroundResult 前台正常结束的结果装配：与旧路径同一形状（I5）。
func jobForegroundResult(j *backgroundJob, stdout, stderr *jobBuffer) Result {
	j.mu.Lock()
	exitCode := j.exitCode
	j.mu.Unlock()
	output := mergeStderrIntoOutput(stdout.snapshot(), stderr.snapshot(), exitCode)
	res := Result{
		Output:    output,
		Truncated: stdout.truncated() || stderr.truncated(),
		Extra: map[string]any{
			"exitCode":   exitCode,
			"stderr":     stderr.snapshot(),
			"durationMs": time.Since(j.started).Milliseconds(),
		},
	}
	if exitCode == -1 {
		res.Err = "command was killed before exit (aborted?)"
	}
	return res
}

// executeLegacy 无注册表的旧路径：CommandContext 超时即杀（裸构造的测试
// 与既有语义保持不变）。
func (t *BashTool) executeLegacy(ctx context.Context, command string, timeoutSec int) Result {
	tctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch t.Shell {
	case "bash", "sh":
		shell := t.Shell
		if shell == "bash" {
			shell = bashExecutable()
		}
		cmd = exec.CommandContext(tctx, shell, "-c", command)
	case "powershell":
		ps := "powershell"
		if _, err := exec.LookPath("pwsh"); err == nil {
			ps = "pwsh"
		}
		cmd = exec.CommandContext(tctx, ps, "-NoProfile", "-NonInteractive", "-Command", command)
	default:
		return Result{Err: fmt.Sprintf("unknown shell %q", t.Shell)}
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
	output := mergeStderrIntoOutput(stdout.String(), stderr.String(), exitCode)
	res := Result{Output: output, Truncated: stdout.truncated || stderr.truncated, Extra: extra}
	if timedOut {
		res.Err = fmt.Sprintf("command timed out after %ds (killed); partial output follows", timeoutSec)
	} else if runErr != nil && exitCode == -1 {
		res.Err = runErr.Error()
	}
	return res
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
