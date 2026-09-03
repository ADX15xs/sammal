package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sammal/internal/provider"
	"sammal/internal/session"
)

// 压缩全配方：0.8× 触发 → 摘要请求逐字重放原前缀 + 追加指令 →
// compaction/happened 落日志 → 后续请求以摘要 + 尾部投影。
// 三 turn 结构：T1 读大文件；T2（仅 2 turn 不可压）；T3 开始时触发压缩。
func TestCompactionTriggeredAndPrefixReplay(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "read", `{"path":"big.txt"}`), // T1 req1
		textChunks("t1-final"),                             // T1 req2
		textChunks("t2-final"),                             // T2 req3
		textChunks("这是摘要：当前任务测试。"),                         // T3 开始：压缩摘要请求
		textChunks("t3-final"),                             // T3 req4（压缩后）
	}}
	fx := newFixture(t, fp)
	t.Cleanup(func() { fx.ag.sess.Close() })
	big := strings.Repeat("x", 40*1024)
	os.WriteFile(filepath.Join(fx.work, "big.txt"), []byte(big), 0o644)
	fx.ag.window = 2000 // 阈值 1600：T1 的 read 结果（投影 ~24k 字符）远超

	events := fx.ag.Events()
	for _, q := range []string{"read big", "t2", "t3"} {
		go fx.ag.Run(context.Background(), q)
		drainEvents(t, events, turnEnded)
	}

	// 请求序列：T1 两请求、T2 一请求、T3 摘要请求 + 压缩后请求。
	if len(fp.calls) != 5 {
		t.Fatalf("calls = %d", len(fp.calls))
	}

	// 摘要请求 = 遮蔽区间内容的逐字重放 + 追加摘要指令。
	// 遮蔽区间 = T1 全部 = T2 请求（calls[2]）去掉其末尾的 T2 user 消息。
	summaryReq := fp.calls[3]
	t2Req := fp.calls[2]
	maskedCount := len(t2Req.Messages) - 1
	if len(summaryReq.Messages) != maskedCount+1 {
		t.Fatalf("摘要请求消息数 = %d, want %d+1", len(summaryReq.Messages), maskedCount)
	}
	for i := 0; i < maskedCount; i++ {
		b1, _ := json.Marshal(t2Req.Messages[i])
		b2, _ := json.Marshal(summaryReq.Messages[i])
		if string(b1) != string(b2) {
			t.Errorf("摘要请求前缀漂移 at %d:\n %s\n %s", i, b1, b2)
		}
	}
	last := summaryReq.Messages[len(summaryReq.Messages)-1]
	if last.Role != "user" || !strings.Contains(provider.ContentText(last.Content), "结构化简报") {
		t.Errorf("摘要指令缺失: %+v", last)
	}

	// compaction/happened 落日志，含摘要与区间。
	var comp *session.CompactionData
	for _, env := range fx.sess.Events() {
		if env.Type == session.TypeCompactionHappened {
			var cd session.CompactionData
			json.Unmarshal(env.Data, &cd)
			comp = &cd
		}
	}
	if comp == nil {
		t.Fatal("compaction/happened 未落日志")
	}
	if comp.Summary == "" || comp.KeptFrom <= comp.SummaryRange[0] {
		t.Errorf("compaction data = %+v", comp)
	}

	// 压缩后请求以 <compacted-summary> 开头。
	post := fp.calls[4]
	if len(post.Messages) == 0 || !strings.HasPrefix(provider.ContentText(post.Messages[0].Content), "<compacted-summary>") {
		t.Fatalf("压缩后首消息 = %+v", post.Messages[0])
	}
	if !strings.Contains(provider.ContentText(post.Messages[0].Content), "当前任务测试") {
		t.Errorf("摘要内容未进入投影: %q", provider.ContentText(post.Messages[0].Content))
	}

	// I1：压缩后重放哈希仍一致。
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("压缩后 request %d 重放不一致", i)
		}
	}
}

