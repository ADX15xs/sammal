package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sammal/internal/provider"
)

func TestAssembleImageParts(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	os.WriteFile(png, []byte("png-bytes"), 0o644)
	jpg := filepath.Join(dir, "b.JPG")
	os.WriteFile(jpg, []byte("jpg-bytes"), 0o644)

	parts, err := assembleImageParts([]string{png, jpg})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if parts[0].Type != "image_url" || !strings.HasPrefix(parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("png part = %+v", parts[0])
	}
	// base64("png-bytes") = cG5nLWJ5dGVz
	if !strings.HasSuffix(parts[0].ImageURL.URL, "cG5nLWJ5dGVz") {
		t.Errorf("png payload = %q", parts[0].ImageURL.URL)
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
		t.Errorf("大写扩展名应识别为 jpeg: %+v", parts[1])
	}
}

func TestAssembleImagePartsErrors(t *testing.T) {
	if _, err := assembleImageParts([]string{filepath.Join(t.TempDir(), "missing.png")}); err == nil {
		t.Error("缺失文件应报错")
	}
	big := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(big, make([]byte, 20*1024*1024+1), 0o644)
	if _, err := assembleImageParts([]string{big}); err == nil {
		t.Error("超过 20MB 应报错")
	}
	txt := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(txt, []byte("x"), 0o644)
	if _, err := assembleImageParts([]string{txt}); err == nil {
		t.Error("不支持的扩展名应报错")
	}
}

// 工具环的后续请求也必须携带图片：端点无状态，缺图的请求即丢失图片上下文。
func TestImagesAttachedAcrossToolLoop(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "read", `{"path":"a.txt"}`),
		textChunks("done"),
	}}
	fx := newFixture(t, fp)
	os.WriteFile(filepath.Join(fx.work, "a.txt"), []byte("x"), 0o644)
	img := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(img, []byte("png"), 0o644)

	fx.ag.Submit("看这张图", []string{img})
	drainEvents(t, fx.ag.Events(), turnEnded)

	if len(fp.calls) != 2 {
		t.Fatalf("calls = %d", len(fp.calls))
	}
	for i, req := range fp.calls {
		found := false
		for _, msg := range req.Messages {
			for _, p := range msg.Content {
				if p.Type == "image_url" {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("请求 %d 未携带图片", i)
		}
	}
}

func TestSubmitImageFileErrorAbortsTurn(t *testing.T) {
	fp := &fakeProvider{}
	fx := newFixture(t, fp)
	fx.ag.Submit("看这张图", []string{filepath.Join(t.TempDir(), "missing.png")})

	evs := drainEvents(t, fx.ag.Events(), turnEnded)
	if len(fp.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(fp.calls))
	}
	sawErr := false
	for _, ev := range evs {
		if _, ok := ev.(ErrorEvent); ok {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("图片读取失败应上报错误")
	}
}
