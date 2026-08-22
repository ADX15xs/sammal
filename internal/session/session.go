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
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Model   string `json:"model"`
	Created string `json:"created"`
	OS      string `json:"os"`
	Shell   string `json:"shell"`
}

type Envelope struct {
	Seq  int             `json:"seq"`
	Ts   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type UserMessageData struct {
	Text string `json:"text"`
}

type AssistantChunkData struct {
	Delta string `json:"delta"`
}

type AssistantMessageData struct {
	Text        string              `json:"text"`
	ToolCalls   []provider.ToolCall `json:"toolCalls,omitempty"`
	Interrupted bool                `json:"interrupted"`
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
// 必要输入（会话中途切模型后请求体随之变化）。
type RequestHeaderData struct {
	PrefixHash   string `json:"prefixHash"`
	MessageCount int    `json:"messageCount"`
	Model        string `json:"model"`
}

type CompactionData struct {
	Summary      string `json:"summary"`
	SummaryRange [2]int `json:"summaryRange"` // 被遮蔽区间的事件 seq [first, last]
	KeptFrom     int    `json:"keptFrom"`     // 此 seq 起（含）的消息保留原文
}

type TurnEndData struct {
	Turn       int    `json:"turn"`
	StopReason string `json:"stopReason"`
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

// Open 打开既有会话并载入事件（崩溃恢复语义见 Recover）。
func Open(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Session{path: path}
	for _, line := range splitLines(data) {
		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			break // 尾部不完整行：丢弃（I3 崩溃恢复的「有效尾部」）
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
	return s, nil
}

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
	msgs     []provider.Message
	summary  string
	keptFrom int
}

func (p *projector) apply(env Envelope) {
	switch env.Type {
	case TypeCompactionHappened:
		var cd CompactionData
		json.Unmarshal(env.Data, &cd)
		p.summary = cd.Summary
		p.keptFrom = cd.KeptFrom
		p.msgs = nil
	case TypeUserMessage:
		if env.Seq < p.keptFrom {
			return
		}
		var d UserMessageData
		json.Unmarshal(env.Data, &d)
		p.msgs = append(p.msgs, provider.Message{Role: "user", Content: d.Text})
	case TypeAssistantMessage:
		if env.Seq < p.keptFrom {
			return
		}
		var d AssistantMessageData
		json.Unmarshal(env.Data, &d)
		p.msgs = append(p.msgs, provider.Message{Role: "assistant", Content: d.Text, ToolCalls: d.ToolCalls})
	case TypeToolResult:
		if env.Seq < p.keptFrom {
			return
		}
		var d ToolResultData
		json.Unmarshal(env.Data, &d)
		p.msgs = append(p.msgs, provider.Message{Role: "tool", ToolCallID: d.ID, Content: tool.ForModel(d.Canonical, modelToolBudget)})
	}
}

func (p *projector) messages() []provider.Message {
	if p.summary == "" {
		return p.msgs
	}
	return append([]provider.Message{{
		Role:    "user",
		Content: "<compacted-summary>\n" + p.summary + "\n</compacted-summary>",
	}}, p.msgs...)
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

// ReplayRequestHashes 重放日志：在每个 request/header 处，用当时的投影
// 重建请求并计算哈希，与留痕比对。返回 (留痕哈希, 重建哈希) 序列——
// I1 的 golden 请求测试直接消费（全部成对相等即通过）。
func (s *Session) ReplayRequestHashes(system string, defs []provider.ToolDef) ([][2]string, error) {
	var out [][2]string
	p := &projector{}
	for _, env := range s.events {
		if env.Type == TypeRequestHeader {
			var d RequestHeaderData
			json.Unmarshal(env.Data, &d)
			hash, err := provider.PrefixHash(provider.Request{
				Model: d.Model, System: system, Messages: p.messages(), Tools: defs,
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

// Close 关闭日志文件。
func (s *Session) Close() error { return s.file.Close() }
