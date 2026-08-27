package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func collectChunks(t *testing.T, ch <-chan Chunk) []Chunk {
	t.Helper()
	var out []Chunk
	for ck := range ch {
		out = append(out, ck)
	}
	return out
}

func TestStreamTextAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"delta":{"role":"assistant"}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"你好"}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"，世界"}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":8,"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":36}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch, err := c.Stream(context.Background(), Request{Model: "m", System: "s"})
	if err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, ch)

	var text string
	var usage *Usage
	var finish string
	for _, ck := range chunks {
		text += ck.TextDelta
		if ck.Usage != nil {
			usage = ck.Usage
		}
		if ck.FinishReason != "" {
			finish = ck.FinishReason
		}
		if ck.Err != nil {
			t.Fatalf("unexpected err chunk: %v", ck.Err)
		}
	}
	if text != "你好，世界" {
		t.Errorf("text = %q", text)
	}
	if finish != "stop" {
		t.Errorf("finish = %q", finish)
	}
	if usage == nil || usage.PromptTokens != 100 || usage.PromptCacheHitTokens != 64 {
		t.Errorf("usage = %+v", usage)
	}
	if got := usage.CacheHitRatio(); got < 0.63 || got > 0.65 {
		t.Errorf("cache ratio = %f", got)
	}
}

func TestStreamReasoningVariants(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"choices":[{"delta":{"`+field+`":"think"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		}))
		c := NewClient(srv.URL, "")
		ch, err := c.Stream(context.Background(), Request{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		var reason string
		for ck := range ch {
			if ck.Err != nil {
				t.Fatalf("%s: err chunk: %v", field, ck.Err)
			}
			reason += ck.ReasonDelta
		}
		if reason != "think" {
			t.Errorf("%s: reason = %q", field, reason)
		}
		srv.Close()
	}
}

func TestStreamToolCallAggregation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.txt\"}"}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"bash","arguments":"{}"}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch, err := c.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	type agg struct{ id, name, args string }
	got := map[int]*agg{}
	var finish string
	for ck := range ch {
		if d := ck.ToolCallDelta; d != nil {
			a := got[d.Index]
			if a == nil {
				a = &agg{}
				got[d.Index] = a
			}
			a.id += d.ID
			a.name += d.Name
			a.args += d.ArgsDelta
		}
		if ck.FinishReason != "" {
			finish = ck.FinishReason
		}
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q", finish)
	}
	if len(got) != 2 {
		t.Fatalf("aggregated calls = %d", len(got))
	}
	if got[0].id != "call_1" || got[0].name != "read" || got[0].args != `{"path":"a.txt"}` {
		t.Errorf("call0 = %+v", got[0])
	}
	if got[1].id != "call_2" || got[1].name != "bash" {
		t.Errorf("call1 = %+v", got[1])
	}
}

func TestStreamOpenAICachedTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":40}}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch, _ := c.Stream(context.Background(), Request{Model: "m"})
	var usage *Usage
	for ck := range ch {
		if ck.Usage != nil {
			usage = ck.Usage
		}
	}
	if usage == nil || usage.CachedTokens != 40 || usage.CacheHitTokens() != 40 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestStreamWatchdogStall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"first"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// 此后静默，触发看门狗
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	c.StallTimeout = 150 * time.Millisecond
	ch, err := c.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var last Chunk
	for ck := range ch {
		last = ck
	}
	var se *StreamInterruptedError
	if last.Err == nil || !errors.As(last.Err, &se) || se.Kind != InterruptStall {
		t.Fatalf("last chunk err = %v", last.Err)
	}
}

func TestStreamContextOverflowClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested ..."}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	if !IsContextOverflow(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptProtocol {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamNetworkError(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptNetwork {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamCleanEOFWithoutDoneIsInterrupt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"partial"},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch, err := c.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, ch)
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatal("expected interrupt on EOF without [DONE]")
	}
}

func TestStreamAbortMidStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient(srv.URL, "")
	ch, err := c.Stream(ctx, Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	var lastErr error
	for ck := range ch {
		if ck.Err != nil {
			lastErr = ck.Err
		}
	}
	if !errors.Is(lastErr, context.Canceled) {
		t.Fatalf("lastErr = %v", lastErr)
	}
}

func TestStreamSSEMultiDataLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": keepalive\n\n")
		io.WriteString(w, "data:{\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch, _ := c.Stream(context.Background(), Request{Model: "m"})
	var text string
	var errChunk error
	for ck := range ch {
		text += ck.TextDelta
		if ck.Err != nil {
			errChunk = ck.Err
		}
	}
	if text != "x" || errChunk != nil {
		t.Errorf("text = %q err = %v", text, errChunk)
	}
}

