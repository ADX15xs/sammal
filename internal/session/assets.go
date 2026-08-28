package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Assets 是会话目录下的内容寻址图片资产存储（assets/，与 checkpoints
// 同级）：重放还原带图请求所需的字节落盘处。引用即文件名（<sha256><ext>），
// 日志只存引用，JSONL 不膨胀。
type Assets struct {
	root string
}

// NewAssets 指向会话目录下的 assets/；目录在首次 Put 时才创建。
func NewAssets(sessionDir string) *Assets {
	return &Assets{root: filepath.Join(sessionDir, "assets")}
}

// Put 按内容寻址写入资产并返回引用：同字节幂等（已存在直接复用，去重）。
func (a *Assets) Put(data []byte, ext string) (string, error) {
	sum := sha256.Sum256(data)
	ref := hex.EncodeToString(sum[:]) + ext
	if err := os.MkdirAll(a.root, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(a.root, ref)
	if _, err := os.Stat(path); err == nil {
		return ref, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return ref, nil
}

// Data 按引用读回资产字节；引用不存在时返回错误（重放侧跳过该图）。
func (a *Assets) Data(ref string) ([]byte, error) {
	return os.ReadFile(filepath.Join(a.root, ref))
}

// Prune 删除 keep 集合之外的资产文件（/rewind 截断后剪孤儿）。
// 目录不存在时为空操作。
func (a *Assets) Prune(keep map[string]bool) {
	entries, err := os.ReadDir(a.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(a.root, e.Name()))
		}
	}
}

// CopyTo 把全部资产复制到另一会话目录的 assets/（/branch 用：分支日志
// 的 request/header 引用同一组资产，重放还原依赖）。无资产时为空操作。
func (a *Assets) CopyTo(sessionDir string) error {
	entries, err := os.ReadDir(a.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dst := NewAssets(sessionDir)
	if err := os.MkdirAll(dst.root, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(a.root, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst.root, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
