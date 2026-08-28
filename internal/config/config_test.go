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
	if c.Models["local"].RetryMax != DefaultRetryMax {
		t.Errorf("retry_max = %d, 默认值未填充", c.Models["local"].RetryMax)
	}
	if c.Models["local"].RateLimitBudget != DefaultRateLimitBudget {
		t.Errorf("rate_limit_budget = %d, 默认值未填充", c.Models["local"].RateLimitBudget)
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

func TestParseEnv(t *testing.T) {
	env := ParseEnv([]byte(
		"# 注释行\r\n" +
			"\r\n" +
			"DEEPSEEK_API_KEY=sk-secret\r\n" +
			"QUOTED=\"double quoted\"\r\n" +
			"SINGLE='single quoted'\r\n" +
			"SPACED = spaced value \r\n" +
			"no-equals-line\r\n" +
			"EMPTY=\r\n",
	))
	if env["DEEPSEEK_API_KEY"] != "sk-secret" {
		t.Errorf("key = %q", env["DEEPSEEK_API_KEY"])
	}
	if env["QUOTED"] != "double quoted" || env["SINGLE"] != "single quoted" {
		t.Errorf("quoted = %+v", env)
	}
	if env["SPACED"] != "spaced value" {
		t.Errorf("spaced = %q", env["SPACED"])
	}
	if _, ok := env["no-equals-line"]; ok {
		t.Error("无 = 行应忽略")
	}
	if _, ok := env["EMPTY"]; ok {
		t.Error("空值应忽略")
	}
}

func TestLoadEnvFileAndPriority(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_FILE=file-value\nONLY_FILE=from-file\n"), 0o644)
	cfgPath := filepath.Join(dir, "config.toml")

	env := LoadEnvFile(cfgPath)
	if env["FROM_FILE"] != "file-value" || env["ONLY_FILE"] != "from-file" {
		t.Errorf("dotenv = %+v", env)
	}

	// 进程环境变量优先于 .env。
	t.Setenv("FROM_FILE", "env-value")
	if got := ResolveSecret("FROM_FILE", env); got != "env-value" {
		t.Errorf("env 应优先: %q", got)
	}
	if got := ResolveSecret("ONLY_FILE", env); got != "from-file" {
		t.Errorf("dotenv 兜底: %q", got)
	}
	if got := ResolveSecret("NEITHER", env); got != "" {
		t.Errorf("两者皆无应为空: %q", got)
	}

	// 无 .env：空表不报错。
	if got := LoadEnvFile(filepath.Join(t.TempDir(), "config.toml")); got != nil {
		t.Errorf("缺 .env 应为空表: %+v", got)
	}
}
