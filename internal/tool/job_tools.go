package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// JobOutputTool 收取后台 job 输出（M5）。无参 = 列出全部 job；带 id =
// 非阻塞读取自上次以来的新增输出。没有阻塞等待参数——「等完成」走前台
// 路径由晋升兜底（M5 压测修订）。
type JobOutputTool struct {
	Jobs *JobRegistry
}

func (t *JobOutputTool) Name() string   { return "job_output" }
func (t *JobOutputTool) ReadOnly() bool { return true }
func (t *JobOutputTool) Description() string {
	return "List background jobs or read new output from one since your last read (non-blocking). A finished job's output also arrives automatically with the user's next message."
}

func (t *JobOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"id": {"type": "string", "description": "Job id (e.g. \"j1\"). Omit to list all jobs with status"}
	}
}`)
}

func (t *JobOutputTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.ID == "" {
		jobs := t.Jobs.List()
		if len(jobs) == 0 {
			return Result{Output: "no background jobs"}, nil
		}
		out := make([]string, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, fmt.Sprintf("[%s %s] %s (%s, %d bytes)", j.ID, j.Status, j.Command, j.Elapsed, j.Total))
		}
		return Result{Output: strings.Join(out, "\n")}, nil
	}
	view, output, ok := t.Jobs.Output(a.ID)
	if !ok {
		return Result{Err: fmt.Sprintf("unknown job %q (see job_output with no id)", a.ID)}, nil
	}
	head := fmt.Sprintf("[%s %s] %s (%s, %d bytes)", view.ID, view.Status, view.Command, view.Elapsed, view.Total)
	if output == "" {
		output = "(no new output)"
	}
	return Result{Output: head + "\n" + output}, nil
}

// JobKillTool 击杀后台 job 的 shell 进程（模型侧；人侧等价物是 /jobs kill）。
// 后代进程按既有孤儿语义继续（2026-10-23 决策）。
type JobKillTool struct {
	Jobs *JobRegistry
}

func (t *JobKillTool) Name() string   { return "job_kill" }
func (t *JobKillTool) ReadOnly() bool { return false }
func (t *JobKillTool) Description() string {
	return "Stop a background job by id. Its descendants are left running per the orphan rule."
}

func (t *JobKillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"id": {"type": "string", "description": "Job id to stop (e.g. \"j1\")"}
	},
	"required": ["id"]
}`)
}

func (t *JobKillTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	view, ok := t.Jobs.Kill(a.ID)
	if !ok {
		return Result{Err: fmt.Sprintf("unknown job %q (see job_output with no id)", a.ID)}, nil
	}
	if view.Status != "running" {
		return Result{Output: fmt.Sprintf("[%s %s] already finished, nothing to kill", view.ID, view.Status)}, nil
	}
	return Result{Output: fmt.Sprintf("stopped %s (%s); descendants keep running per the orphan rule", view.ID, view.Command)}, nil
}
