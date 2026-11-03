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
	"sammal/internal/tool"
)

// TestTruncatedToolArgsSyntacticInvalid 语法非法 JSON：截断导致语法错误
// 如：`{"path":"a.txt","content":"trun`（未闭合字符串）
func TestTruncatedToolArgsSyntacticInvalid(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			// c1 参数在流中途被截断：非法 JSON（字符串未闭合）
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"a.txt","content":"trun`},
		}, {
			// c2 正常调用
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

	// 截断调用返回结构化错误结果给模型（可据此强制重试）
	var retryErr tool.Result
	for _, ev := range evs {
		if re, ok := ev.(ToolResultEvent); ok && re.ID == "c1" {
			retryErr = re.Result
		}
	}
	if retryErr.Err == "" {
		t.Fatal("截断调用应返回错误")
	}
	if retryErr.Extra == nil || retryErr.Extra["code"] != "TRUNCATED_ARGS" {
		t.Errorf("截断调用应返回结构化错误码 TRUNCATED_ARGS，实际 Extra=%v", retryErr.Extra)
	}
	if !strings.Contains(retryErr.Err, "truncated") {
		t.Errorf("截断调用应返回截断错误文案，实际 %q", retryErr.Err)
	}

	// I1：日志每一行仍是合法 JSON；c1 的 tool/call 参数为「截断原文本的 JSON 字符串占位」
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

// TestTruncatedToolArgsSemanticIncomplete 语法合法但语义不完整：必填字段缺失
// 如：`{"path":"a.txt"}` 缺少必填字段 content
func TestTruncatedToolArgsSemanticIncomplete(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			// c1 参数语法合法但缺少必填字段 content（write 工具 required: path, content）
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"a.txt"}`},
		}, {
			// c2 正常调用
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
		t.Fatalf("语义不完整应返给模型重试，stopReason = %q", te.StopReason)
	}

	// 截断调用不得执行工具
	if _, err := os.Stat(filepath.Join(fx.work, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("语义不完整参数不应执行工具，a.txt 不应存在（err=%v）", err)
	}
	// 合法调用照常执行
	data, err := os.ReadFile(filepath.Join(fx.work, "b.txt"))
	if err != nil || string(data) != "ok" {
		t.Errorf("合法调用应照常执行: %q err=%v", data, err)
	}

	// 验证结构化错误码
	var retryErr tool.Result
	for _, ev := range evs {
		if re, ok := ev.(ToolResultEvent); ok && re.ID == "c1" {
			retryErr = re.Result
		}
	}
	if retryErr.Extra == nil || retryErr.Extra["code"] != "TRUNCATED_ARGS" {
		t.Errorf("语义不完整应返回 TRUNCATED_ARGS，实际 Extra=%v", retryErr.Extra)
	}

	// I1：日志占位保留原始文本
	rawLog, err := os.ReadFile(fx.sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	placeholder := ""
	for _, line := range strings.Split(string(rawLog), "\n") {
		if line == "" {
			continue
		}
		var env session.Envelope
		json.Unmarshal([]byte(line), &env)
		if env.Type != session.TypeToolCall {
			continue
		}
		var d session.ToolCallData
		json.Unmarshal(env.Data, &d)
		if d.ID != "c1" {
			continue
		}
		json.Unmarshal(d.Args, &placeholder)
	}
	if placeholder != `{"path":"a.txt"}` {
		t.Errorf("占位应保留原始文本，实际 %q", placeholder)
	}

	// I1 重放哈希一致
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: 日志 %s != 重建 %s", i, p[0], p[1])
		}
	}
}

// TestTruncatedToolArgsEmptyStringRequired 语法合法但必填字段为空串
// 如：`{"path":"a.txt","content":""}` content 为空串
func TestTruncatedToolArgsEmptyStringRequired(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			// c1 参数 content 为空串（write 工具 required: path, content，content 不能为空）
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"a.txt","content":""}`},
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
		t.Fatalf("空串必填字段应返给模型重试，stopReason = %q", te.StopReason)
	}

	// 截断调用不得执行工具
	if _, err := os.Stat(filepath.Join(fx.work, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("空 content 不应执行工具，a.txt 不应存在")
	}
	// 合法调用照常执行
	data, err := os.ReadFile(filepath.Join(fx.work, "b.txt"))
	if err != nil || string(data) != "ok" {
		t.Errorf("合法调用应照常执行: %q err=%v", data, err)
	}

	// 验证结构化错误码
	var retryErr tool.Result
	for _, ev := range evs {
		if re, ok := ev.(ToolResultEvent); ok && re.ID == "c1" {
			retryErr = re.Result
		}
	}
	if retryErr.Extra == nil || retryErr.Extra["code"] != "TRUNCATED_ARGS" {
		t.Errorf("空串必填字段应返回 TRUNCATED_ARGS，实际 Extra=%v", retryErr.Extra)
	}

	// I1 重放哈希一致
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: 日志 %s != 重建 %s", i, p[0], p[1])
		}
	}
}

