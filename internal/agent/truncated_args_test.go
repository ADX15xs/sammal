package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sammal/internal/provider"
	"sammal/internal/session"
)

// 上游 SSE 截断（DEBT 008.19）端到端：模型产出的工具参数 JSON 非法时，
// 不得执行工具、错误结果返给模型重试（而非 appendFatal 终止会话）；日志
// 只记合法占位（I1 可重放）；同批后续调用照常执行，turn 正常完成。
func TestTruncatedToolArgsReturnedForRetry(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			// c1 参数在流中途被截断：非法 JSON
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"a.txt","content":"trun`},
		}, {
			ToolCallDelta: &provider.ToolCallDelta{Index: 1, ID: "c2", Name: "write", ArgsDelta: `{"path":"b.txt","content":"ok"}`},
		},
			{FinishReason: "tool_calls"},
		},
		textChunks("done"),
	}}
	fx := newFixture(t, fp)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "write two files")
	evs := drainEvents(t, events, turnEnded)
	te := evs[len(evs)-1].(TurnEndedEvent)
	if te.StopReason != StopCompleted {
		t.Fatalf("截断应返给模型重试而非终止会话，stopReason = %q", te.StopReason)
	}
	for _, ev := range evs {
		if _, ok := ev.(ErrorEvent); ok {
			t.Fatal("不应出现 ErrorEvent（日志 Marshal 失败才会终止会话）")
		}
	}
	if len(fp.calls) != 2 {
		t.Fatalf("provider 请求数 = %d, want 2（首轮工具环 + 次轮文本定稿）", len(fp.calls))
	}

	// 截断调用不得执行工具；同批合法调用照常执行
	if _, err := os.Stat(filepath.Join(fx.work, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("截断参数不应执行工具，a.txt 不应存在（err=%v）", err)
	}
	data, err := os.ReadFile(filepath.Join(fx.work, "b.txt"))
	if err != nil || string(data) != "ok" {
		t.Errorf("合法调用应照常执行: %q err=%v", data, err)
	}

	// 截断调用返回错误结果给模型（可据此重试）
	var retryErr string
	for _, ev := range evs {
		if re, ok := ev.(ToolResultEvent); ok && re.ID == "c1" {
			retryErr = re.Result.Err
		}
	}
	if !strings.Contains(retryErr, "truncated") {
		t.Errorf("截断调用应返回截断错误，实际 %q", retryErr)
	}

	// I1：日志每一行仍是合法 JSON；c1 的 tool/call 参数为「截断原文本的
	// JSON 字符串占位」（json.Marshal(RawMessage) 会嵌入裸字节并校验失败，
	// 这正是原致死点——占位必须可整体序列化）。
	rawLog, err := os.ReadFile(fx.sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	placeholder := ""
	for _, line := range strings.Split(string(rawLog), "\n") {
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Fatalf("日志出现非法 JSON 行：%s", line)
		}
		var env session.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("日志行反序列化失败：%v", err)
		}
		if env.Type != session.TypeToolCall {
			continue
		}
		var d session.ToolCallData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			t.Fatalf("tool/call 数据反序列化失败：%v", err)
		}
		if d.ID != "c1" {
			continue
		}
		if !json.Valid(d.Args) {
			t.Fatalf("c1 落日志参数必须为合法占位，实际: %s", d.Args)
		}
		if err := json.Unmarshal(d.Args, &placeholder); err != nil {
			t.Fatalf("占位应可反序列化为字符串：%v", err)
		}
	}
	if placeholder != `{"path":"a.txt","content":"trun` {
		t.Errorf("占位应保留截断原文本，实际 %q", placeholder)
	}

	// I1：留痕请求哈希逐字节一致（占位保证重放不失败）
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("request headers = %d, want 2", len(pairs))
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: 日志 %s != 重建 %s", i, p[0], p[1])
		}
	}
}
