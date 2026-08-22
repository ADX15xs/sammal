// Package checkpoint 实现 git-free 的 per-turn 文件快照与回滚（第 6.4 节）。
// 仅追踪写类工具（write/edit）的文件落盘；bash 副作用明确不追踪。
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Store struct {
	root    string // <session-dir>/checkpoints
	workDir string
}

type manifestEntry struct {
	Path    string `json:"path"` // 相对 workDir
	Existed bool   `json:"existed"`
	Blob    string `json:"blob"` // checkpoints/<turn>/ 下的文件名；Existed=false 时空
}

func New(sessionDir, workDir string) *Store {
	return &Store{root: filepath.Join(sessionDir, "checkpoints"), workDir: workDir}
}

func (s *Store) turnDir(turn int) string {
	return filepath.Join(s.root, strconv.Itoa(turn))
}

// CaptureBeforeWrite 在 turn 内首次修改 path 前捕获原内容（幂等）。
// 返回是否为该 turn 的首次捕获（调用方可借此发一次性提示）。
func (s *Store) CaptureBeforeWrite(turn int, path string) (first bool, err error) {
	abs := s.abs(path)
	rel := relPath(s.workDir, abs)

	m, err := s.loadManifest(turn)
	if err != nil {
		return false, err
	}
	for _, e := range m {
		if e.Path == rel {
			return false, nil
		}
	}

	dir := s.turnDir(turn)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	entry := manifestEntry{Path: rel}
	if orig, readErr := os.ReadFile(abs); readErr == nil {
		entry.Existed = true
		entry.Blob = "blob-" + strconv.Itoa(len(m))
		if err := os.WriteFile(filepath.Join(dir, entry.Blob), orig, 0o644); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(readErr) {
		return false, readErr
	}

	m = append(m, entry)
	raw, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	return len(m) == 1, os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
}

// RewindToBefore 把文件状态回滚到 turn 之前：从最新 turn 向 turn 逐个
// 逆序恢复原始内容（后写的先还原）。返回被恢复的文件数。
func (s *Store) RewindToBefore(turn int) (int, error) {
	turns, err := s.turns()
	if err != nil {
		return 0, err
	}
	restored := 0
	// 降序应用：latest → turn。
	sort.Sort(sort.Reverse(sort.IntSlice(turns)))
	for _, t := range turns {
		if t < turn {
			break
		}
		m, err := s.loadManifest(t)
		if err != nil {
			return restored, err
		}
		for i := len(m) - 1; i >= 0; i-- {
			e := m[i]
			abs := s.abs(e.Path)
			if e.Existed {
				data, err := os.ReadFile(filepath.Join(s.turnDir(t), e.Blob))
				if err != nil {
					return restored, err
				}
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return restored, err
				}
				if err := os.WriteFile(abs, data, 0o644); err != nil {
					return restored, err
				}
			} else if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return restored, err
			}
			restored++
		}
	}
	return restored, nil
}

// Turns 列出存在的快照 turn 号（升序）。
func (s *Store) Turns() ([]int, error) { return s.turns() }

// ForgetFrom 删除 turn 及之后的快照目录（rewind 已恢复其内容）。
func (s *Store) ForgetFrom(turn int) {
	turns, err := s.turns()
	if err != nil {
		return
	}
	for _, t := range turns {
		if t >= turn {
			os.RemoveAll(s.turnDir(t))
		}
	}
}

func (s *Store) turns() ([]int, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

func (s *Store) loadManifest(turn int) ([]manifestEntry, error) {
	raw, err := os.ReadFile(filepath.Join(s.turnDir(turn), "manifest.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m []manifestEntry
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("checkpoint manifest 损坏（turn %d）：%w", turn, err)
	}
	return m, nil
}

func (s *Store) abs(rel string) string {
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Join(s.workDir, rel)
}

func relPath(base, abs string) string {
	if r, err := filepath.Rel(base, abs); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(abs)
}