// TestTruncatedToolArgsTrailingComma 语法合法但有多余逗号（某些解析器宽容）
// 如：`{"path":"a.txt","content":"ok",}` - 标准 JSON 非法，但某些上游可能产出
func TestTruncatedToolArgsTrailingComma(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			// c1 参数有多余逗号（标准 JSON 非法，json.Valid 会拦截）
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"a.txt","content":"ok",}`},
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
		t.Fatalf("多余逗号应返给模型重试，stopReason = %q", te.StopReason)
	}

	// 多余逗号语法非法，应被拦截
	if _, err := os.Stat(filepath.Join(fx.work, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("多余逗号不应执行工具，a.txt 不应存在")
	}
	// 合法调用照常执行
	data, err := os.ReadFile(filepath.Join(fx.work, "b.txt"))
	if err != nil || string(data) != "ok" {
		t.Errorf("合法调用应照常执行: %q err=%v", data, err)
	}

	// I1 重放哈希一致
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: 日志 %s != 重建 %s", i, p[0], p[1])
		}
	}
}

// TestTruncatedToolArgsValidPasses 完整合法参数应正常执行
func TestTruncatedToolArgsValidPasses(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write", ArgsDelta: `{"path":"a.txt","content":"v1"}`},
		}, {
			ToolCallDelta: &provider.ToolCallDelta{Index: 1, ID: "c2", Name: "write", ArgsDelta: `{"path":"b.txt","content":"v2"}`},
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
		t.Fatalf("合法参数应正常完成，stopReason = %q", te.StopReason)
	}

	// 两个文件都应创建成功
	data1, err := os.ReadFile(filepath.Join(fx.work, "a.txt"))
	if err != nil || string(data1) != "v1" {
		t.Errorf("a.txt 应创建成功: %q err=%v", data1, err)
	}
	data2, err := os.ReadFile(filepath.Join(fx.work, "b.txt"))
	if err != nil || string(data2) != "v2" {
		t.Errorf("b.txt 应创建成功: %q err=%v", data2, err)
	}

	// 不应有错误结果
	for _, ev := range evs {
		if re, ok := ev.(ToolResultEvent); ok {
			if re.Result.Err != "" {
				t.Errorf("合法参数不应产生错误结果: ID=%s Err=%s", re.ID, re.Result.Err)
			}
		}
	}

	// I1 重放哈希一致
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: 日志 %s != 重建 %s", i, p[0], p[1])
		}
	}
}

// TestTruncatedToolArgsUnknownTool 未知工具仅做语法校验
func TestTruncatedToolArgsUnknownTool(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		{{
			// 未知工具，参数语法合法但语义未知
			ToolCallDelta: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "unknown_tool", ArgsDelta: `{"foo":"bar"}`},
		}, {
			ToolCallDelta: &provider.ToolCallDelta{Index: 1, ID: "c2", Name: "write", ArgsDelta: `{"path":"b.txt","content":"ok"}`},
		},
			{FinishReason: "tool_calls"},
		},
		textChunks("done"),
	}}
	fx := newFixture(t, fp)
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "test unknown tool")
	evs := drainEvents(t, events, turnEnded)
	te := evs[len(evs)-1].(TurnEndedEvent)
	if te.StopReason != StopCompleted {
		t.Fatalf("未知工具语法合法应放行（由执行层报错），stopReason = %q", te.StopReason)
	}

	// 未知工具应返回执行错误（非截断错误）
	var unknownErr tool.Result
	for _, ev := range evs {
		if re, ok := ev.(ToolResultEvent); ok && re.ID == "c1" {
			unknownErr = re.Result
		}
	}
	if unknownErr.Err == "" {
		t.Fatal("未知工具应返回错误")
	}
	if unknownErr.Extra != nil && unknownErr.Extra["code"] == "TRUNCATED_ARGS" {
		t.Errorf("未知工具不应返回 TRUNCATED_ARGS，实际 Extra=%v", unknownErr.Extra)
	}

	// 合法调用照常执行
	data, err := os.ReadFile(filepath.Join(fx.work, "b.txt"))
	if err != nil || string(data) != "ok" {
		t.Errorf("合法调用应照常执行: %q err=%v", data, err)
	}
}