package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sammal/internal/checkpoint"
	"sammal/internal/compaction"
	"sammal/internal/provider"
	"sammal/internal/session"
)

// Rewind 同时回滚（a）快照覆盖的文件（b）会话日志截断到该 turn 之前
// （第 6.4 节）。生成中不可用。
func (a *Agent) Rewind(turn int) (string, error) {
	if a.Running() {
		return "", errors.New("生成中不能 rewind，请先 Esc 中止")
	}
	if turn < 1 || turn >= a.sess.Turn() {
		return "", fmt.Errorf("turn %d 不存在（当前可回滚范围 1..%d）", turn, a.sess.Turn()-1)
	}
	n, err := a.cp.RewindToBefore(turn)
	if err != nil {
		return "", fmt.Errorf("文件回滚失败：%w", err)
	}
	a.cp.ForgetFrom(turn)
	if err := a.sess.TruncateBeforeTurn(turn); err != nil {
		return "", fmt.Errorf("日志截断失败：%w", err)
	}
	a.sess.PruneAssets()
	return fmt.Sprintf("已回滚 turn %d：恢复 %d 个文件，会话日志截断到该 turn 之前（bash 副作用不在回滚范围）", turn, n), nil
}

// autoCompact 在 step 边界检查 0.8× 阈值并触发压缩（第 6.6 节）。
func (a *Agent) autoCompact(ctx context.Context) {
	if a.window <= 0 {
		return
	}
	if !compaction.OverThreshold(a.system, a.reg.Defs(), a.sess.DeriveMessages(), a.window) {
		return
	}
	if err := a.compact(ctx); err != nil {
		a.emit(StatusEvent{Text: "自动压缩失败（继续未压缩对话）：" + err.Error()})
	}
}

// compact 执行压缩：摘要请求逐字重放原系统提示词 + 工具 schema +
// 遮蔽消息，只在尾部追加摘要指令（前缀 KV 直接命中，6.6 第 4 步）。
// 摘要请求不留痕 request/header：它是日志的确定性函数（遮蔽消息 +
// 常量模板），重放可重建；留痕反而破坏 ReplayRequestHashes 的投影语义。
func (a *Agent) compact(ctx context.Context) error {
	keptFrom, ok := compaction.SplitTail(a.sess.Events(), a.window)
	if !ok {
		return errors.New("会话尚短，无可压缩区间")
	}
	masked := a.sess.MessagesUpTo(keptFrom)
	if len(masked) == 0 {
		return errors.New("遮蔽区间为空")
	}
	req := provider.Request{
		Model:    a.model,
		System:   a.system,
		Tools:    a.reg.Defs(),
		Messages: append(masked, provider.Message{Role: "user", Content: provider.ContentFromText(compaction.SummaryInstruction)}),
	}
	a.emit(StatusEvent{Text: "上下文压缩中（遮蔽 seq " + fmt.Sprint(keptFrom-1) + " 之前）..."})

	ch, err := a.prov.Stream(ctx, req)
	if err != nil {
		return err
	}
	var summary strings.Builder
	for ck := range ch {
		if ck.Err != nil {
			return ck.Err
		}
		summary.WriteString(ck.TextDelta)
	}
	text := strings.TrimSpace(summary.String())
	if text == "" {
		return errors.New("摘要响应为空")
	}
	return a.sess.Append(session.TypeCompactionHappened, session.CompactionData{
		Summary:      text,
		SummaryRange: [2]int{a.firstMaskedSeq(keptFrom), keptFrom - 1},
		KeptFrom:     keptFrom,
	})
}

// firstMaskedSeq 返回当前遮蔽区间内第一个消息事件的 seq（记录用）。
func (a *Agent) firstMaskedSeq(keptFrom int) int {
	base := 1
	for _, env := range a.sess.Events() {
		if env.Type == session.TypeCompactionHappened {
			var cd session.CompactionData
			json.Unmarshal(env.Data, &cd)
			base = cd.KeptFrom
		}
	}
	for _, env := range a.sess.Events() {
		if env.Seq >= base && env.Seq < keptFrom {
			switch env.Type {
			case session.TypeUserMessage, session.TypeAssistantMessage, session.TypeToolResult:
				return env.Seq
			}
		}
	}
	return base
}

