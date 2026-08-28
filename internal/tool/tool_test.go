package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	return NewRegistry(Resolve(dir, shellForTest(t), nil)...), dir
}

func shellForTest(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil && runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}

func runTool(t *testing.T, r *Registry, name, args string) Result {
	t.Helper()
	tl, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %s not found", name)
	}
	res, err := tl.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s infra error: %v", name, err)
	}
	return res
}

func TestWriteReadRoundtrip(t *testing.T) {
	r, dir := testRegistry(t)

	res := runTool(t, r, "write", `{"path":"sub/a.txt","content":"line1\nline2\nline3"}`)
	if res.Err != "" {
		t.Fatalf("write err: %s", res.Err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sub", "a.txt"))
	if string(data) != "line1\nline2\nline3" {
		t.Fatalf("file content = %q", data)
	}

	res = runTool(t, r, "read", `{"path":"sub/a.txt"}`)
	if res.Err != "" {
		t.Fatalf("read err: %s", res.Err)
	}
	want := "1\tline1\n2\tline2\n3\tline3\n"
	if res.Output != want {
		t.Errorf("read output = %q, want %q", res.Output, want)
	}
	if res.Truncated {
		t.Error("不应截断")
	}

	res = runTool(t, r, "read", `{"path":"sub/a.txt","offset":2,"limit":1}`)
	if res.Output != "2\tline2\n" {
		t.Errorf("paged read = %q", res.Output)
	}

	res = runTool(t, r, "read", `{"path":"missing.txt"}`)
	if res.Err == "" {
		t.Error("缺文件应为工具级错误")
	}
}

func TestEditUniqueMatchRequired(t *testing.T) {
	r, dir := testRegistry(t)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbeta\nalpha\n"), 0o644)

	res := runTool(t, r, "edit", `{"path":"f.txt","old_string":"alpha","new_string":"x"}`)
	if res.Err == "" || !strings.Contains(res.Err, "matched 2 times") {
		t.Errorf("多命中应报错: %+v", res)
	}
	if !strings.Contains(res.Err, "line 1") {
		t.Errorf("应回显命中位置: %s", res.Err)
	}

	res = runTool(t, r, "edit", `{"path":"f.txt","old_string":"beta","new_string":"BETA"}`)
	if res.Err != "" {
		t.Fatalf("唯一命中应成功: %s", res.Err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "alpha\nBETA\nalpha\n" {
		t.Errorf("edited = %q", data)
	}

	res = runTool(t, r, "edit", `{"path":"f.txt","old_string":"nowhere","new_string":"x"}`)
	if res.Err == "" || !strings.Contains(res.Err, "matched 0 times") {
		t.Errorf("零命中应报错: %+v", res)
	}
}

func TestBashEchoAndExitCode(t *testing.T) {
	r, _ := testRegistry(t)
	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "powershell"
	}

	var res Result
	if shell == "bash" {
		res = runTool(t, r, "bash", `{"command":"echo hello"}`)
	} else {
		res = runTool(t, r, "bash", `{"command":"Write-Output hello"}`)
	}
	if res.Err != "" {
		t.Fatalf("bash err: %s", res.Err)
	}
	if strings.TrimSpace(res.Output) != "hello" {
		t.Errorf("output = %q", res.Output)
	}

	if shell == "bash" {
		res = runTool(t, r, "bash", `{"command":"echo out; echo err 1>&2; exit 3"}`)
	} else {
		res = runTool(t, r, "bash", `{"command":"Write-Output out; Write-Error err; exit 3"}`)
	}
	code, _ := asInt(res.Extra["exitCode"])
	if code != 3 {
		t.Errorf("exitCode = %d, extra=%+v", code, res.Extra)
	}
	if res.Err != "" {
		t.Errorf("非零退出不是工具错误: %s", res.Err)
	}
}

func TestBashTimeout(t *testing.T) {
	r, _ := testRegistry(t)
	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "powershell"
	}
	var res Result
	if shell == "bash" {
		res = runTool(t, r, "bash", `{"command":"echo started; sleep 30","timeout_seconds":1}`)
	} else {
		res = runTool(t, r, "bash", `{"command":"Write-Output started; Start-Sleep 30","timeout_seconds":1}`)
	}
	if res.Err == "" || !strings.Contains(res.Err, "timed out") {
		t.Errorf("超时应报错: %+v", res)
	}
	if !strings.Contains(res.Output, "started") {
		t.Errorf("应保留部分输出: %q", res.Output)
	}
}

func TestGrep(t *testing.T) {
	r, dir := testRegistry(t)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package main\n\nfunc hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\nnope\n"), 0o644)

	res := runTool(t, r, "grep", `{"pattern":"hello","glob":"*.go"}`)
	if res.Err != "" {
		t.Fatalf("grep err: %s", res.Err)
	}
	// rg 与纯 Go 后备的路径前缀不同（rg 相对 cmd.Dir，纯 Go 相对 walk root），
	// 只断言关键部分。
	if !strings.Contains(res.Output, "a.go") || !strings.Contains(res.Output, "func hello() {}") {
		t.Errorf("grep output = %q", res.Output)
	}
	if strings.Contains(res.Output, "b.txt") {
		t.Errorf("glob 过滤失效: %q", res.Output)
	}

	res = runTool(t, r, "grep", `{"pattern":"["}`)
	if res.Err == "" {
		t.Error("非法正则应报错")
	}
}

// 取消的搜索不得静默返回空/部分输出——模型会把"无输出"当完整结论，
// 进而发起更大范围的重搜。Esc 中止依赖 ctx 一路传到 grep 实现。
func TestGrepCanceledContextMarked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644)
	tl := &GrepTool{WorkDir: dir}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tl.Execute(ctx, json.RawMessage(`{"pattern":"hello"}`))
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(res.Output, "[aborted") {
		t.Errorf("取消结果应带标注: %q", res.Output)
	}
	if !res.Truncated {
		t.Error("取消结果应标记 truncated")
	}
}

