package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"sammal/internal/provider"
	"sammal/internal/session"
)

// I7 非请求不输出：assistant 输出（assistant/chunk、assistant/message）只能
// 位于某次 user/message 开启、turn/end 尚未闭合的回合内。
func TestI7NoSpontaneousOutput(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		textChunks("first"),
		textChunks("second"),
	}}
	fx := newFixture(t, fp)
	go fx.ag.Run(context.Background(), "first question")
	drainEvents(t, fx.ag.Events(), turnEnded)
	go fx.ag.Run(context.Background(), "second question")
	drainEvents(t, fx.ag.Events(), turnEnded)

	awaitingUser, sawAssistant := true, false
	for _, env := range fx.sess.Events() {
		switch env.Type {
		case session.TypeUserMessage:
			if !awaitingUser {
				t.Fatal("user/message 出现在未闭合的回合内")
			}
			awaitingUser = false
		case session.TypeAssistantChunk, session.TypeAssistantMessage:
			if env.Type == session.TypeAssistantMessage {
				sawAssistant = true
			}
			if awaitingUser {
				t.Fatalf("%s 出现在任何 user/message 之前（自发输出）", env.Type)
			}
		case session.TypeTurnEnd:
			awaitingUser = true
		}
	}
	if !sawAssistant {
		t.Fatal("日志中无 assistant/message，测试无效")
	}
}

// I1：job/completed 注入随 user turn 进请求前缀——后台 job 在 turn 间结算，
// 归属规则（日志序紧随的下一个 user turn）必须让重放哈希逐字节一致，
// 且注入文本可从日志确定性还原（M5 归属规则的 golden 验证）。
func TestI1JobInjectionReplay(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "bash", `{"command":"echo bg-output-marker; sleep 0.3","run_in_background":true}`),
		textChunks("started"),
		textChunks("checked"),
	}}
	fx := newFixture(t, fp)
	go fx.ag.Run(context.Background(), "start it")
	drainEvents(t, fx.ag.Events(), turnEnded)

	deadline := time.Now().Add(5 * time.Second)
	for fx.jobs.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fx.jobs.ActiveCount() > 0 {
		t.Fatal("job 未在期限内结算")
	}

	go fx.ag.Run(context.Background(), "check it")
	drainEvents(t, fx.ag.Events(), turnEnded)

	// 注入出现在第二个 turn 的 user 消息尾部（投影）。
	msgs := fx.sess.DeriveMessages()
	found := false
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		text := provider.ContentText(m.Content)
		if strings.Contains(text, "check it") {
			if !strings.Contains(text, "[j1 done] echo bg-output-marker") ||
				!strings.Contains(text, "[background jobs completed since your last message]") {
				t.Fatalf("归属 turn 未含注入注记：%q", text)
			}
			if !strings.Contains(text, "bg-output-marker") {
				t.Fatalf("注入应携带结算 tail：%q", text)
			}
			found = true
		} else if strings.Contains(text, "start it") && strings.Contains(text, "background jobs completed") {
			t.Fatalf("注入泄漏到前一 turn：%q", text)
		}
	}
	if !found {
		t.Fatal("未找到归属 turn")
	}

	// 重放：全部 request 哈希成对相等（I1）。
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 {
		t.Fatalf("request headers = %d", len(pairs))
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("request %d: logged %s != rebuilt %s", i, p[0], p[1])
		}
	}
}
