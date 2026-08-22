package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
)

func (t *BashTool) Name() string   { return "bash" }
func (t *BashTool) ReadOnly() bool { return false }
func (t *BashTool) Description() string {
	return "Run a shell command in the working directory. Returns stdout, stderr and exit code. Non-zero exit is not an error by itself."
}

func (t *BashTool) Schema() json.RawMessage {
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
		cmd = exec.CommandContext(tctx, t.Shell, "-c", a.Command)
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

	var stdout, stderr limitBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	timedOut := tctx.Err() == context.DeadlineExceeded
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	extra := map[string]any{
		"exitCode":   exitCode,
		"stderr":     stderr.String(),
		"durationMs": duration.Milliseconds(),
	}
	res := Result{Output: stdout.String(), Truncated: stdout.truncated || stderr.truncated, Extra: extra}
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
