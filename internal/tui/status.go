package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/compaction"
	"sammal/internal/human"
)

// warnRatio ctx% 变黄预警线（压缩触发线 compaction.TriggerRatio 变红）。
const warnRatio = 0.7

// warnContextPressure 投影逼近压缩触发线时提示一次（把「下一轮要压缩、
// 会变慢且缓存重建」提前解释给用户）。TurnEnded 时判定，一轮只报一次。
func (m Model) warnContextPressure() tea.Cmd {
	if m.windowTokens <= 0 || m.usage == nil {
		return nil
	}
	if r := float64(m.usage.PromptTokens) / float64(m.windowTokens); r >= compaction.TriggerRatio && !m.ctxWarned {
		m.ctxWarned = true
		return m.printScroll(dim(fmt.Sprintf("| 上下文已达窗口 %d%%（压缩触发线 %d%%）：下一轮可能自动压缩并重建 KV 缓存",
			int(r*100), int(compaction.TriggerRatio*100))))
	}
	return nil
}

// statusSeg 是状态栏的一个显示段。
type statusSeg struct {
	text string // 已着色的最终文本
	pri  int    // 丢弃优先级：越大越先丢；负值 = 永不丢（模型名、生成中标记）
}

// statusLine 状态栏。空间不足时按丢弃优先级从高到低：工具数(5) → cache(3)
// → in/out(2) → 计时器(2) → ctx(1)；同优先级丢更靠左的（见 dropToFit），
// 故 in/out 先于计时器。模型名与生成中标记永不丢。计时器原为 4，在极窄
// 终端仅晚于工具数被丢；降到 2 后让位给 cache/in/out，但仍保 ctx——ctx
// 决定下一轮是否压缩，是决策信息，计时器是安慰信息，二者冲突时先丢计时器。
func (m Model) statusLine() string {
	segs := []statusSeg{{text: m.modelName}}
	if m.usage != nil {
		segs = append(segs,
			statusSeg{text: fmt.Sprintf("in %d out %d", m.usage.PromptTokens, m.usage.CompletionTokens), pri: 2},
			statusSeg{text: m.cachePart(), pri: 3},
			statusSeg{text: m.ctxPart(), pri: 1},
		)
	}
	if m.busy && m.toolCalls > 0 {
		segs = append(segs, statusSeg{text: fmt.Sprintf("工具 %d", m.toolCalls), pri: 5})
	}
	if m.busy && m.turnStart.After(time.Time{}) {
		segs = append(segs, statusSeg{text: "* " + human.Duration(time.Since(m.turnStart)), pri: 2})
	} else if m.busy {
		// 等待期（已提交、TurnStarted 未到）：spinner 帧随心跳推进，
		// 避免「生成中」长时间静止而被误读为卡死。负优先级 = 永不丢。
		segs = append(segs, statusSeg{text: "* 生成中 " + spinnerFrames[m.tickN%len(spinnerFrames)], pri: -1})
	}

	width := m.width
	if width < 20 {
		width = 20
	}
	const separator = " | "
	segs = dropToFit(segs, width-3) // 行首空格 + 安全边距
	return dim(" " + strings.Join(segTexts(segs), separator))
}

// dropToFit 超预算时按优先级从右往左逐段丢弃（负优先级段不可丢），
// 直到塞下或只剩不可丢段——宁可溢出不丢语义。
func dropToFit(segs []statusSeg, budget int) []statusSeg {
	const separator = " | "
	for widthOf(strings.Join(segTexts(segs), separator)) > budget && len(segs) > 1 {
		drop := -1
		for i := len(segs) - 1; i >= 1; i-- { // segs[0] 模型名永不丢；并列时丢更靠左的（右往左扫 + >=）
			if segs[i].pri < 0 {
				continue // 负优先级段不可丢弃
			}
			if drop == -1 || segs[i].pri >= segs[drop].pri {
				drop = i
			}
		}
		if drop == -1 {
			break
		}
		segs = append(segs[:drop], segs[drop+1:]...)
	}
	return segs
}

func segTexts(segs []statusSeg) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.text
	}
	return out
}

// cachePart 缓存命中率段（无数据返回空串）。
func (m Model) cachePart() string {
	if r := m.usage.CacheHitRatio(); r >= 0 {
		return fmt.Sprintf("cache %d%%", int(r*100))
	}
	return ""
}

// ctxPart 上下文窗口占用百分比；逼近压缩触发线时变色预警。
// 无 usage 或未知窗口时返回空串（调用方过滤）。
// 变色段自带完整包裹（color+reset），嵌入外层 dim 文本时会终止 dim——
// 有意为之：预警色必须盖过 dim。
func (m Model) ctxPart() string {
	if m.windowTokens <= 0 || m.usage == nil || m.usage.PromptTokens <= 0 {
		return ""
	}
	r := float64(m.usage.PromptTokens) / float64(m.windowTokens)
	pct := fmt.Sprintf("ctx %d%%", int(r*100))
	switch {
	case r >= compaction.TriggerRatio:
		return ansiRed + pct + ansiReset
	case r >= warnRatio:
		return ansiYellow + pct + ansiReset
	default:
		return pct
	}
}
