// Package tool 定义 Tool 接口与六件套实现（第 6.3 节）。
// I5：Execute 返回无损的 canonical value（入日志、供模型消费），
// 「模型看到什么」「界面画什么」由独立投影函数决定。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"sammal/internal/provider"
)

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // 手写 JSON Schema，静态（I2）
	ReadOnly() bool          // true = 不触发快照
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result 是 canonical value：无损、结构化、入日志。
// 工具级失败放 Err（模型可见、可自行修正）；error 仅用于基础设施故障。
type Result struct {
	Err       string         `json:"error,omitempty"`
	Output    string         `json:"output,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// ForModel 是模型视角的投影：截断策略在此，不改日志（I5）。
// 截断必须是确定性的：同一 canonical 与 budget 产出相同字节（I1 重放依赖）。
func ForModel(r Result, budget int) string {
	var b strings.Builder
	if r.Err != "" {
		b.WriteString("error: " + r.Err + "\n")
	}
	body := r.Output
	if budget > 0 && len(body) > budget {
		head := budget * 3 / 4
		tail := budget / 4
		b.WriteString(body[:head])
		fmt.Fprintf(&b, "\n...[truncated %d chars]...\n", len(body)-head-tail)
		b.WriteString(body[len(body)-tail:])
	} else {
		b.WriteString(body)
	}
	if r.Truncated {
		b.WriteString("\n[output truncated by tool limit]")
	}
	if code, ok := asInt(r.Extra["exitCode"]); ok && code != 0 {
		fmt.Fprintf(&b, "\nexit code: %d", code)
	}
	if stderr, ok := r.Extra["stderr"].(string); ok && stderr != "" {
		b.WriteString("\nstderr:\n" + stderr)
	}
	return b.String()
}

// asInt 归一化 canonical 中的整数字段：内存值是 int，日志重放后是 float64。
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// ForTUI 是界面视角的投影：单行摘要。
func ForTUI(r Result) string {
	line := strings.TrimSpace(r.Output)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if r.Err != "" {
		line = "error: " + r.Err
	}
	if widthOfRunes(line) > 200 {
		runes := []rune(line)
		line = string(runes[:197]) + "..."
	}
	if r.Truncated {
		line += " (+)"
	}
	return line
}

func widthOfRunes(s string) int { return len([]rune(s)) }

// Registry 是工具注册表；order 固定保证 Tools 序列化确定（I2）。
type Registry struct {
	byName map[string]Tool
	order  []string
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.byName[t.Name()] = t
		r.order = append(r.order, t.Name())
	}
	return r
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Defs 按注册顺序返回静态工具目录。
func (r *Registry) Defs() []provider.ToolDef {
	defs := make([]provider.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		t := r.byName[name]
		defs = append(defs, provider.ToolDef{
			Type: provider.ToolTypeFunction,
			Function: provider.ToolFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			},
		})
	}
	return defs
}

// decodeArgs 把 JSON 参数解到具体结构；失败转为模型可见的工具错误。
func decodeArgs(args json.RawMessage, dst any) error {
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("参数解析失败：%w（原始参数 %s）", err, string(args))
	}
	return nil
}

// resolvePath 把工具参数中的路径解析为基于工作目录的绝对路径。
func resolvePath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(workDir, p)
}