// switchSession 原子切换活跃会话（/new /resume /branch）。调用方须确保空闲。
func (a *Agent) switchSession(sess *session.Session) {
	h := sess.Header()
	a.system = BuildSystemPrompt(PromptFacts{Cwd: h.Cwd, OS: h.OS, Shell: h.Shell, Project: h.AgentsMD})
	if a.sess != nil && a.sess != sess {
		a.sess.Close()
	}
	a.sess = sess
	a.cp = checkpoint.New(sess.Dir(), h.Cwd)
	a.gitHintShown = false
}

// newSession 以当前会话的身份事实开新会话。AGENTS.md 从磁盘重读而非
// 继承 header：新会话 = 新前缀，用户改过项目指令应即时生效。
func (a *Agent) newSession() (*session.Session, error) {
	h := a.sess.Header()
	now := time.Now().UTC().Format(time.RFC3339)
	return session.Create(a.dataRoot, session.Header{
		ID: session.NewID(), Cwd: h.Cwd, Model: h.Model,
		Created: now, OS: h.OS, Shell: h.Shell,
		AgentsMD: ReadAgentsMD(h.Cwd),
	})
}

// branchSession 在 turn 边界 fork：日志前缀完整复制到新会话（6.5.3）。
// 只重写首行 header 的 ID 为新身份，其余事件逐字复制（seq/ts 不变，
// compaction 的 seq 引用因此保持有效）。调用方已排除生成中。
func (a *Agent) branchSession() (*session.Session, error) {
	data, err := os.ReadFile(a.sess.Path())
	if err != nil {
		return nil, err
	}
	h := a.sess.Header()
	newID := session.NewID()
	dir := filepath.Join(a.dataRoot, "sessions", session.NormalizeCwd(h.Cwd), newID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	var firstEnv session.Envelope
	if err := json.Unmarshal(firstLine(data), &firstEnv); err != nil || firstEnv.Type != session.TypeSessionHeader {
		return nil, errors.New("日志首行不是 session/header")
	}
	newHeader := h
	newHeader.ID = newID
	headerData, err := json.Marshal(newHeader)
	if err != nil {
		return nil, err
	}
	firstEnv.Data = headerData
	rewritten, err := json.Marshal(firstEnv)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(rewritten)
	out.WriteByte('\n')
	out.Write(restLines(data))

	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return nil, err
	}
	// 分支日志的 request/header 引用原会话的图片资产，重放还原依赖，必须
	// 随日志一起带走；checkpoint 属于原会话的物理写历史，不带。
	if err := a.sess.Assets().CopyTo(dir); err != nil {
		return nil, fmt.Errorf("资产复制失败：%w", err)
	}
	return session.Open(path)
}

func firstLine(data []byte) []byte {
	if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
		return data[:idx]
	}
	return data
}

func restLines(data []byte) []byte {
	if idx := bytes.IndexByte(data, '\n'); idx >= 0 && idx+1 < len(data) {
		return data[idx+1:]
	}
	return nil
}

