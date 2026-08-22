package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool 整文件写入，父目录自动创建。
type WriteTool struct {
	WorkDir string
}

func (t *WriteTool) Name() string   { return "write" }
func (t *WriteTool) ReadOnly() bool { return false }
func (t *WriteTool) Description() string {
	return "Write an entire file, creating parent directories as needed. Overwrites existing content."
}

func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "File path, relative to the working directory or absolute"},
		"content": {"type": "string", "description": "Full file content to write"}
	},
	"required": ["path", "content"]
}`)
}

func (t *WriteTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.Path == "" {
		return Result{Err: "path is required"}, nil
	}

	abs := resolvePath(t.WorkDir, a.Path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if err := os.WriteFile(abs, []byte(a.Content), 0o644); err != nil {
		return Result{Err: err.Error()}, nil
	}
	return Result{
		Output: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path),
		Extra:  map[string]any{"path": a.Path, "bytes": len(a.Content)},
	}, nil
}
