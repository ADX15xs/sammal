package provider

import (
	"encoding/json"
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
