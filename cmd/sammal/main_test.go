package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sammal/internal/agent"
	"sammal/internal/config"
	"sammal/internal/provider"
)

func loadTestConfig(t *testing.T) (*config.Config, string) {
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
	return cfg, path
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
	cfg, _ := loadTestConfig(t)
	specs := modelSpecs(cfg, nil)
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

// 端到端：httptest 假模型服务 → run() 全装配（config/TUI/agent/provider/
// session）→ 输入管道发送 → 断言回复与日志落盘。
func TestEndToEndRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, s := range []string{"端", "到", "端"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", s)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":3}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
default_model = "fake"

[models.fake]
base_url = %q
model = "fake-model"
context_window = 8192
`, srv.URL+"/v1")), 0o644)

	work := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	// 数据目录重定向到临时区，避免污染真实会话存储。
	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("LOCALAPPDATA", dataRoot)
	t.Setenv("XDG_DATA_HOME", dataRoot)

	var out bytes.Buffer
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run(pr, &out, cfgPath)
	}()

	// 发送消息，等回复完成后再 EOF 退出，避免与 agent 写盘竞态。
	pw.Write([]byte("hello\r"))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "端到端") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // 等 turn/end 落盘
	pw.Write([]byte("\x03"))           // 空闲 + 空输入 → 退出
	pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Logf("输出内容：\n%q", out.String())
		t.Fatal("run 未退出")
	}

	if !strings.Contains(out.String(), "端到端") {
		t.Errorf("输出缺少回复:\n%s", out.String())
	}
	sessions, err := filepath.Glob(filepath.Join(dataRoot, "sammal", "sessions", "*", "*", "session.jsonl"))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %v err = %v", sessions, err)
	}
	data, _ := os.ReadFile(sessions[0])
	for _, want := range []string{`"type":"user/message"`, `"type":"assistant/message"`, `"type":"turn/end"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("日志缺 %s:\n%s", want, data)
		}
	}
}

// api_key_env 的完整装配：进程环境变量 → Client.APIKey（发请求时落
// Authorization，见 provider 测试）；.env 兜底；两者皆缺时提示。
func TestAPIKeyEnvWiring(t *testing.T) {
	cfg, cfgPath := loadTestConfig(t) // alpha/beta 均无 api_key_env
	secrets := config.LoadEnvFile(cfgPath)
	envPath := config.EnvFile(cfgPath)

	// 缺 env：无提示。
	if hints := missingAPIKeyHints(cfg, secrets, envPath); len(hints) != 0 {
		t.Errorf("无 api_key_env 不应提示: %v", hints)
	}

	// 配置 api_key_env 且环境变量存在：环境变量优先。
	cfg.Models["beta"] = config.Model{
		BaseURL: "http://x/v1", Model: "m", APIKeyEnv: "TEST_API_KEY",
	}
	t.Setenv("TEST_API_KEY", "k-env")
	specs := modelSpecs(cfg, secrets)
	if apiKeyOf(specs, "beta") != "k-env" {
		t.Errorf("env 优先路径 APIKey = %q", apiKeyOf(specs, "beta"))
	}

	// 环境变量缺失、.env 兜底：写 %APPDATA%\sammal\.env 即生效。
	os.Unsetenv("TEST_API_KEY")
	os.WriteFile(envPath, []byte("TEST_API_KEY=k-dotenv\n"), 0o644)
	secrets = config.LoadEnvFile(cfgPath)
	specs = modelSpecs(cfg, secrets)
	if apiKeyOf(specs, "beta") != "k-dotenv" {
		t.Errorf(".env 兜底 APIKey = %q", apiKeyOf(specs, "beta"))
	}
	if hints := missingAPIKeyHints(cfg, secrets, envPath); len(hints) != 0 {
		t.Errorf(".env 已提供不应提示: %v", hints)
	}

	// 两者皆缺：提示到位，且文案指明 .env 写入位置。
	os.Remove(envPath)
	secrets = config.LoadEnvFile(cfgPath)
	hints := missingAPIKeyHints(cfg, secrets, envPath)
	if len(hints) != 1 || !strings.Contains(hints[0], "TEST_API_KEY") || !strings.Contains(hints[0], ".env") {
		t.Errorf("hints = %v", hints)
	}
}

func apiKeyOf(specs []agent.ModelSpec, name string) string {
	for _, s := range specs {
		if s.Name == name {
			return s.Client.(*provider.Client).APIKey
		}
	}
	return ""
}

// 默认路径分支：空 configPath 时 .env 必须与系统默认 config.toml
// 同目录（%APPDATA%\sammal\.env），而不是相对当前工作目录。
func TestEndToEndDefaultConfigReadsDotenv(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	// 系统默认配置目录重定向。
	cfgDir := t.TempDir()
	t.Setenv("APPDATA", cfgDir)
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "data"))

	home := filepath.Join(cfgDir, "sammal")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf(`
default_model = "cloud"

[models.cloud]
base_url = %q
model = "cloud-model"
api_key_env = "TEST_DOTENV_KEY"
context_window = 8192
`, srv.URL+"/v1")), 0o644)
	os.WriteFile(filepath.Join(home, ".env"), []byte("TEST_DOTENV_KEY=from-dotenv\n"), 0o644)

	work := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	var out bytes.Buffer
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- run(pr, &out, "") }() // 空 configPath = 默认路径分支

	pw.Write([]byte("hi\r"))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "ok") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	pw.Write([]byte("\x03"))
	pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run 未退出")
	}

	if authHeader != "Bearer from-dotenv" {
		t.Errorf("默认路径下 .env 未生效: Authorization = %q", authHeader)
	}
	if strings.Contains(out.String(), "[!] ") {
		t.Errorf("不应出现缺失密钥提示:\n%s", out.String())
	}
}
