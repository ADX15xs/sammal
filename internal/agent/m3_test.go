package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"sammal/internal/provider"
)

// M3 切换语义：历史 carried、请求 model ID 随切、KV 重建如实提示、
// I1 重放跨切换一致。
func TestModelSwitchCarriesHistory(t *testing.T) {
	fpA := &fakeProvider{streams: [][]provider.Chunk{textChunks("from A")}}
	fpB := &fakeProvider{streams: [][]provider.Chunk{textChunks("from B")}}
	fx := newFixture(t, fpA)
	t.Cleanup(func() { fx.ag.sess.Close() })

	fx.ag = New(Config{
		Root:     context.Background(),
		Provider: fpA,
		Session:  fx.sess,
		Registry: fx.reg,
		System:   fx.system(),
		Models: []ModelSpec{
			{Name: "alpha", ModelID: "model-a", Client: fpA},
			{Name: "beta", ModelID: "model-b", Client: fpB},
		},
	})
	events := fx.ag.Events()

	go fx.ag.Run(context.Background(), "hi")
	drainEvents(t, events, turnEnded)

	out := fx.ag.Slash("/model beta")
	if len(out) != 1 || !strings.Contains(out[0], "KV 缓存已重建") || !strings.Contains(out[0], "历史完整保留") {
		t.Fatalf("/model 输出 = %v", out)
	}
	if fx.ag.Model() != "model-b" {
		t.Errorf("当前 model = %s", fx.ag.Model())
	}

	go fx.ag.Run(context.Background(), "again")
	drainEvents(t, events, turnEnded)

	if len(fpB.calls) != 1 {
		t.Fatalf("fpB calls = %d", len(fpB.calls))
	}
	req := fpB.calls[0]
	if req.Model != "model-b" {
		t.Errorf("请求 model = %s", req.Model)
	}
	b, _ := json.Marshal(req.Messages)
	joined := string(b)
	if !strings.Contains(joined, "from A") || !strings.Contains(joined, "again") {
		t.Errorf("历史未携带: %s", joined)
	}

	// I1：request/header 记录了当时的 model，跨切换重放逐字节一致。
	pairs, err := fx.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("request headers = %d", len(pairs))
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("跨切换 request %d 重放不一致", i)
		}
	}

	// 切换事件透出（TUI 更新状态行）。
	out = fx.ag.Slash("/model alpha")
	if len(out) != 1 || !strings.Contains(out[0], "已切换") {
		t.Fatalf("切回输出 = %v", out)
	}
	// 重复切换到当前是无操作。
	out = fx.ag.Slash("/model alpha")
	if len(out) != 1 || !strings.Contains(out[0], "已在使用") {
		t.Fatalf("重复切换输出 = %v", out)
	}
}

func TestModelList(t *testing.T) {
	fpA := &fakeProvider{}
	fpB := &fakeProvider{}
	fx := newFixture(t, fpA)
	fx.ag = New(Config{
		Root: context.Background(), Provider: fpA, Session: fx.sess,
		Registry: fx.reg, System: fx.system(),
		Models: []ModelSpec{
			{Name: "alpha", ModelID: "a", Client: fpA},
			{Name: "beta", ModelID: "b", Client: fpB},
		},
	})
	if names := fx.ag.ModelNames(); len(names) != 2 || names[0] != "alpha" {
		t.Fatalf("ModelNames = %v", names)
	}
	out := fx.ag.Slash("/model")
	if len(out) != 3 || !strings.Contains(out[1], "* alpha") {
		t.Fatalf("/model 列表 = %v", out)
	}
	out = fx.ag.Slash("/model nope")
	if len(out) != 1 || !strings.Contains(out[0], "未定义") {
		t.Fatalf("未定义模型输出 = %v", out)
	}
}
