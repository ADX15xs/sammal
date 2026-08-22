package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EditTool 精确字符串替换：old_string 必须唯一命中，否则报错并回显命中数
// 与上下文（不做 fuzzy 容错——模型自己会修正）。
type EditTool struct {
	WorkDir string
}

func (t *EditTool) Name() string   { return "edit" }
func (t *EditTool) ReadOnly() bool { return false }
func (t *EditTool) Description() string {
	return "Replace an exact string in a file. old_string must match exactly once; include surrounding lines to disambiguate."
}

func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "File path, relative to the working directory or absolute"},
		"old_string": {"type": "string", "description": "Exact text to replace (must be unique in the file)"},
		"new_string": {"type": "string", "description": "Replacement text"}
	},
	"required": ["path", "old_string", "new_string"]
}`)
}

func (t *EditTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.Path == "" || a.OldString == "" {
		return Result{Err: "path and old_string are required"}, nil
	}

	abs := resolvePath(t.WorkDir, a.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{Err: err.Error()}, nil
	}
	content := string(data)

	matches := strings.Count(content, a.OldString)
	if matches != 1 {
		return Result{
			Err: fmt.Sprintf("old_string matched %d times in %s (must match exactly once); %s",
				matches, a.Path, matchContext(content, a.OldString)),
			Extra: map[string]any{"path": a.Path, "matches": matches},
		}, nil
	}

	updated := strings.Replace(content, a.OldString, a.NewString, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return Result{Err: err.Error()}, nil
	}
	return Result{
		Output: fmt.Sprintf("replaced 1 occurrence in %s", a.Path),
		Extra:  map[string]any{"path": a.Path, "matches": 1},
	}, nil
}

// matchContext 命中 0 次时回显文件首部帮助模型定位；多次时列出各命中位置行号。
func matchContext(content, old string) string {
	if !strings.Contains(content, old) {
		lines := strings.Split(content, "\n")
		end := min(20, len(lines))
		return "file starts with:\n" + strings.Join(lines[:end], "\n")
	}
	var positions []string
	line := 1
	rest := content
	for {
		idx := strings.Index(rest, old)
		if idx < 0 {
			break
		}
		line += strings.Count(rest[:idx], "\n")
		positions = append(positions, fmt.Sprintf("line %d", line))
		rest = rest[idx+len(old):]
		line += strings.Count(old, "\n")
		if len(positions) >= 10 {
			positions = append(positions, "...")
			break
		}
	}
	return "matches at " + strings.Join(positions, ", ")
}
