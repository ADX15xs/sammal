package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `
default_model = "local"

[models.local]
base_url = "http://localhost:11434/v1"
model = "qwen3:32b"

[ui]
editor = "nvim"
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models["local"].ContextWindow != DefaultContextWindow {
		t.Errorf("context_window = %d, 默认值未填充", c.Models["local"].ContextWindow)
	}
	name, m, err := c.Resolve("")
	if err != nil || name != "local" || m.Model != "qwen3:32b" {
		t.Errorf("resolve = %s %+v %v", name, m, err)
	}
	if _, _, err := c.Resolve("nope"); err == nil {
		t.Error("未定义模型应报错")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err == nil {
		t.Fatal("缺文件应报错")
	}
}