// Slash 处理 slash 命令，返回给用户看的输出行（第 8.2 节最小命令集）。
func (a *Agent) Slash(input string) []string {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "/help":
		return []string{
			"/model [name] 切换模型；无参列出可用模型（Ctrl+P 打开选择器）",
			"/new          开新会话",
			"/attach <path> 附加图片后提交；无参列出，-clear 清空",
			"/skill [name] 展开 skill 与任务拼接提交；无参打开选择器",
			"/resume [n]   恢复历史会话；无参列出",
			"/branch       从当前 turn 分叉探索",
			"/compact      手动触发上下文压缩",
			"/rewind [n]   回滚代码与对话到 turn n 之前；无参列出可回滚的 turn",
			"/help         命令自述",
		}
	case "/model":
		if len(fields) == 1 {
			lines := []string{"可用模型（* = 当前）："}
			for _, name := range a.modelNames {
				mark := "  "
				if name == a.modelName {
					mark = "* "
				}
				lines = append(lines, mark+name)
			}
			return lines
		}
		out, err := a.switchModel(fields[1])
		if err != nil {
			return []string{"切换失败：" + err.Error()}
		}
		return out
	case "/rewind":
		if len(fields) == 1 {
			return a.listRewindable()
		}
		var n int
		if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil {
			return []string{"用法：/rewind <turn-n>"}
		}
		msg, err := a.Rewind(n)
		if err != nil {
			return []string{"rewind 失败：" + err.Error()}
		}
		return []string{msg}
	case "/new":
		if msg, ok := a.idleGuard("/new"); !ok {
			return msg
		}
		sess, err := a.newSession()
		if err != nil {
			return []string{"新会话创建失败：" + err.Error()}
		}
		a.switchSession(sess)
		return []string{"已开新会话 " + sess.Header().ID}
	case "/resume":
		return a.slashResume(fields)
	case "/branch":
		if msg, ok := a.idleGuard("/branch"); !ok {
			return msg
		}
		sess, err := a.branchSession()
		if err != nil {
			return []string{"分叉失败：" + err.Error()}
		}
		a.switchSession(sess)
		return []string{"已从当前 turn 分叉出新会话 " + sess.Header().ID + "（原会话保留）"}
	case "/compact":
		if msg, ok := a.idleGuard("/compact"); !ok {
			return msg
		}
		if err := a.compact(a.root); err != nil {
			return []string{"压缩失败：" + err.Error()}
		}
		return []string{"已压缩：旧区间以 <compacted-summary> 摘要替代，尾部原文保留"}
	default:
		return []string{fmt.Sprintf("未知命令 %s，/help 查看可用命令", fields[0])}
	}
}

// sessionStamp 渲染会话的本地可读时间。Created 是 UTC 时刻，直接显示会差
// 一个时区；解析失败（旧会话）退回 ID 前缀——它本身就是本地时间戳。旧格式
// Created 是"本地日期 + T00:00:00Z"，时刻本身无意义，Local() 会显示虚假的
// 08:00，只显示日期。
func sessionStamp(si session.SessionInfo) string {
	t, err := time.Parse(time.RFC3339, si.Created)
	if err != nil {
		return si.ID
	}
	if strings.HasSuffix(si.Created, "T00:00:00Z") {
		return t.Local().Format("01-02")
	}
	return t.Local().Format("01-02 15:04:05")
}

func (a *Agent) slashResume(fields []string) []string {
	if msg, ok := a.idleGuard("/resume"); !ok {
		return msg
	}
	sessions, err := session.ListSessions(a.dataRoot, a.sess.Header().Cwd)
	if err != nil {
		return []string{"会话列表失败：" + err.Error()}
	}
	// 过滤当前会话。
	var others []session.SessionInfo
	for _, si := range sessions {
		if si.Path != a.sess.Path() {
			others = append(others, si)
		}
	}
	if len(fields) == 1 {
		if len(others) == 0 {
			return []string{"没有其他历史会话"}
		}
		lines := []string{"历史会话（/resume <序号>）："}
		for i, si := range others {
			// ID 保留：同一分钟创建的会话时间串会撞，序号之外的唯一标识。
			lines = append(lines, fmt.Sprintf("  %d. %s  %d turns  %s", i+1, sessionStamp(si), si.Turns, si.ID))
		}
		return lines
	}
	var idx int
	if _, err := fmt.Sscanf(fields[1], "%d", &idx); err != nil || idx < 1 || idx > len(others) {
		return []string{"用法：/resume <序号>（无参查看列表）"}
	}
	sess, err := session.Open(others[idx-1].Path)
	if err != nil {
		return []string{"打开会话失败：" + err.Error()}
	}
	a.switchSession(sess)
	lines := []string{"已恢复会话 " + sess.Header().ID + "，转录如下："}
	return append(lines, sess.Transcript()...)
}

func (a *Agent) idleGuard(cmd string) ([]string, bool) {
	if a.Running() {
		return []string{"生成中不能 " + cmd + "，请先 Esc 中止"}, false
	}
	return nil, true
}

func (a *Agent) listRewindable() []string {
	turns, err := a.cp.Turns()
	if err != nil || len(turns) == 0 {
		return []string{"没有可回滚的快照（写文件后才会产生）"}
	}
	lines := []string{"可回滚的 turn（/rewind <n>）："}
	for _, t := range turns {
		lines = append(lines, fmt.Sprintf("  turn %d", t))
	}
	return lines
}
