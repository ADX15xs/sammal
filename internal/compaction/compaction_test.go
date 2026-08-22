package compaction

import (
	"encoding/json"
	"strings"
	"testing"

	"sammal/internal/provider"
	"sammal/internal/session"
	"sammal/internal/tool"
)

func TestEstimateTokens(t *testing.T) {
	ascii := EstimateTokens(strings.Repeat("a", 4000)) // 4KB → ~1000 token
	if ascii < 800 || ascii > 1200 {
		t.Errorf("ascii estimate = %d", ascii)
	}
	cjk := EstimateTokens(strings.Repeat("好", 1000)) // 3KB, 1000 字 → ~1000 token
	if cjk < 800 || cjk > 1200 {
		t.Errorf("cjk estimate = %d", cjk)
	}
	if EstimateTokens("") != 0 {
		t.Error("empty")
	}
}

func TestOverThreshold(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: strings.Repeat("a", 8000)}}
	if !OverThreshold("sys", nil, msgs, 800) {
		t.Error("8000 chars 应触发 800 窗口阈值")
	}
	if OverThreshold("sys", nil, msgs, 128000) {
		t.Error("不应触发大窗口")
	}
	if OverThreshold("sys", nil, msgs, 0) {
		t.Error("window=0 应关闭压缩")
	}
}

func synthEvents(turns int, toolResultChars int) []session.Envelope {
	var events []session.Envelope
	seq := 1
	appendEv := func(typ string) {
		events = append(events, session.Envelope{Seq: seq, Type: typ})
		seq++
	}
	for turn := 1; turn <= turns; turn++ {
		appendEv(session.TypeUserMessage)
		if toolResultChars > 0 {
			events = append(events, session.Envelope{
				Seq: seq, Type: session.TypeToolResult,
				Data: mustJSON(session.ToolResultData{
					ID:        "c",
					Canonical: tool.Result{Output: strings.Repeat("x", toolResultChars)},
				}),
			})
			seq++
		}
		appendEv(session.TypeAssistantMessage)
		appendEv(session.TypeTurnEnd)
	}
	// 尾部附加一个进行中的 turn（user 消息）。
	events = append(events, session.Envelope{Seq: seq, Type: session.TypeUserMessage,
		Data: mustJSON(session.UserMessageData{Text: "next"})})
	return events
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestSplitTail(t *testing.T) {
	// 3 个闭合 turn（每个含 100k 工具结果）+ 1 个进行中 turn。
	// 预算 16000 token 在保留 T3 + 进行中 turn 时即满足 → 遮蔽 T1/T2。
	events := synthEvents(3, 100000)
	keptFrom, ok := SplitTail(events, 100000)
	if !ok {
		t.Fatal("应可切分")
	}
	// 每 turn 4 事件：T1=1..4，T2=5..8，T3=9..12，进行中=13。
	if keptFrom != 9 {
		t.Errorf("keptFrom = %d, want 9（T3 起点）", keptFrom)
	}

	// 总 turn 数 = 闭合 + 进行中；1 闭合 + 1 进行中 = 2 turn，
	// 保留尾部至少 2 turn → 无遮蔽区间，不可压缩。
	if _, ok := SplitTail(synthEvents(1, 100000), 100000); ok {
		t.Error("2 turn 不应可压缩")
	}

	// 全部 turn 都很大、窗口极小：预算在保留 2 turn 时即满足，
	// 切分点 = T5 起点（seq 17；每 turn 4 事件）。
	events = synthEvents(5, 100000)
	keptFrom, ok = SplitTail(events, 100)
	if !ok || keptFrom != 17 {
		t.Errorf("大内容场景 keptFrom = %d ok = %v, want 17", keptFrom, ok)
	}
}
