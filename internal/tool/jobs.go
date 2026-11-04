package tool

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// JobRegistry 是后台 bash job 注册表（M5 第 6.3 节）：后台命令与被晋升
// 前台命令的唯一归宿。job 结算经 OnComplete 回调落会话日志（session 包
// 持锁），经 Settled 通道唤醒 TUI 状态栏；两者都可缺省（测试只关心语义）。
type JobRegistry struct {
	mu         sync.Mutex
	jobs       []*backgroundJob
	counter    int
	OnComplete func(JobInfo)
	settled    chan JobInfo
}

// 后台并发上限与结算 tail 预算。上限拦截的是模型无节制后台化（压测 A3）；
// 晋升不受上限约束——超时晋升是超时语义的新承诺，杀掉等于回到旧世界。
// 不设运行时长上限：dev server 类长跑 job 由 /jobs 人侧兜底。
const (
	maxBackgroundJobs = 4
	jobTailBytes      = 4096 // 每流截尾，stdout+stderr 合计 ≤8KB（对齐剪枝阈值量级）
)

// job 结算三态（job/completed 事件的 status 字段）。
const (
	JobDone   = "done"
	JobFailed = "failed"
	JobKilled = "killed"
)

// JobInfo 结算快照：落日志与通知 TUI 的单份数据。
type JobInfo struct {
	ID         string
	Command    string
	Status     string
	ExitCode   int
	Tail       string
	TotalBytes int64
	DurationMs int64
}

// JobView 是 /jobs 与 job_output 列表的单行视图。
type JobView struct {
	ID      string
	Status  string
	Command string
	Elapsed time.Duration
	Total   int64
}

// backgroundJob 一个后台命令：进程、双流环缓冲与结算状态。Wait 的唯一
// 所有者是 spawn 挂出的 wait goroutine；晋升只是对已运行的 job 补注册，
// 绝不二次 Wait。
type backgroundJob struct {
	id      string
	command string
	cmd     *exec.Cmd
	kill    func() // Process.Kill；后代进程按既有孤儿语义继续（2026-10-23 决策）
	stdout  *jobBuffer
	stderr  *jobBuffer
	outCur  int64 // job_output 单读者游标（模型是唯一读取方）
	errCur  int64
	started time.Time

	mu       sync.Mutex
	status   string // running | done | failed | killed
	exitCode int
	// registered 标记是否已入册：前台命令未被晋升就结束时从未属于注册表，
	// 其结算不产生 job/completed、不通知 TUI。
	registered bool
	done       chan struct{}
}

func NewJobRegistry() *JobRegistry {
	return &JobRegistry{settled: make(chan JobInfo, 16)}
}

// Settled 返回结算通知通道（TUI 状态栏徽标的唤醒源）。
func (r *JobRegistry) Settled() <-chan JobInfo { return r.settled }

// ActiveCount 运行中的已入册 job 数（状态栏徽标与退出警告）。
func (r *JobRegistry) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, j := range r.jobs {
		if j.statusLocked() == "running" {
			n++
		}
	}
	return n
}

// List 全部已入册 job 的视图（/jobs 与 job_output 无参形态）。
func (r *JobRegistry) List() []JobView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]JobView, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j.viewLocked())
	}
	return out
}

// Spawn 以未注册形态接管一个已 Start 的命令：挂 Wait 与 kill。前台命令
// 先 Spawn 再按需 Register；后台命令 Spawn 后立即 Register。
func (r *JobRegistry) Spawn(command string, cmd *exec.Cmd, stdout, stderr *jobBuffer) *backgroundJob {
	j := &backgroundJob{
		command: command,
		cmd:     cmd,
		stdout:  stdout,
		stderr:  stderr,
		started: time.Now(),
		status:  "running",
		done:    make(chan struct{}),
	}
	j.kill = func() { _ = j.cmd.Process.Kill() }
	go r.wait(j)
	return j
}

// Register 入册并分配 id：后台命令立即调用；前台超时晋升时调用。晋升
// 不做上限检查（见常量注释）。
func (r *JobRegistry) Register(j *backgroundJob) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counter++
	j.mu.Lock()
	j.id = fmt.Sprintf("j%d", r.counter)
	j.registered = true
	j.mu.Unlock()
	r.jobs = append(r.jobs, j)
	return j.id
}

// wait 等待进程退出并结算；未入册的前台命令静默返回（结果由前台路径
// 自行装配）。结算先于 close(done)：前台路径看到 done 即可安全读状态。
func (r *JobRegistry) wait(j *backgroundJob) {
	waitErr := j.cmd.Wait()

	j.mu.Lock()
	j.status = statusOf(j, waitErr)
	j.exitCode = exitCode(j, waitErr)
	info := JobInfo{ID: j.id, Command: j.command, Status: j.status, ExitCode: j.exitCode,
		Tail: tailOf(j), TotalBytes: j.totalLocked(), DurationMs: time.Since(j.started).Milliseconds()}
	registered := j.registered
	j.mu.Unlock()
	close(j.done)

	if !registered {
		return
	}
	if r.OnComplete != nil {
		r.OnComplete(info)
	}
	select {
	case r.settled <- info:
	default: // TUI 未消费时丢弃：状态栏徽标下次渲染直接读 List
	}
}

func statusOf(j *backgroundJob, waitErr error) string {
	if j.status == JobKilled {
		return JobKilled
	}
	if exitCode(j, waitErr) == 0 {
		return JobDone
	}
	return JobFailed
}

