package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContentFromText(t *testing.T) {
	parts := ContentFromText("hello")
	if len(parts) != 1 {
		t.Fatalf("len = %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "hello" {
		t.Errorf("part = %+v", parts[0])
	}
}

// 回归：空文本不产生缺 text 键的 {"type":"text"} part——vLLM 等严格
// 解析端会拒收（HTTP 500 key 'text' not found）。空文本应序列化为空数组。
func TestContentFromTextEmptyWire(t *testing.T) {
	req := Request{
		Model: "test",
		Messages: []Message{
			{Role: "assistant", Content: ContentFromText(""), ToolCalls: []ToolCall{
				{ID: "c1", Type: "function", Function: FunctionCall{Name: "read", Arguments: "{}"}},
			}},
		},
	}
	b, err := MarshalRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	msgs := v["messages"].([]any)
	asst := msgs[1].(map[string]any)
	content, ok := asst["content"].([]any)
	if !ok || len(content) != 0 {
		t.Errorf("空文本 assistant 的 content 应为空数组，got %v", asst["content"])
	}
}

func TestContentText(t *testing.T) {
	parts := []ContentPart{
		{Type: "text", Text: "hello"},
		{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,abc"}},
		{Type: "text", Text: "world"},
	}
	// ContentText 只拼接 text 类 part，跳过 image_url
	if got := ContentText(parts); got != "helloworld" {
		t.Errorf("got %q", got)
	}
	if got := ContentText([]ContentPart{{Type: "text", Text: "only"}}); got != "only" {
		t.Errorf("got %q", got)
	}
}

func TestImagePart(t *testing.T) {
	part, ok := ImagePart(".png", []byte("abc"))
	if !ok || part.Type != "image_url" || !strings.HasPrefix(part.ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("part = %+v ok = %v", part, ok)
	}
	// base64("abc") = YWJj
	if !strings.HasSuffix(part.ImageURL.URL, "YWJj") {
		t.Errorf("payload = %q", part.ImageURL.URL)
	}
	if p, ok := ImagePart(".JPG", []byte("x")); !ok || !strings.HasPrefix(p.ImageURL.URL, "data:image/jpeg;base64,") {
		t.Errorf("大写扩展名应识别为 jpeg: %+v %v", p, ok)
	}
	if _, ok := ImagePart(".txt", []byte("x")); ok {
		t.Error("不支持的扩展名应返回 false")
	}
}

func TestWireFormatImage(t *testing.T) {
	req := Request{
		Model: "test",
		Messages: []Message{
			{Role: "user", Content: []ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,abc"}},
			}},
		},
	}
	b, err := MarshalRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	msgs := v["messages"].([]any)
	first := msgs[1].(map[string]any)
	arr := first["content"].([]any)
	if len(arr) != 2 {
		t.Fatalf("len = %d", len(arr))
	}
	img := arr[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Errorf("type = %v", img["type"])
	}
}
