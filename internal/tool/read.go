package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ReadTool 读文件，行号前缀，offset/limit 分页。
type ReadTool struct {
	WorkDir string
}

func (t *ReadTool) Name() string   { return "read" }
func (t *ReadTool) ReadOnly() bool { return true }
func (t *ReadTool) Description() string {
	return "Read a text file with 1-based line numbers. Use offset/limit to page through large files."
}

func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "File path, relative to the working directory or absolute"},
		"offset": {"type": "integer", "description": "1-based line to start from", "minimum": 1},
		"limit": {"type": "integer", "description": "Max lines to return (default 2000)", "minimum": 1}
	},
	"required": ["path"]
}`)
}

const readDefaultLimit = 2000

func (t *ReadTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.Path == "" {
		return Result{Err: "path is required"}, nil
	}
	if a.Offset < 1 {
		a.Offset = 1
	}
	if a.Limit < 1 {
		a.Limit = readDefaultLimit
	}

	abs := resolvePath(t.WorkDir, a.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{Err: err.Error()}, nil
	}
	if !utf8.Valid(data) {
		return Result{Err: fmt.Sprintf("%s is not valid UTF-8 (binary file?)", a.Path)}, nil
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	end := min(a.Offset-1+a.Limit, len(lines))
	var b strings.Builder
	for i := a.Offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	return Result{
		Output:    b.String(),
		Truncated: end < len(lines),
		Extra: map[string]any{
			"path": a.Path, "startLine": a.Offset, "endLine": end, "totalLines": len(lines),
		},
	}, nil
}
