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

	"sammal/internal/config"
	"sammal/internal/provider"
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

// api_key_env 的完整装配：环境变量 → Client.APIKey（发请求时落
// Authorization，见 provider 测试）；缺失时产生启动提示。
func TestAPIKeyEnvWiring(t *testing.T) {
	cfg := loadTestConfig(t) // alpha/beta 均无 api_key_env

	// 缺 env：无提示。
	if hints := missingAPIKeyHints(cfg); len(hints) != 0 {
		t.Errorf("无 api_key_env 不应提示: %v", hints)
	}

	// 配置 api_key_env 且环境变量存在：带入 Client.APIKey。
	cfg.Models["beta"] = config.Model{
		BaseURL: "http://x/v1", Model: "m", APIKeyEnv: "TEST_API_KEY",
	}
	t.Setenv("TEST_API_KEY", "k-secret")
	specs := modelSpecs(cfg)
	beta := specs[0]
	if beta.Name == "alpha" {
		beta = specs[1]
	}
	if beta.Name != "beta" {
		t.Fatalf("specs = %+v", specs)
	}
	if beta.Client.(*provider.Client).APIKey != "k-secret" {
		t.Errorf("APIKey = %q", beta.Client.(*provider.Client).APIKey)
	}

	// 配置了 api_key_env 但环境变量缺失：启动提示到位。
	os.Unsetenv("TEST_API_KEY")
	hints := missingAPIKeyHints(cfg)
	if len(hints) != 1 || !strings.Contains(hints[0], "TEST_API_KEY") {
		t.Errorf("hints = %v", hints)
	}
}
