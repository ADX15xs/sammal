package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sammal/internal/config"
)

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	toml := `
default_model = "alpha"

[models.alpha]
base_url = "http://localhost:1/v1"
model = "model-a"
context_window = 4096

[models.beta]
base_url = "http://localhost:2/v1"
model = "model-b"
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEditorCommandResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.md")

	// 配置带参数：按空白切分，路径在末尾。
	cmd, err := editorCommand("code -w")(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) != 3 || cmd.Args[0] != "code" || cmd.Args[1] != "-w" || cmd.Args[2] != path {
		t.Errorf("args = %v", cmd.Args)
	}

	// 配置优先于环境变量。
	t.Setenv("VISUAL", "vi-from-env")
	cmd, _ = editorCommand("notepad")(path)
	if !strings.HasPrefix(strings.Join(cmd.Args, " "), "notepad") {
		t.Errorf("配置应优先: %v", cmd.Args)
	}
	os.Unsetenv("VISUAL")
}

func TestModelSpecsSorted(t *testing.T) {
	cfg := loadTestConfig(t)
	specs := modelSpecs(cfg)
	if len(specs) != 2 {
		t.Fatalf("specs = %d", len(specs))
	}
	if specs[0].Name != "alpha" || specs[0].ModelID != "model-a" || specs[0].Window != 4096 {
		t.Errorf("spec0 = %+v", specs[0])
	}
	if specs[1].Name != "beta" {
		t.Errorf("spec1 = %+v", specs[1])
	}
}
