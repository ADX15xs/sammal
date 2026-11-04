package tool

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// newJobsFixture 装配挂注册表的 bash 工具；结算经通道回传测试。
func newJobsFixture(t *testing.T) (*BashTool, *JobRegistry, <-chan JobInfo) {
	t.Helper()
	reg := NewJobRegistry()
	settled := make(chan JobInfo, 8)
	reg.OnComplete = func(i JobInfo) { settled <- i }
	bt := &BashTool{WorkDir: t.TempDir(), Shell: "sh", Jobs: reg}
	t.Cleanup(reg.Close)
	return bt, reg, settled
}

func waitForStatus(t *testing.T, reg *JobRegistry, id, status string) JobView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs := reg.List()
		for _, j := range jobs {
			if j.ID == id && j.Status == status {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s 未在期限内到达状态 %s", id, status)
	return JobView{}
}

func TestBashBackgroundImmediateReturn(t *testing.T) {
	bt, reg, settled := newJobsFixture(t)
	res, err := bt.Execute(context.Background(), []byte(`{"command":"echo hello-bg; sleep 0.4","run_in_background":true}`))
	if err != nil || res.Err != "" {
		t.Fatalf("后台启动失败：%v / %s", err, res.Err)
	}
	if !strings.Contains(res.Output, "background job j1") {
		t.Fatalf("应立即返回 job id，得到：%s", res.Output)
	}

	// 幂等（压测 A3）：同命令重复后台化返回既有 id，不叠加。
	res2, _ := bt.Execute(context.Background(), []byte(`{"command":"echo hello-bg; sleep 0.4","run_in_background":true}`))
	if !strings.Contains(res2.Output, "already running as background job j1") {
		t.Fatalf("同命令应返回既有 job，得到：%s", res2.Output)
	}

	view := waitForStatus(t, reg, "j1", JobDone)
	if view.Total == 0 {
		t.Fatal("done job 应有输出字节")
	}
	_, out, ok := reg.Output("j1")
	if !ok || !strings.Contains(out, "hello-bg") {
		t.Fatalf("job_output 应含输出，得到：%q", out)
	}
	info := <-settled
	if info.Status != JobDone || !strings.Contains(info.Tail, "hello-bg") {
		t.Fatalf("结算快照不符：%+v", info)
	}
}

func TestBashPromoteOnTimeout(t *testing.T) {
	bt, reg, settled := newJobsFixture(t)
	start := time.Now()
	res, err := bt.Execute(context.Background(), []byte(`{"command":"sleep 2; echo late-done","timeout_seconds":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 1800*time.Millisecond {
		t.Fatalf("晋升应在限时附近返回，实际 %s", elapsed)
	}
	if !strings.Contains(res.Output, "[still running after 1s; moved to background job j1]") {
		t.Fatalf("应晋升而非杀死，得到：%s", res.Output)
	}

	view := waitForStatus(t, reg, "j1", JobDone)
	if view.Total == 0 {
		t.Fatal("晋升 job 应有输出")
	}
	_, out, _ := reg.Output("j1")
	if !strings.Contains(out, "late-done") {
		t.Fatalf("晋升 job 的后续输出应可收取，得到：%q", out)
	}
	if info := <-settled; info.Status != JobDone {
		t.Fatalf("晋升 job 结算状态 %s", info.Status)
	}
}

func TestJobKill(t *testing.T) {
	bt, reg, settled := newJobsFixture(t)
	if res, _ := bt.Execute(context.Background(), []byte(`{"command":"sleep 30","run_in_background":true}`)); !strings.Contains(res.Output, "background job j1") {
		t.Fatalf("启动失败：%s", res.Output)
	}
	view, ok := reg.Kill("j1")
	if !ok || view.Status != JobKilled {
		t.Fatalf("击杀返回 %+v ok=%v", view, ok)
	}
	select {
	case info := <-settled:
		if info.Status != JobKilled {
			t.Fatalf("击杀后结算状态 %s", info.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("击杀后未结算")
	}
	if _, _, ok := reg.Output("j1"); !ok {
		t.Fatal("已击杀 job 仍应可查")
	}
}

func TestJobConcurrencyLimit(t *testing.T) {
	bt, _, _ := newJobsFixture(t)
	for i := 0; i < maxBackgroundJobs; i++ {
		cmd := fmt.Sprintf(`{"command":"echo bg%d; sleep 2","run_in_background":true}`, i)
		if res, _ := bt.Execute(context.Background(), []byte(cmd)); res.Err != "" {
			t.Fatalf("第 %d 个 job 启动失败：%s", i+1, res.Err)
		}
	}
	res, _ := bt.Execute(context.Background(), []byte(`{"command":"echo bg5; sleep 2","run_in_background":true}`))
	if res.Err == "" || !strings.Contains(res.Err, "上限") {
		t.Fatalf("第 5 个 job 应被拒：%+v", res)
	}
}

func TestJobOutputUnknownID(t *testing.T) {
	_, reg, _ := newJobsFixture(t)
	if _, out, ok := reg.Output("j9"); ok || out != "" {
		t.Fatal("未知 id 应不可查")
	}
	jo := &JobOutputTool{Jobs: reg}
	if res, _ := jo.Execute(context.Background(), []byte(`{"id":"j9"}`)); res.Err == "" {
		t.Fatal("未知 id 应报模型可见错误")
	}
	if res, _ := jo.Execute(context.Background(), []byte(`{}`)); res.Err != "" || !strings.Contains(res.Output, "no background jobs") {
		t.Fatalf("空列表应如实报告：%+v", res)
	}
}

func TestJobBufferRing(t *testing.T) {
	b := newJobBuffer()
	pattern := strings.Repeat("x", 64 * 1024)
	for i := 0; i < 8; i++ {
		b.Write([]byte(pattern))
	}
	if got := b.totalLocked(); got != 8*64*1024 {
		t.Fatalf("累计计数 %d", got)
	}
	if !b.truncated() {
		t.Fatal("超限应记 truncated")
	}
	text, skipped := b.read(0)
	if skipped == 0 || len(text) > bashOutputCap {
		t.Fatalf("read(0) 应跳过环外内容：skipped=%d len=%d", skipped, len(text))
	}
	// 从当前 cursor 再读：无新增
	text2, skip2 := b.read(b.totalLocked())
	if text2 != "" || skip2 != 0 {
		t.Fatalf("cursor 追平后应为空：%q skip=%d", text2, skip2)
	}
	if got := b.tail(4); got != "xxxx" {
		t.Fatalf("tail(4) = %q", got)
	}
}
