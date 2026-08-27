// Package compaction 实现上下文压缩配方的纯逻辑部分（第 6.6 节）：
// 触发判定、token 估算、保留尾部切分与摘要指令模板。
// 配方第 1 步（工具输出剪枝）在 session 的投影器中实现——它是投影语义。
package compaction

import (
	"encoding/json"

	"github.com/mattn/go-runewidth"

	"sammal/internal/provider"
	"sammal/internal/session"
)

// unmarshal 供估算路径使用：畸形数据按 0 token 计，不中断压缩。
func unmarshal(data []byte, v any) { _ = json.Unmarshal(data, v) }

// 配方参数（Reasonix 与 dsh 独立收敛，直接继承）。
const (
	TriggerRatio = 0.8  // 投影 token ≥ 0.8 × 窗口触发
	TailRatio    = 0.16 // 最新 0.16 × 窗口的 turn 原文保留
	MinKeptTurns = 2    // 保留尾部不小于 2 个 turn
)

// SummaryInstruction 摘要指令模板。必须是常量：摘要请求的前缀重放
// （I2/6.6 第 4 步）依赖它的字节稳定。
const SummaryInstruction = `请把以上对话压缩为一份结构化简报，供接手的模型继续工作。固定以下字段，用中文，只输出简报本身：

当前任务：
关键决定：
相关文件：
已遇错误：
待办：
下一步：`

// EstimateTokens 粗估 token 数：窄字符约 4 字/token，宽字符（CJK 等）
// 约 1 字/token。宁高勿低——高估只会在阈值内提前压缩，低估才有溢出风险。
func EstimateTokens(s string) int {
	narrow, wide := 0, 0
	for _, r := range s {
		if runewidth.RuneWidth(r) >= 2 {
			wide++
		} else {
			narrow++
		}
	}
	return narrow/4 + wide
}

// EstimateRequest 估算一次请求的 token 体量。
func EstimateRequest(system string, tools []provider.ToolDef, msgs []provider.Message) int {
	total := EstimateTokens(system)
	for _, t := range tools {
		total += EstimateTokens(t.Function.Description) + EstimateTokens(string(t.Function.Parameters))
	}
	for _, m := range msgs {
		total += EstimateTokens(provider.ContentText(m.Content)) + 8
		for _, tc := range m.ToolCalls {
			total += EstimateTokens(tc.Function.Name) + EstimateTokens(tc.Function.Arguments)
		}
	}
	return total
}

// OverThreshold 判定当前投影是否触发压缩。
func OverThreshold(system string, tools []provider.ToolDef, msgs []provider.Message, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	return EstimateRequest(system, tools, msgs) >= int(TriggerRatio*float64(contextWindow))
}

// SplitTail 计算保留尾部的起始事件 seq（keptFrom，含）：
// 从最新事件向前按 turn 累计 token，达到 TailRatio×window 即止，
// 但保留不少于 MinKeptTurns 个 turn，且至少留一个 turn 给遮蔽区间。
// 进行中（未闭合）的 turn 计入保留尾部。没有可遮蔽区间时 ok=false。
func SplitTail(events []session.Envelope, contextWindow int) (keptFrom int, ok bool) {
	closed := 0
	lastTurnEndIdx := -1
	for i, env := range events {
		if env.Type == session.TypeTurnEnd {
			closed++
			lastTurnEndIdx = i
		}
	}
	// 尾部还有未闭合 turn 的事件 → 总 turn 数 = closed+1。
	total := closed
	if len(events) > lastTurnEndIdx+1 {
		total++
	}
	if total < MinKeptTurns+1 {
		return 0, false
	}

	tailBudget := int(TailRatio * float64(contextWindow))
	acc := 0
	keptTurns := 0
	// 从尾向头扫，遇到的每个 turn/end 是保留区间的左边界候选。
	for i := len(events) - 1; i >= 0; i-- {
		env := events[i]
		if isMessageBearing(env) {
			acc += eventTokens(env)
		}
		if env.Type == session.TypeTurnEnd {
			keptTurns++
			if keptTurns >= MinKeptTurns && acc >= tailBudget {
				return events[i+1].Seq, true
			}
			if keptTurns >= total-1 {
				// 再往前就没有遮蔽区间了。
				if keptTurns >= MinKeptTurns {
					return events[i+1].Seq, true
				}
				return 0, false
			}
		}
	}
	return 0, false
}

func isMessageBearing(env session.Envelope) bool {
	switch env.Type {
	case session.TypeUserMessage, session.TypeAssistantMessage, session.TypeToolResult:
		return true
	}
	return false
}

func eventTokens(env session.Envelope) int {
	switch env.Type {
	case session.TypeUserMessage:
		var d session.UserMessageData
		unmarshal(env.Data, &d)
		return EstimateTokens(d.Text)
	case session.TypeAssistantMessage:
		var d session.AssistantMessageData
		unmarshal(env.Data, &d)
		n := EstimateTokens(d.Text)
		for _, tc := range d.ToolCalls {
			n += EstimateTokens(tc.Function.Arguments)
		}
		return n
	case session.TypeToolResult:
		var d session.ToolResultData
		unmarshal(env.Data, &d)
		return EstimateTokens(d.Canonical.Output) + EstimateTokens(d.Canonical.Err)
	}
	return 0
}
