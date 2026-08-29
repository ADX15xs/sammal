// Package session 是事件溯源 JSONL 日志：会话的唯一真相（第 6.5 节）。
// resume、branch、compaction、TUI 转录全部是这份日志的投影，不维护影子状态。
package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"sammal/internal/provider"
	"sammal/internal/tool"
)

// 事件类型（envelope 统一：seq / ts / type / data）。
const (
	TypeSessionHeader      = "session/header"
	TypeUserMessage        = "user/message"
	TypeAssistantChunk     = "assistant/chunk"
	TypeAssistantMessage   = "assistant/message"
	TypeToolCall           = "tool/call"
	TypeToolResult         = "tool/result"
	TypeRequestHeader      = "request/header"
	TypeCompactionHappened = "compaction/happened"
	TypeTurnEnd            = "turn/end"
)

// Header 首行不可变事件：会话身份与提示词事实（重建系统提示词的足够信息）。
type Header struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Model    string `json:"model"`
	Created  string `json:"created"`
	OS       string `json:"os"`
	Shell    string `json:"shell"`
	AgentsMD string `json:"agentsMd,omitempty"` // <cwd>/AGENTS.md 内容首拍（SPEC 6.10）；旧日志无此字段 = 空
}

type Envelope struct {
	Seq  int             `json:"seq"`
	Ts   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type UserMessageData struct {
	Text   string   `json:"text"`
	Images []string `json:"images,omitempty"` // 图片资产引用（assets/ 下文件名）：turn 事实记录，投影不含图，重放按 request/header 的引用还原
}

// chunk kind 常量：assistant/chunk 的流增量类别。空值 = text（旧日志兼容）。
const (
	ChunkText      = "text"
	ChunkReasoning = "reasoning"
)

type AssistantChunkData struct {
	Delta string `json:"delta"`
	Kind  string `json:"kind,omitempty"` // text | reasoning（思考只落盘，不进模型投影）
}

type AssistantMessageData struct {
	Text        string              `json:"text"`
	ToolCalls   []provider.ToolCall `json:"toolCalls,omitempty"`
	Interrupted bool                `json:"interrupted"`
	Synthetic   bool                `json:"synthetic,omitempty"` // 崩溃恢复时自 chunk 合成
}

type ToolCallData struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type ToolResultData struct {
	ID        string      `json:"id"`
	Canonical tool.Result `json:"canonical"`
	Synthetic bool        `json:"synthetic,omitempty"`
}

// RequestHeaderData 留痕请求形态（I1）。model 字段是重放逐字节比对的
// 必要输入（会话中途切模型后请求体随之变化）；images 记录该请求尾部携带
// 的图片资产引用——图片不进投影（前缀稳定），重放据此还原。与 MessageCount
// 同为记录性字段：写入日志供消费，不参与投影。
type RequestHeaderData struct {
	PrefixHash   string   `json:"prefixHash"`
	MessageCount int      `json:"messageCount"`
	Model        string   `json:"model"`
	Images       []string `json:"images,omitempty"`
}

type CompactionData struct {
	Summary      string `json:"summary"`
	SummaryRange [2]int `json:"summaryRange"` // 被遮蔽区间的事件 seq [first, last]
	KeptFrom     int    `json:"keptFrom"`     // 此 seq 起（含）的消息保留原文
}

type TurnEndData struct {
	Turn       int    `json:"turn"`
	StopReason string `json:"stopReason"`
	Synthetic  bool   `json:"synthetic,omitempty"` // 崩溃恢复补发的闭合事件
}

// modelToolBudget 是 deriveMessages 对工具结果的投影预算（I5 的截断
// 策略）。必须是常量：请求前缀的确定性（I1/I2）依赖它。
const modelToolBudget = 24 * 1024

// Session 持有打开的日志文件与内存事件缓存。
type Session struct {
	path   string
	file   *os.File
	events []Envelope
	seq    int
	turn   int
	header Header

	assets *Assets
}

// DataRoot 返回会话数据根目录（~/.local/share/sammal 或
// %LOCALAPPDATA%\sammal）。
func DataRoot() (string, error) {
	if runtime.GOOS == "windows" {
		dir := os.Getenv("LOCALAPPDATA")
		if dir == "" {
			return "", fmt.Errorf("LOCALAPPDATA 未设置")
		}
		return filepath.Join(dir, "sammal"), nil
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "sammal"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "sammal"), nil
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// NormalizeCwd 把路径中的分隔符等 unsafe 字符替换为 '-'。
func NormalizeCwd(cwd string) string {
	return unsafeChars.ReplaceAllString(cwd, "-")
}

// NewID 生成时间基会话 ID。
func NewID() string {
	return time.Now().Format("20060102-150405") + fmt.Sprintf("-%04d", time.Now().Nanosecond()%10000)
}

// Create 建立新会话目录与日志首行。
func Create(root string, h Header) (*Session, error) {
	dir := filepath.Join(root, "sessions", NormalizeCwd(h.Cwd), h.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "session.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Session{path: path, file: f, header: h, turn: 1}
	if err := s.Append(TypeSessionHeader, h); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// Open 打开既有会话：载入事件、丢弃不完整尾部、为未闭合的 turn/step/
// tool 补发合成闭合事件（I3 崩溃恢复）。
func Open(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Session{path: path, turn: 1}
	for _, line := range splitLines(data) {
		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			break // 尾部不完整行：丢弃（I3 的「有效尾部」）
		}
		if env.Type == TypeSessionHeader {
			if err := json.Unmarshal(env.Data, &s.header); err != nil {
				return nil, fmt.Errorf("session/header 损坏：%w", err)
			}
		}
		if env.Type == TypeTurnEnd {
			var te TurnEndData
			json.Unmarshal(env.Data, &te)
			s.turn = te.Turn + 1
		}
		s.events = append(s.events, env)
		s.seq = env.Seq
	}
	if s.header.ID == "" {
		return nil, fmt.Errorf("%s 缺少 session/header", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.file = f
	s.recoverTail()
	return s, nil
}

// recoverTail 为崩溃留下的未闭合状态补发合成事件：悬挂的 tool/call 补
// 合成错误结果；已流出的 chunk 无定稿时合成 interrupted 消息；最后补
// turn/end（stopReason = crash-recovered）。合成事件落日志，保证重放
// 与 resume 状态一致（I3）。
func (s *Session) recoverTail() {
	turnOpen := false
	var chunkText strings.Builder
	pendingTools := []sessionPendingTool{}

	for _, env := range s.events {
		switch env.Type {
		case TypeUserMessage:
			turnOpen = true
		case TypeAssistantChunk:
			var d AssistantChunkData
			json.Unmarshal(env.Data, &d)
			if d.Kind == ChunkReasoning {
				continue // 思考增量不进投影：模型可见内容不含思考（I1/I5）
			}
			chunkText.WriteString(d.Delta)
		case TypeAssistantMessage:
			chunkText.Reset()
		case TypeToolCall:
			var d ToolCallData
			json.Unmarshal(env.Data, &d)
			pendingTools = append(pendingTools, sessionPendingTool{id: d.ID, name: d.Name})
		case TypeToolResult:
			var d ToolResultData
			json.Unmarshal(env.Data, &d)
			for i, p := range pendingTools {
				if p.id == d.ID {
					pendingTools = append(pendingTools[:i], pendingTools[i+1:]...)
					break
				}
			}
		case TypeTurnEnd:
			turnOpen = false
			chunkText.Reset()
			pendingTools = pendingTools[:0]
		}
	}

	if !turnOpen {
		return
	}
	for _, p := range pendingTools {
		s.Append(TypeToolResult, ToolResultData{
			ID:        p.id,
			Canonical: tool.Result{Err: "interrupted by crash"},
			Synthetic: true,
		})
	}
	if text := chunkText.String(); text != "" {
		s.Append(TypeAssistantMessage, AssistantMessageData{
			Text: text, Interrupted: true, Synthetic: true,
		})
	}
	s.Append(TypeTurnEnd, TurnEndData{Turn: s.turn, StopReason: "crash-recovered", Synthetic: true})
	s.turn++
}

type sessionPendingTool struct{ id, name string }

func splitLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		idx := 0
		for idx < len(data) && data[idx] != '\n' {
			idx++
		}
		line := data[:idx]
		if len(line) > 0 {
			out = append(out, line)
		}
		if idx >= len(data) {
			break
		}
		data = data[idx+1:]
	}
	return out
}

// Append 追加一个事件：分配 seq/ts，写盘并缓存。
func (s *Session) Append(evType string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.seq++
	env := Envelope{
		Seq:  s.seq,
		Ts:   time.Now().UTC().Format(time.RFC3339Nano),
		Type: evType,
		Data: raw,
	}
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		s.seq-- // 写失败回滚计数，保持 seq 与文件一致
		return err
	}
	s.events = append(s.events, env)
	return nil
}

func (s *Session) Path() string       { return s.path }
func (s *Session) Dir() string        { return filepath.Dir(s.path) }
func (s *Session) Header() Header     { return s.header }
func (s *Session) Turn() int          { return s.turn }
func (s *Session) Events() []Envelope { return s.events }

// Assets 返回会话的图片资产存储（<会话目录>/assets）。
func (s *Session) Assets() *Assets {
	if s.assets == nil {
		s.assets = NewAssets(s.Dir())
	}
	return s.assets
}

// EndTurn 记录 turn/end 并推进 turn 计数。
func (s *Session) EndTurn(stopReason string) error {
	if err := s.Append(TypeTurnEnd, TurnEndData{Turn: s.turn, StopReason: stopReason}); err != nil {
		return err
	}
	s.turn++
	return nil
}

// projector 从事件流增量投影模型历史；DeriveMessages 与 I1 重放共用，
// 保证「当时的请求」与「现在的投影」用同一套语义。
type projector struct {
	msgs    []taggedMessage
	summary string
	turn    int // 已闭合的 turn 数；进行中的 turn = turn+1
}

type taggedMessage struct {
	msg  provider.Message
	turn int // 消息所属 turn（1 起）
	seq  int
}

// 剪枝配方（第 6.6 节第 1 步）：旧 turn 的工具结果投影超过阈值时截为
// 头+尾。「旧」只依请求时刻已知的 turn 结构（当前与上一 turn 之外），
// 重放确定性由此保证。
const (
	pruneThreshold = 8192
	pruneHead      = 4096
	pruneTail      = 1024
)

func (p *projector) apply(env Envelope) {
	cur := p.turn + 1
	switch env.Type {
	case TypeTurnEnd:
		p.turn++
	case TypeCompactionHappened:
		// compaction 事件在日志中位于保留尾部之后：按 seq 过滤已应用
		// 的消息，保留 keptFrom 起的原文，遮蔽区间交给 summary。
		var cd CompactionData
		json.Unmarshal(env.Data, &cd)
		p.summary = cd.Summary
		kept := p.msgs[:0]
		for _, tm := range p.msgs {
			if tm.seq >= cd.KeptFrom {
				kept = append(kept, tm)
			}
		}
		p.msgs = kept
	case TypeAssistantChunk:
		var d AssistantChunkData
		json.Unmarshal(env.Data, &d)
		if d.Kind == ChunkReasoning {
			return // 思考增量只落盘不投影：发给模型的历史不含思考（I5）
		}
	case TypeUserMessage:
		var d UserMessageData
		json.Unmarshal(env.Data, &d)
		p.msgs = append(p.msgs, taggedMessage{msg: provider.Message{Role: "user", Content: provider.ContentFromText(d.Text)}, turn: cur, seq: env.Seq})
	case TypeAssistantMessage:
		var d AssistantMessageData
		json.Unmarshal(env.Data, &d)
		p.msgs = append(p.msgs, taggedMessage{
			msg:  provider.Message{Role: "assistant", Content: provider.ContentFromText(d.Text), ToolCalls: d.ToolCalls},
			turn: cur, seq: env.Seq,
		})
	case TypeToolResult:
		var d ToolResultData
		json.Unmarshal(env.Data, &d)
		p.msgs = append(p.msgs, taggedMessage{
			msg: provider.Message{
				Role: "tool", ToolCallID: d.ID,
				Content: provider.ContentFromText(tool.ForModel(d.Canonical, modelToolBudget)),
			},
			turn: cur, seq: env.Seq,
		})
	}
}

// messages 返回当前投影快照；对旧 turn 的超限工具结果应用剪枝。
func (p *projector) messages() []provider.Message {
	out := make([]provider.Message, 0, len(p.msgs))
	if p.summary != "" {
		out = append(out, provider.Message{
			Role:    "user",
			Content: provider.ContentFromText("<compacted-summary>\n" + p.summary + "\n</compacted-summary>"),
		})
	}
	for _, tm := range p.msgs {
		if tm.msg.Role == "tool" && tm.turn <= p.turn-1 {
			// 工具结果恒为单 text part（投影自 ForModel），直接按字符串剪。
			if s := provider.ContentText(tm.msg.Content); len(s) > pruneThreshold {
				s = s[:pruneHead] +
					fmt.Sprintf("\n...[pruned %d chars]...\n", len(s)-pruneHead-pruneTail) +
					s[len(s)-pruneTail:]
				tm.msg.Content = provider.ContentFromText(s)
			}
		}
		out = append(out, tm.msg)
	}
	return out
}

// DeriveMessages 从日志投影模型历史（resume/compaction/回放共用）。
// 工具结果按 ForModel 预算投影（I5：投影不改日志）。
func (s *Session) DeriveMessages() []provider.Message {
	p := &projector{}
	for _, env := range s.events {
		p.apply(env)
	}
	return p.messages()
}

// MessagesUpTo 返回 seq < limit 事件的投影（压缩摘要请求重建遮蔽区间用，
// 与 DeriveMessages 同一投影器语义）。
func (s *Session) MessagesUpTo(limit int) []provider.Message {
	p := &projector{}
	for _, env := range s.events {
		if env.Seq >= limit {
			break
		}
		p.apply(env)
	}
	return p.messages()
}

// ReplayRequestHashes 重放日志：在每个 request/header 处，用当时的投影
// 重建请求（含 header 记录的图片资产引用还原）并计算哈希，与留痕比对。
// 返回 (留痕哈希, 重建哈希) 序列——I1 的 golden 请求测试直接消费
// （全部成对相等即通过）。
func (s *Session) ReplayRequestHashes(system string, defs []provider.ToolDef) ([][2]string, error) {
	var out [][2]string
	p := &projector{}
	for _, env := range s.events {
		if env.Type == TypeRequestHeader {
			var d RequestHeaderData
			json.Unmarshal(env.Data, &d)
			hash, err := provider.PrefixHash(provider.Request{
				Model:    d.Model,
				System:   system,
				Messages: AttachImageParts(p.messages(), s.imageParts(d.Images)),
				Tools:    defs,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, [2]string{d.PrefixHash, hash})
		}
		p.apply(env)
	}
	return out, nil
}

// AttachImageParts 把图片 parts 追加到最后一条 user 消息的 Content 尾部。
// live 请求构造与日志重放共用的唯一放置规则：图片只进请求尾部、不进投影
// 历史（I2 前缀稳定与缓存命中的前提）。parts 为空或无 user 消息时原样返回。
func AttachImageParts(msgs []provider.Message, parts []provider.ContentPart) []provider.Message {
	if len(parts) == 0 {
		return msgs
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			msgs[i].Content = append(msgs[i].Content, parts...)
			return msgs
		}
	}
	return msgs
}

// imageParts 把资产引用解析为图片 parts；引用无法读取（资产被外部删除）
// 时跳过该图——对应请求的重放哈希将不一致，是既定降级语义。
func (s *Session) imageParts(refs []string) []provider.ContentPart {
	var parts []provider.ContentPart
	for _, ref := range refs {
		data, err := s.Assets().Data(ref)
		if err != nil {
			continue
		}
		if part, ok := provider.ImagePart(filepath.Ext(ref), data); ok {
			parts = append(parts, part)
		}
	}
	return parts
}

// PruneAssets 删除日志中不再被引用的资产文件（/rewind 截断后调用）。
func (s *Session) PruneAssets() {
	keep := map[string]bool{}
	for _, env := range s.events {
		switch env.Type {
		case TypeUserMessage:
			var d UserMessageData
			json.Unmarshal(env.Data, &d)
			for _, ref := range d.Images {
				keep[ref] = true
			}
		case TypeRequestHeader:
			var d RequestHeaderData
			json.Unmarshal(env.Data, &d)
			for _, ref := range d.Images {
				keep[ref] = true
			}
		}
	}
	s.Assets().Prune(keep)
}

// TruncateBeforeTurn 丢弃 turn 及其后的所有事件并重写日志（/rewind 用，
// I3：截断后日志仍是唯一真相）。
func (s *Session) TruncateBeforeTurn(turn int) error {
	keep := len(s.events)
	cur := 1
	for i, env := range s.events {
		if cur >= turn {
			keep = i
			break
		}
		if env.Type == TypeTurnEnd {
			cur++
		}
	}
	kept := s.events[:keep]
	var buf bytes.Buffer
	for _, env := range kept {
		line, err := json.Marshal(env)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := s.file.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(s.path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	s.file = f
	s.events = kept
	s.seq = 0
	for _, env := range kept {
		if env.Seq > s.seq {
			s.seq = env.Seq
		}
	}
	s.turn = turn
	return nil
}

// Transcript 返回会话转录（resume 时回放给 TUI 的投影，I3：与重放
// 同一来源）。工具结果用 ForTUI 摘要；chunk 不参与。
func (s *Session) Transcript() []string {
	var lines []string
	for _, env := range s.events {
		switch env.Type {
		case TypeUserMessage:
			var d UserMessageData
			json.Unmarshal(env.Data, &d)
			lines = append(lines, "> "+d.Text)
		case TypeAssistantMessage:
			var d AssistantMessageData
			json.Unmarshal(env.Data, &d)
			for _, tc := range d.ToolCalls {
				lines = append(lines, dimLine("-> "+tc.Function.Name))
			}
			if d.Text != "" {
				mark := ""
				if d.Interrupted {
					mark = "（中断）"
				}
				lines = append(lines, trimTrailing(d.Text)+mark)
			}
		case TypeToolResult:
			var d ToolResultData
			json.Unmarshal(env.Data, &d)
			lines = append(lines, dimLine("<- "+tool.ForTUI(d.Canonical)))
		case TypeCompactionHappened:
			lines = append(lines, dimLine("[compaction] 上下文已压缩"))
		}
	}
	return lines
}

func dimLine(s string) string { return "| " + s }

func trimTrailing(s string) string { return strings.TrimRight(s, "\n") }

// SessionInfo 是会话列表条目。
type SessionInfo struct {
	ID      string
	Created string
	Turns   int
	Path    string
}

// ListSessions 列出 cwd 对应的会话（按修改时间降序）。
func ListSessions(root, cwd string) ([]SessionInfo, error) {
	dir := filepath.Join(root, "sessions", NormalizeCwd(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type withMtime struct {
		info SessionInfo
		mt   int64
	}
	var out []withMtime
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "session.jsonl")
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		s, err := Open(path)
		if err != nil {
			continue
		}
		out = append(out, withMtime{
			info: SessionInfo{ID: s.Header().ID, Created: s.Header().Created, Turns: s.Turn() - 1, Path: path},
			mt:   fi.ModTime().UnixMilli(),
		})
		s.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mt > out[j].mt })
	infos := make([]SessionInfo, len(out))
	for i, w := range out {
		infos[i] = w.info
	}
	return infos, nil
}

// Close 关闭日志文件。
func (s *Session) Close() error { return s.file.Close() }