func exitCode(j *backgroundJob, waitErr error) int {
	if j.cmd.ProcessState != nil {
		return j.cmd.ProcessState.ExitCode()
	}
	if waitErr != nil {
		return -1
	}
	return 0
}

// tailOf 结算 tail：stdout 尾 + stderr 尾，各 ≤4KB（落 job/completed）。
func tailOf(j *backgroundJob) string {
	out := j.stdout.tail(jobTailBytes)
	if errTail := j.stderr.tail(jobTailBytes); errTail != "" {
		out += "\nstderr:\n" + errTail
	}
	return out
}

// findRunning 同命令幂等（压测 A3：防本地小模型对 "still running" 反应性
// 重试叠加）：已有运行中的同命令 job 时返回它。
func (r *JobRegistry) findRunning(command string) *backgroundJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.statusLocked() == "running" && j.command == strings.TrimSpace(command) {
			return j
		}
	}
	return nil
}

// Output 推进单读者游标，返回自上次以来的新增输出（环内已覆盖的部分以
// skip 注记标明）；job 已结算时返回剩余输出。
func (r *JobRegistry) Output(id string) (JobView, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.id != id {
			continue
		}
		view := j.viewLocked()
		j.mu.Lock()
		out, outSkip := j.stdout.read(j.outCur)
		j.outCur = j.stdout.totalLocked()
		errText, errSkip := j.stderr.read(j.errCur)
		j.errCur = j.stderr.totalLocked()
		j.mu.Unlock()
		var b strings.Builder
		if outSkip > 0 {
			fmt.Fprintf(&b, "[...skipped %d bytes...]\n", outSkip)
		}
		b.WriteString(out)
		if errText != "" {
			b.WriteString("\nstderr:\n" + errText)
		}
		if errSkip > 0 {
			fmt.Fprintf(&b, "\n[...skipped %d stderr bytes...]", errSkip)
		}
		return view, b.String(), true
	}
	return JobView{}, "", false
}

// Kill 击杀 job 的 shell 进程；后代进程按既有孤儿语义继续。
func (r *JobRegistry) Kill(id string) (JobView, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.id != id {
			continue
		}
		j.mu.Lock()
		wasRunning := j.status == "running"
		if wasRunning {
			j.status = JobKilled
		}
		view := JobView{ID: j.id, Status: j.status, Command: FirstLine(j.command),
			Elapsed: time.Since(j.started).Truncate(time.Second),
			Total:   j.stdout.totalLocked() + j.stderr.totalLocked()}
		j.mu.Unlock()
		if wasRunning {
			j.kill()
		}
		return view, true
	}
	return JobView{}, false
}

// Close 击杀全部运行中 job 并等待结算（进程退出与测试清理）。
func (r *JobRegistry) Close() {
	r.mu.Lock()
	var running []*backgroundJob
	for _, j := range r.jobs {
		if j.statusLocked() == "running" {
			j.mu.Lock()
			j.status = JobKilled
			j.mu.Unlock()
			j.kill()
			running = append(running, j)
		}
	}
	r.mu.Unlock()
	for _, j := range running {
		<-j.done
	}
}

func (j *backgroundJob) viewLocked() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobView{ID: j.id, Status: j.status, Command: FirstLine(j.command), Elapsed: time.Since(j.started).Truncate(time.Second), Total: j.stdout.totalLocked() + j.stderr.totalLocked()}
}

// statusLocked / totalLocked 在已持有或将要获取 j.mu 的调用方间共用；
// 内部再取各自缓冲锁（锁序恒为 registry → job → buffer）。
func (j *backgroundJob) statusLocked() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

func (j *backgroundJob) totalLocked() int64 {
	return j.stdout.totalLocked() + j.stderr.totalLocked()
}

// jobBuffer 滑动环缓冲：保留最后 bashOutputCap 字节与累计计数，写入并发
// 安全。前台结果在环未超限时与原 limitBuffer 逐字节一致（超限从「留头」
// 变为「留尾」——模型的最终状态比开头更有用，行为变化随 M5 声明）。
type jobBuffer struct {
	mu    sync.Mutex
	buf   []byte
	total int64
}

func newJobBuffer() *jobBuffer { return &jobBuffer{buf: make([]byte, 0, bashOutputCap)} }

func (b *jobBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	if len(p) >= bashOutputCap {
		b.buf = append(b.buf[:0], p[len(p)-bashOutputCap:]...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - bashOutputCap; over > 0 {
		// 覆盖头部：copy 对同一数组是 memmove 安全的
		copy(b.buf, b.buf[over:])
		b.buf = b.buf[:bashOutputCap]
	}
	return len(p), nil
}

// read 自 cursor 起的新增内容；cursor 之前的环外内容计入 skipped。
func (b *jobBuffer) read(from int64) (text string, skipped int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := b.total - int64(len(b.buf))
	if from < start {
		skipped = start - from
		from = start
	}
	if from < b.total {
		text = string(b.buf[from-start:])
	}
	return text, skipped
}

// snapshot 环内全部内容（晋升消息携带的「已有输出」）。
func (b *jobBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// truncated 报告环是否发生过覆盖（对应旧 limitBuffer 的 truncated）。
func (b *jobBuffer) truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > int64(len(b.buf))
}

// tail 最近 n 字节。
func (b *jobBuffer) tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := len(b.buf) - n; s > 0 {
		return string(b.buf[s:])
	}
	return string(b.buf)
}

func (b *jobBuffer) totalLocked() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// FirstLine 取首行（job 视图与注入注记共用；session 包复用同一实现）。
func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
