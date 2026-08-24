package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GrepTool 内容搜索：PATH 中有 ripgrep 则委托，否则纯 Go 实现
// （walk + regexp）。输出 path:line:text，上限截断。
type GrepTool struct {
	WorkDir string
}

const (
	grepMaxResults = 200
	grepFileCap    = 10 * 1024 * 1024
	grepResultCap  = 64 * 1024
	// 取消的搜索必须显式标注：静默返回空/部分输出会被模型当作
	// 完整结论（"无命中"），进而发起更大范围的重搜。
	grepAbortedNote = "...[aborted by user]\n"
)

func (t *GrepTool) Name() string   { return "grep" }
func (t *GrepTool) ReadOnly() bool { return true }
func (t *GrepTool) Description() string {
	return "Search file contents with a regular expression (RE2 syntax). Output is path:line:text, capped at 200 matches."
}

func (t *GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"pattern": {"type": "string", "description": "Regular expression (RE2)"},
		"path": {"type": "string", "description": "File or directory to search (default: working directory)"},
		"glob": {"type": "string", "description": "Only search files matching this glob, e.g. *.go"}
	},
	"required": ["pattern"]
}`)
}

func (t *GrepTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return Result{Err: err.Error()}, nil
	}
	if a.Pattern == "" {
		return Result{Err: "pattern is required"}, nil
	}
	if _, err := regexp.Compile(a.Pattern); err != nil {
		return Result{Err: fmt.Sprintf("invalid pattern: %v", err)}, nil
	}

	base := resolvePath(t.WorkDir, a.Path)

	var output string
	var truncated bool
	if rg, err := exec.LookPath("rg"); err == nil {
		output, truncated = t.runRipgrep(ctx, rg, a.Pattern, base, a.Glob)
	} else {
		output, truncated = t.grepGo(ctx, a.Pattern, base, a.Glob)
	}
	return Result{
		Output:    output,
		Truncated: truncated,
		Extra:     map[string]any{"path": a.Path, "pattern": a.Pattern},
	}, nil
}

func (t *GrepTool) runRipgrep(ctx context.Context, rg, pattern, base, glob string) (string, bool) {
	args := []string{"--line-number", "--no-heading", "--max-count", fmt.Sprint(grepMaxResults), "-e", pattern}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, base)
	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = t.WorkDir
	var buf limitBuffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
			return "rg error: " + err.Error(), false
		}
		// 退出码 1 = 无命中，正常。
	}
	if ctx.Err() != nil {
		return buf.String() + grepAbortedNote, true
	}
	return buf.String(), buf.truncated
}

// grepGo 纯 Go 后备：walk + regexp，逐行匹配。
func (t *GrepTool) grepGo(ctx context.Context, pattern, base, glob string) (string, bool) {
	re := regexp.MustCompile(pattern)
	var globRe *regexp.Regexp
	if glob != "" {
		globRe = globToRegexp(glob)
	}
	matches := 0
	aborted := false
	var out bytes.Buffer
	root := base
	info, err := os.Stat(base)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		root = filepath.Dir(base)
	}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			aborted = true
			return filepath.SkipAll
		}
		if err != nil || out.Len() >= grepResultCap || matches >= grepMaxResults {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && path != base {
			return nil // 单文件模式：只查目标文件
		}
		if globRe != nil && !globRe.MatchString(filepath.ToSlash(path)) {
			return nil
		}
		match, truncated := grepFile(path, re)
		if match != "" {
			matches++
			out.WriteString(match)
		}
		if truncated {
			out.WriteString(fmt.Sprintf("...[stopped at %d matches]\n", grepMaxResults))
			return filepath.SkipAll
		}
		return nil
	})
	if aborted {
		out.WriteString(grepAbortedNote)
	}
	return out.String(), matches >= grepMaxResults || aborted
}

func grepFile(path string, re *regexp.Regexp) (match string, truncated bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > grepFileCap {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	for i, line := range strings.Split(string(data), "\n") {
		if re.MatchString(line) {
			fmt.Fprintf(&b, "%s:%d:%s\n", path, i+1, strings.TrimRight(line, "\r"))
		}
	}
	return b.String(), false
}

// globToRegexp 把 *.go 一类 shell glob 翻译为对完整路径匹配的正则。
func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(^|/)")
	for _, part := range glob {
		switch part {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '.', '(', ')', '+', '|', '^', '$', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteRune(part)
		default:
			b.WriteRune(part)
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