func TestRequestWireFormat(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	req := Request{
		Model:  "qwen3:32b",
		System: "You are Sammal.",
		Messages: []Message{
			{Role: "user", Content: ContentFromText("hi")},
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID:       "c1",
				Type:     ToolTypeFunction,
				Function: FunctionCall{Name: "read", Arguments: `{"path":"a"}`},
			}}},
			{Role: "tool", ToolCallID: "c1", Content: ContentFromText("1\ta")},
			{Role: "user", Content: ContentFromText("go on")},
		},
		Tools: []ToolDef{{
			Type: ToolTypeFunction,
			Function: ToolFunction{
				Name:        "read",
				Description: "Read a file",
				Parameters:  jsonRaw(`{"type":"object"}`),
			},
		}},
	}
	c := NewClient(srv.URL, "")
	ch, err := c.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	want := `{"model":"qwen3:32b","messages":[{"role":"system","content":[{"type":"text","text":"You are Sammal."}]},` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},` +
		`{"role":"tool","content":[{"type":"text","text":"1\ta"}],"tool_call_id":"c1"},` +
		`{"role":"user","content":[{"type":"text","text":"go on"}]}],` +
		`"tools":[{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object"}}}],` +
		`"stream":true,"stream_options":{"include_usage":true}}`
	if string(body) != want {
		t.Errorf("wire body mismatch:\n got: %s\nwant: %s", body, want)
	}

	// 确定性：同一请求两次序列化字节一致（I2 前提）。
	b1, _ := MarshalRequest(req)
	b2, _ := MarshalRequest(req)
	if string(b1) != string(b2) {
		t.Error("MarshalRequest not deterministic")
	}
	h1, _ := PrefixHash(req)
	h2, _ := PrefixHash(req)
	if h1 != h2 || len(h1) != 64 {
		t.Errorf("PrefixHash unstable: %s vs %s", h1, h2)
	}
	if !strings.HasPrefix(string(b1), `{"model":"qwen3:32b","messages":[{"role":"system","content":[{"type":"text","text":"You are Sammal."}]}`) {
		t.Error("static prefix must lead the body")
	}
}

func jsonRaw(s string) []byte { return []byte(s) }

// --- 限流 / 服务端错误分类（第 6.1 节三档：限流重试、5xx 重试、配额快速失败）---

func statusHandler(status int, body string, header map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func TestStreamRateLimitWithRetryAfterSeconds(t *testing.T) {
	srv := httptest.NewServer(statusHandler(http.StatusTooManyRequests,
		`{"error":{"message":"rate limit exceeded"}}`, map[string]string{"Retry-After": "12"}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptRateLimit || se.RetryAfter != 12*time.Second {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamRateLimitWithHTTPDate(t *testing.T) {
	reset := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	srv := httptest.NewServer(statusHandler(http.StatusTooManyRequests, `{"error":{}}`,
		map[string]string{"Retry-After": reset}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptRateLimit {
		t.Fatalf("err = %v", err)
	}
	if se.RetryAfter < 60*time.Second || se.RetryAfter > 90*time.Second {
		t.Fatalf("RetryAfter = %v, want ~(90s - elapsed)", se.RetryAfter)
	}
}

func TestStreamRateLimitNoHeader(t *testing.T) {
	srv := httptest.NewServer(statusHandler(http.StatusTooManyRequests, `{"error":{}}`, nil))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptRateLimit || se.RetryAfter != 0 {
		t.Fatalf("err = %v", err)
	}
}

// 订阅制 coding/token plan 用量窗口耗尽：429 且响应体命中配额特征，
// 归为配额窗口（agent 侧快速失败，不烧重试预算）。
func TestStreamQuotaMarkerClassification(t *testing.T) {
	srv := httptest.NewServer(statusHandler(http.StatusTooManyRequests,
		`{"error":{"type":"usage_limit_reached","message":"You have hit your usage limit. Your 5-hour window resets soon."}}`, nil))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptQuota {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamServerErrorClassification(t *testing.T) {
	srv := httptest.NewServer(statusHandler(http.StatusServiceUnavailable, `upstream gone`, nil))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.Stream(context.Background(), Request{Model: "m"})
	var se *StreamInterruptedError
	if !errors.As(err, &se) || se.Kind != InterruptServerError {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	futureDate := now.Add(30 * time.Second)
	pastDate := now.Add(-time.Hour)

	tests := []struct {
		name   string
		header map[string][]string
		want   time.Duration
	}{
		{"无头部", nil, 0},
		{"秒数", map[string][]string{"Retry-After": {"12"}}, 12 * time.Second},
		{"带空白的秒数", map[string][]string{"Retry-After": {" 7 "}}, 7 * time.Second},
		{"HTTP日期", map[string][]string{"Retry-After": {futureDate.Format(http.TimeFormat)}}, 30 * time.Second},
		{"过去的HTTP日期", map[string][]string{"Retry-After": {pastDate.Format(http.TimeFormat)}}, 0},
		{"垃圾值", map[string][]string{"Retry-After": {"soon"}}, 0},
		{"reset的unix秒", map[string][]string{"X-Ratelimit-Reset": {fmt.Sprint(futureDate.Unix())}}, 30 * time.Second},
		{"reset的RFC3339", map[string][]string{"X-Ratelimit-Reset": {futureDate.Format(time.RFC3339)}}, 30 * time.Second},
		{"reset过去时点", map[string][]string{"X-Ratelimit-Reset": {fmt.Sprint(pastDate.Unix())}}, 0},
		{"reset垃圾值", map[string][]string{"X-Ratelimit-Reset": {"6m30s"}}, 0},
		{"RetryAfter优先", map[string][]string{"Retry-After": {"5"}, "X-Ratelimit-Reset": {fmt.Sprint(futureDate.Unix())}}, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(http.Header(tt.header), now)
			if got < tt.want-time.Second || got > tt.want+time.Second {
				t.Errorf("parseRetryAfter = %v, want ≈%v", got, tt.want)
			}
		})
	}
}

// APIKeyEnv → Authorization 头的完整链路锁定：有 key 发 Bearer，
// 无 key 不发该头（本地端点无需鉴权）。
func TestStreamAuthorizationHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret-token-123")
	ch, err := c.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotHeader != "Bearer secret-token-123" {
		t.Errorf("Authorization = %q", gotHeader)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv2.Close()

	c2 := NewClient(srv2.URL, "") // 本地端点无 key
	ch2, err := c2.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for range ch2 {
	}
	if gotHeader != "" {
		t.Errorf("无 key 不应发 Authorization, got %q", gotHeader)
	}
}
