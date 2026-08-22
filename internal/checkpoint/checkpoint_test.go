package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	return New(filepath.Join(dir, "sess"), work), work
}

func TestCaptureAndRewindSameTurn(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	os.WriteFile(f, []byte("original"), 0o644)

	if _, err := s.CaptureBeforeWrite(1, "a.txt"); err != nil {
		t.Fatal(err)
	}
	// 同 turn 第二次捕获是 no-op。
	if _, err := s.CaptureBeforeWrite(1, "a.txt"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, []byte("modified"), 0o644)

	n, err := s.RewindToBefore(1)
	if err != nil || n != 1 {
		t.Fatalf("rewind = %d, %v", n, err)
	}
	data, _ := os.ReadFile(f)
	if string(data) != "original" {
		t.Errorf("file = %q", data)
	}
}

// 跨 turn 同文件：逆序恢复保证回到更早状态。
func TestRewindAcrossTurns(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "a.txt")
	os.WriteFile(f, []byte("v1"), 0o644)

	s.CaptureBeforeWrite(1, "a.txt")
	os.WriteFile(f, []byte("v2"), 0o644)
	s.CaptureBeforeWrite(2, "a.txt")
	os.WriteFile(f, []byte("v3"), 0o644)

	if _, err := s.RewindToBefore(1); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f)
	if string(data) != "v1" {
		t.Errorf("file = %q, want v1", data)
	}
}

func TestRewindNewFileIsDeleted(t *testing.T) {
	s, work := newStore(t)
	f := filepath.Join(work, "new.txt")

	s.CaptureBeforeWrite(3, "new.txt") // 不存在的文件
	os.WriteFile(f, []byte("created"), 0o644)

	if _, err := s.RewindToBefore(3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("新建文件应被删除: %v", err)
	}
}

func TestForgetFrom(t *testing.T) {
	s, work := newStore(t)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("x"), 0o644)
	s.CaptureBeforeWrite(1, "a.txt")
	s.CaptureBeforeWrite(2, "a.txt")

	s.ForgetFrom(2)
	turns, _ := s.Turns()
	if len(turns) != 1 || turns[0] != 1 {
		t.Errorf("turns = %v", turns)
	}
	s.ForgetFrom(1)
	turns, _ = s.Turns()
	if len(turns) != 0 {
		t.Errorf("turns = %v", turns)
	}
}
