package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sammal/internal/provider"
)

func TestImportImages(t *testing.T) {
	fx := newFixture(t, &fakeProvider{})
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	os.WriteFile(png, []byte("png-bytes"), 0o644)
	jpg := filepath.Join(dir, "b.JPG")
	os.WriteFile(jpg, []byte("jpg-bytes"), 0o644)

	refs, parts, err := fx.ag.importImages([]string{png, png, jpg})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || len(parts) != 2 {
		t.Fatalf("重复路径应去重: refs=%d parts=%d", len(refs), len(parts))
	}
	// 引用保留原始扩展名大小写；解析侧 ImagePart 大小写不敏感。
	if filepath.Ext(refs[0]) != ".png" || filepath.Ext(refs[1]) != ".JPG" {
		t.Errorf("引用扩展名 = %s / %s", refs[0], refs[1])
	}
	if !strings.HasPrefix(parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("png part = %+v", parts[0])
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
		t.Errorf("大写扩展名应为 jpeg: %+v", parts[1])
	}
}

func TestImportImagesErrors(t *testing.T) {
	fx := newFixture(t, &fakeProvider{})
	if _, _, err := fx.ag.importImages([]string{filepath.Join(t.TempDir(), "missing.png")}); err == nil {
		t.Error("缺失文件应报错")
	}
	big := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(big, make([]byte, 20*1024*1024+1), 0o644)
	if _, _, err := fx.ag.importImages([]string{big}); err == nil {
		t.Error("超过 20MB 应报错")
	}
	txt := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(txt, []byte("x"), 0o644)
	if _, _, err := fx.ag.importImages([]string{txt}); err == nil {
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

// 关键验收（DEBT「图片重放」主项）：带图请求可从日志逐字节重建，
// 重放哈希与留痕全部成对相等。
func TestImagesReplayHashesMatch(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "read", `{"path":"a.txt"}`),
		textChunks("done"),
	}}
	fx := newFixture(t, fp)
	os.WriteFile(filepath.Join(fx.work, "a.txt"), []byte("x"), 0o644)
	img := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(img, []byte("png-bytes"), 0o644)

	fx.ag.Submit("看这张图", []string{img})
	drainEvents(t, fx.ag.Events(), turnEnded)

	pairs, err := fx.ag.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("pairs = %d, want 2", len(pairs))
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("带图请求 %d 重放不一致", i)
		}
	}
}

// 图片不进投影：turn1 带图、turn2 请求无图——跨轮前缀与无图会话逐字节
// 一致，模型切换不受多模态限制。
func TestImagesNotInProjectionAcrossTurns(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		textChunks("t1"),
		textChunks("t2"),
	}}
	fx := newFixture(t, fp)
	img := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(img, []byte("png-bytes"), 0o644)

	fx.ag.Submit("看这张图", []string{img})
	drainEvents(t, fx.ag.Events(), turnEnded)
	fx.ag.Submit("纯文本", nil)
	drainEvents(t, fx.ag.Events(), turnEnded)

	if len(fp.calls) != 2 {
		t.Fatalf("calls = %d", len(fp.calls))
	}
	hasImage := func(req provider.Request) bool {
		for _, msg := range req.Messages {
			for _, p := range msg.Content {
				if p.Type == "image_url" {
					return true
				}
			}
		}
		return false
	}
	if !hasImage(fp.calls[0]) {
		t.Error("turn1 请求应携带图片")
	}
	if hasImage(fp.calls[1]) {
		t.Error("turn2 请求不得携带图片")
	}
}

// 压缩摘要请求与压缩后请求保持无图（摘要请求不留痕、不参与重放；
// 留痕请求的重放仍然全部一致）。
func TestCompactionWithImagesStaysLeanAndReplays(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{
		toolCallChunks("c1", "read", `{"path":"big.txt"}`), // T1 req1（带图）
		textChunks("t1-final"),                             // T1 req2（带图）
		textChunks("t2-final"),                             // T2 req3
		textChunks("这是摘要：当前任务测试。"),                         // T3 开始：压缩摘要请求（不留痕）
		textChunks("t3-final"),                             // T3 req4（压缩后）
	}}
	fx := newFixture(t, fp)
	big := strings.Repeat("x", 40*1024)
	os.WriteFile(filepath.Join(fx.work, "big.txt"), []byte(big), 0o644)
	fx.ag.window = 2000 // 阈值 1600：T1 的 read 结果（投影 ~24k 字符）远超
	img := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(img, []byte("png-bytes"), 0o644)

	fx.ag.Submit("看大文件", []string{img})
	drainEvents(t, fx.ag.Events(), turnEnded)
	fx.ag.Submit("t2", nil)
	drainEvents(t, fx.ag.Events(), turnEnded)
	fx.ag.Submit("t3", nil)
	drainEvents(t, fx.ag.Events(), turnEnded)

	if len(fp.calls) != 5 {
		t.Fatalf("calls = %d", len(fp.calls))
	}
	hasImage := func(req provider.Request) bool {
		for _, msg := range req.Messages {
			for _, p := range msg.Content {
				if p.Type == "image_url" {
					return true
				}
			}
		}
		return false
	}
	if !hasImage(fp.calls[0]) || !hasImage(fp.calls[1]) {
		t.Error("T1 请求应携带图片")
	}
	for _, i := range []int{2, 3, 4} {
		if hasImage(fp.calls[i]) {
			t.Errorf("请求 %d（T2/摘要/压缩后）不应携带图片", i)
		}
	}

	pairs, err := fx.ag.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 4 {
		t.Fatalf("留痕请求 = %d, want 4（摘要请求不留痕）", len(pairs))
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("请求 %d 重放不一致", i)
		}
	}
}

// /rewind 截断带图 turn 后，孤儿资产被剪枝。
func TestRewindPrunesOrphanAssets(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{textChunks("t1")}}
	fx := newFixture(t, fp)
	img := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(img, []byte("png-bytes"), 0o644)
	fx.ag.Submit("看图", []string{img})
	drainEvents(t, fx.ag.Events(), turnEnded)

	if _, err := fx.ag.Rewind(1); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(fx.ag.sess.Dir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("turn1 回滚后资产应被剪枝，剩余 %d 个", len(entries))
	}
}

// /branch 复制图片资产：分支日志的 request/header 引用同一组资产，
// 分支会话的重放仍然一致。
func TestBranchCopiesAssets(t *testing.T) {
	fp := &fakeProvider{streams: [][]provider.Chunk{textChunks("t1")}}
	fx := newFixture(t, fp)
	img := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(img, []byte("png-bytes"), 0o644)
	fx.ag.Submit("看图", []string{img})
	drainEvents(t, fx.ag.Events(), turnEnded)

	fx.ag.Slash("/branch")

	entries, err := os.ReadDir(filepath.Join(fx.ag.sess.Dir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("分支资产文件 = %d, want 1", len(entries))
	}
	pairs, err := fx.ag.sess.ReplayRequestHashes(fx.system(), fx.reg.Defs())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("分支请求 %d 重放不一致", i)
		}
	}
}
