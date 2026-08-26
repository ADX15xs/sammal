// statusfit_test.go 里的矩阵测试验证逻辑正确性；这个文件提供人工目检：
// `go test ./internal/tui/ -run TestDumpStatusMatrix -v` 直接打印全部
// 状态组合的真实渲染输出（含 ANSI 色），肉眼验收布局与配色。
package tui

import (
	"fmt"
	"testing"
	"time"

	"sammal/internal/agent"
	"sammal/internal/provider"
)

func TestDumpStatusMatrix(t *testing.T) {
	type scene struct {
		name string
		make func(m Model) Model
	}
	base := New(Deps{ModelName: "qwen3:8b", ContextWindow: 32768})
	base.width = 100

	scenes := []scene{
		{"冷启动（仅模型名）", func(m Model) Model { return m }},
		{"生成中·无数据", func(m Model) Model {
			m.busy = true
			return m
		}},
		{"生成中·计时+工具+usage", func(m Model) Model {
			m.busy = true
			m.turnStart = time.Now().Add(-134 * time.Second)
			m.toolCalls = 5
			m.usage = &provider.Usage{PromptTokens: 14000, CompletionTokens: 2300, CachedTokens: 12000}
			return m
		}},
		{"思考中（流式块）", func(m Model) Model {
			m.busy = true
			m.turnStart = time.Now().Add(-38 * time.Second)
			m.thinking = true
			for _, tk := range []string{"用户", "想", "让", "我", "查", "一下", "缓存", "命中率", "，", "需要"} {
				m.appendReason(tk)
			}
			return m
		}},
		{"ctx 黄色预警 72%", func(m Model) Model {
			m.usage = &provider.Usage{PromptTokens: 23552, CompletionTokens: 500}
			return m
		}},
		{"ctx 红色预警 91%", func(m Model) Model {
			m.busy = true
			m.turnStart = time.Now().Add(-9 * time.Second)
			m.toolCalls = 2
			m.usage = &provider.Usage{PromptTokens: 29800, CompletionTokens: 800, CachedTokens: 20000}
			return m
		}},
		{"无缓存数据", func(m Model) Model {
			m.usage = &provider.Usage{PromptTokens: 1400, CompletionTokens: 300, PromptCacheMissTokens: 1400}
			return m
		}},
		{"窄终端 60 列", func(m Model) Model {
			m.width = 60
			m.busy = true
			m.turnStart = time.Now().Add(-74 * time.Second)
			m.toolCalls = 3
			m.usage = &provider.Usage{PromptTokens: 9000, CompletionTokens: 400, CachedTokens: 6000}
			return m
		}},
		{"极窄 24 列", func(m Model) Model {
			m.width = 24
			m.busy = true
			m.turnStart = time.Now().Add(-74 * time.Second)
			m.toolCalls = 3
			m.usage = &provider.Usage{PromptTokens: 9000, CompletionTokens: 400, CachedTokens: 6000}
			return m
		}},
		{"空闲·有历史 usage", func(m Model) Model {
			m.usage = &provider.Usage{PromptTokens: 14000, CompletionTokens: 2300, CachedTokens: 12000}
			return m
		}},
	}

	// 思考行单独渲染（streamBlockLines），其余直接打状态栏。
	thinking := scenes[3].make(base)
	t.Logf("\n=== 思考行（流式块内） ===\n%s",
		dim(fmt.Sprintf("%-97.97v", stripANSI(thinking.streamBlockLines(100)[0]))))

	for _, sc := range scenes[:3] {
		t.Logf("=== %s ===\n%s\n", sc.name, sc.make(base).statusLine())
	}
	for _, sc := range scenes[4:] {
		t.Logf("=== %s ===\n%s\n", sc.name, sc.make(base).statusLine())
	}
	_ = agent.TurnStartedEvent{} // 保持 import 对齐，事件类型在 reason_test 覆盖
}
