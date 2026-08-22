package tool

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobTool 模式匹配文件列表（支持 ** 递归），上限截断。
type GlobTool struct {
	WorkDir string
}

const globMaxEntries = 500

func (t *GlobTool) Name() string   { return "glob" }
func (t *GlobTool) ReadOnly() bool { return true }
func (t *GlobTool) Description() string {
	return "List files matching a glob pattern (supports ** for recursive match), relative to the working directory."
}

func (t *GlobTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"pattern": {"type": "string", "description": "Glob pattern, e.g. **/*.go or src/*.ts"},
		"path": {"type": "string", "description": "Base directory (default: working directory)"}
	},
	"required": ["pattern"]
}`)
}

func (t *GlobTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.Pattern == "" {
		return Result{Err: "pattern is required"}, nil
	}

	base := resolvePath(t.WorkDir, a.Path)
	matches, err := doublestar.Glob(os.DirFS(base), a.Pattern)
	if err != nil {
		return Result{Err: err.Error()}, nil
	}
	sort.Strings(matches)
	truncated := len(matches) > globMaxEntries
	if truncated {
		matches = matches[:globMaxEntries]
	}
	return Result{
		Output:    strings.Join(matches, "\n") + "\n",
		Truncated: truncated,
		Extra:     map[string]any{"count": len(matches)},
	}, nil
}