// /new /resume /branch：会话生命周期。
func TestNewResumeBranch(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		textChunks("first-session-reply"),
		textChunks("branch-reply"),
		textChunks("resume-continue"),
	}}
	fx := newFixture(t, fp)
	t.Cleanup(func() { fx.ag.sess.Close() })
	events := fx.ag.Events()
	firstID := fx.sess.Header().ID

	go fx.ag.Run(context.Background(), "hello A")
	drainEvents(t, events, turnEnded)

	// /branch：从当前 turn 分叉 → session B（相同历史，独立演进）。
	out := fx.ag.Slash("/branch")
	if len(out) != 1 || !strings.Contains(out[0], "分叉") {
		t.Fatalf("/branch = %v", out)
	}
	if fx.ag.sess.Header().ID == firstID {
		t.Fatal("分叉未产生新会话")
	}
	if len(fx.ag.sess.DeriveMessages()) != 2 {
		t.Fatalf("分叉历史 = %+v", fx.ag.sess.DeriveMessages())
	}
	go fx.ag.Run(context.Background(), "hello B")
	drainEvents(t, events, turnEnded)

	// /new → 空历史的 session C。
	out = fx.ag.Slash("/new")
	if len(out) != 1 || !strings.Contains(out[0], "新会话") {
		t.Fatalf("/new = %v", out)
	}
	if len(fx.ag.sess.DeriveMessages()) != 0 {
		t.Fatal("新会话应为空历史")
	}

	// /resume 列表含原会话；按序号恢复 A。
	list := fx.ag.Slash("/resume")
	idx := -1
	for i, ln := range list[1:] {
		if strings.Contains(ln, firstID) {
			idx = i + 1
		}
	}
	if idx < 0 {
		t.Fatalf("/resume 列表缺原会话: %v", list)
	}
	out = fx.ag.Slash("/resume " + strconv.Itoa(idx))
	if len(out) < 1 || !strings.Contains(out[0], "已恢复") {
		t.Fatalf("/resume n = %v", out)
	}
	if fx.ag.sess.Header().ID != firstID {
		t.Fatalf("恢复到了错误会话: %s", fx.ag.sess.Header().ID)
	}
	msgs := fx.ag.sess.DeriveMessages()
	if len(msgs) != 2 || !strings.HasPrefix(provider.ContentText(msgs[0].Content), "Today: ") ||
		!strings.HasSuffix(provider.ContentText(msgs[0].Content), "hello A") {
		t.Fatalf("恢复后历史 = %+v", msgs)
	}

	// 恢复后继续对话，请求携带原历史。
	go fx.ag.Run(context.Background(), "continue A")
	drainEvents(t, events, turnEnded)
	lastReq := fp.calls[len(fp.calls)-1]
	b, _ := json.Marshal(lastReq.Messages)
	if !strings.Contains(string(b), "hello A") {
		t.Error("恢复后的请求缺少原历史")
	}
}

// kill -9 场景的 agent 级验收：带未闭合 turn 的日志直接重开续聊。
func TestCrashedSessionResumeContinues(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		textChunks("recovered-continue"),
	}}
	fx := newFixture(t, fp)
	t.Cleanup(func() { fx.ag.sess.Close() })

	// 手工构造崩溃态：user + 部分流式输出，无 turn/end。
	fx.sess.Append(session.TypeUserMessage, session.UserMessageData{Text: "q"})
	fx.sess.Append(session.TypeAssistantChunk, session.AssistantChunkData{Delta: "par"})
	fx.sess.Append(session.TypeAssistantChunk, session.AssistantChunkData{Delta: "tial"})
	crashedPath := fx.sess.Path()
	fx.sess.Close()

	// 重新打开：恢复合成闭合事件。
	recovered, err := session.Open(crashedPath)
	if err != nil {
		t.Fatal(err)
	}
	msgs := recovered.DeriveMessages()
	// [user q, assistant partial(interrupted)]
	if len(msgs) != 2 || provider.ContentText(msgs[1].Content) != "partial" {
		t.Fatalf("恢复后历史 = %+v", msgs)
	}

	// 换到恢复的会话继续对话。
	fx.ag.switchSession(recovered)
	events := fx.ag.Events()
	go fx.ag.Run(context.Background(), "next")
	drainEvents(t, events, turnEnded)

	req := fp.calls[len(fp.calls)-1]
	b, _ := json.Marshal(req.Messages)
	if !strings.Contains(string(b), "partial") || !strings.Contains(string(b), "next") {
		t.Errorf("续聊请求 = %s", b)
	}
}
