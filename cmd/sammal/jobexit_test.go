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
	"sync/atomic"
	"testing"
	"time"
)

// 复现 T14 第 3 步：退出击杀后台 job 后，killed 结算必须落进日志，
// 否则 /resume 转录里看不到那一行。
func TestEndToEndExitSettlesKilledJob(t *testing.T) {
	var reqs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		if atomic.AddInt32(&reqs, 1) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"sleep 30\\\",\\\"run_in_background\\\":true}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfgPath, []byte(fmt.Sprintf("default_model = \"fake\"\n\n[models.fake]\nbase_url = %q\nmodel = \"fake-model\"\ncontext_window = 8192\n", srv.URL+"/v1")), 0o644)

	work := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("LOCALAPPDATA", dataRoot)
	t.Setenv("XDG_DATA_HOME", dataRoot)

	var out bytes.Buffer
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- run(pr, &out, cfgPath) }()

	pw.Write([]byte("run\r"))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "background job j1") {
		time.Sleep(50 * time.Millisecond)
	}
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "ok") {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	pw.Write([]byte("\x03"))
	// 第 2 步：第一次 ctrl+c 只警告不退出。
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "个后台 job 正在运行") {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "个后台 job 正在运行") {
		t.Errorf("第一次 ctrl+c 未给出后台 job 退出警告，输出：\n%q", out.String())
	}
	select {
	case err := <-done:
		t.Fatalf("第一次 ctrl+c 就退出了（应只警告）：%v", err)
	default:
	}
	time.Sleep(200 * time.Millisecond)
	pw.Write([]byte("\x03"))
	pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Logf("输出：\n%q", out.String())
		t.Fatal("run 未退出")
	}

	sessions, _ := filepath.Glob(filepath.Join(dataRoot, "sammal", "sessions", "*", "*", "session.jsonl"))
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v", sessions)
	}
	data, _ := os.ReadFile(sessions[0])
	if !strings.Contains(string(data), "\"type\":\"job/completed\"") {
		t.Errorf("退出时被击杀的 job 没有落 job/completed")
	}
	if !strings.Contains(string(data), "\"status\":\"killed\"") {
		t.Errorf("job/completed 未标记 killed")
	}

	// 第 3 步：新进程 /resume 回放，被击杀 job 必须以一行暗色结算出现。
	var out2 bytes.Buffer
	pr2, pw2 := io.Pipe()
	done2 := make(chan error, 1)
	go func() { done2 <- run(pr2, &out2, cfgPath) }()
	pw2.Write([]byte("/resume 1\r"))
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out2.String(), "已恢复会话") {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	pw2.Write([]byte("\x03"))
	time.Sleep(200 * time.Millisecond)
	pw2.Close()

	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("第二次 run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("第二次 run 未退出")
	}
	if got := out2.String(); !strings.Contains(got, "[job j1] 已击杀：sleep 30") {
		t.Errorf("/resume 转录缺少 killed 结算行，输出：\n%q", got)
	}
}