func TestGlob(t *testing.T) {
	r, dir := testRegistry(t)
	os.MkdirAll(filepath.Join(dir, "x", "y"), 0o755)
	os.WriteFile(filepath.Join(dir, "x", "y", "deep.go"), []byte("p"), 0o644)
	os.WriteFile(filepath.Join(dir, "top.md"), []byte("p"), 0o644)

	res := runTool(t, r, "glob", `{"pattern":"**/*.go"}`)
	if res.Err != "" {
		t.Fatalf("glob err: %s", res.Err)
	}
	if !strings.Contains(res.Output, "x/y/deep.go") && !strings.Contains(res.Output, filepath.Join("x", "y", "deep.go")) {
		t.Errorf("glob output = %q", res.Output)
	}
	if strings.Contains(res.Output, "top.md") {
		t.Errorf("glob 匹配过多: %q", res.Output)
	}
}

// I5：投影是纯函数；canonical 不含投影信息。
func TestProjectionSeparation(t *testing.T) {
	r, dir := testRegistry(t)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("a\n", 5000)), 0o644)
	res := runTool(t, r, "read", `{"path":"big.txt","limit":5000}`)
	if len(res.Output) <= 8192 {
		t.Fatalf("测试前提：输出应超预算，got %d", len(res.Output))
	}

	model := ForModel(res, 4096)
	if len(model) > 6000 {
		t.Errorf("ForModel 应截断: %d", len(model))
	}
	if !strings.Contains(model, "[truncated") {
		t.Error("ForModel 应含截断标记")
	}

	tuiView := ForTUI(res)
	if strings.Contains(tuiView, "\n") {
		t.Errorf("ForTUI 应单行: %q", tuiView)
	}
	if len([]rune(tuiView)) > 210 {
		t.Errorf("ForTUI 应短: %d", len([]rune(tuiView)))
	}

	// 截断确定性：同一输入两次投影字节一致（I1 重放依赖）。
	if ForModel(res, 4096) != model {
		t.Error("ForModel 不确定")
	}
}

// I2：工具目录序列化确定性 + schema 合法 JSON。
func TestRegistryDefsStable(t *testing.T) {
	r, _ := testRegistry(t)
	d1 := r.Defs()
	d2 := r.Defs()
	b1, _ := json.Marshal(d1)
	b2, _ := json.Marshal(d2)
	if string(b1) != string(b2) {
		t.Error("Defs 序列化不确定")
	}
	if len(d1) != 6 {
		t.Errorf("工具数 = %d", len(d1))
	}
	var check map[string]any
	if err := json.Unmarshal(d1[0].Function.Parameters, &check); err != nil {
		t.Errorf("read schema 非法 JSON: %v", err)
	}
}

// PowerShell 执行路径端到端（Windows 上 powershell.exe 为系统内置；
// 有 pwsh 时优先）：验证 -NoProfile/-NonInteractive 调用、输出、
// 非零退出码与超时杀进程——降级后模型侧真正吃到的就是这个路径。
func TestBashToolPowerShellPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 验证 PowerShell 路径")
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("无 powershell.exe")
	}
	r := NewRegistry(&BashTool{WorkDir: t.TempDir(), Shell: "powershell"})

	res := runTool(t, r, "bash", `{"command":"Write-Output 'PS-HELLO'; exit 3"}`)
	if strings.TrimSpace(res.Output) != "PS-HELLO" {
		t.Errorf("output = %q", res.Output)
	}
	if code, _ := asInt(res.Extra["exitCode"]); code != 3 {
		t.Errorf("exitCode = %+v", res.Extra)
	}
	if res.Err != "" {
		t.Errorf("非零退出不应是工具错误: %s", res.Err)
	}

	// 超时杀进程：Start-Sleep 超过时限被 kill，部分输出保留。
	res = runTool(t, r, "bash", `{"command":"Write-Output 'started'; Start-Sleep 30","timeout_seconds":1}`)
	if res.Err == "" || !strings.Contains(res.Err, "timed out") {
		t.Errorf("超时应报错: %+v", res)
	}
	if !strings.Contains(res.Output, "started") {
		t.Errorf("应保留部分输出: %q", res.Output)
	}
}
