package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssetsPutIdempotent(t *testing.T) {
	a := NewAssets(t.TempDir())
	ref1, err := a.Put([]byte("img-bytes"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := a.Put([]byte("img-bytes"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Errorf("同内容应同引用: %s vs %s", ref1, ref2)
	}
	if filepath.Ext(ref1) != ".png" {
		t.Errorf("引用应保留扩展名: %s", ref1)
	}
}

func TestAssetsRoundtrip(t *testing.T) {
	a := NewAssets(t.TempDir())
	ref, err := a.Put([]byte("img-bytes"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	data, err := a.Data(ref)
	if err != nil || string(data) != "img-bytes" {
		t.Fatalf("data = %q err = %v", data, err)
	}
}

func TestAssetsPrune(t *testing.T) {
	a := NewAssets(t.TempDir())
	refKeep, err := a.Put([]byte("keep"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	refGone, err := a.Put([]byte("gone"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	a.Prune(map[string]bool{refKeep: true})
	if _, err := a.Data(refKeep); err != nil {
		t.Errorf("被引用资产不应被删: %v", err)
	}
	if _, err := a.Data(refGone); err == nil {
		t.Error("孤儿资产应被删除")
	}
}

func TestAssetsLazy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created-yet")
	a := NewAssets(dir)
	if _, err := a.Data("whatever.png"); err == nil {
		t.Error("目录不存在时 Data 应报错")
	}
	a.Prune(nil) // 目录不存在时剪枝应为空操作
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Prune 不应创建目录: %v", err)
	}
	ref, err := a.Put([]byte("x"), ".png")
	if err != nil {
		t.Fatalf("Put 应惰性创建目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "assets", ref)); err != nil {
		t.Errorf("资产文件不存在: %v", err)
	}
}
