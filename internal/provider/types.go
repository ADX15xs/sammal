package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Message 是模型历史的内存形态；序列化字段顺序即 JSON 键序（I2 依赖确定性）。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall 是聚合完成的工具调用（assistant 消息携带，或来自日志重放）。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolTypeFunction 是 OpenAI 协议中唯一的工具调用类型。
const ToolTypeFunction = "function"

// ToolDef 是工具的静态 schema（I2：会话内字节级不变）。
type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request 一次流式补全请求；System 与 Tools 构成静态前缀，Messages 随轮追加。
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolDef
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireRequest struct {
	Model         string            `json:"model"`
	Messages      []wireMessage     `json:"messages"`
	Tools         []ToolDef         `json:"tools,omitempty"`
	Stream        bool              `json:"stream"`
	StreamOptions wireStreamOptions `json:"stream_options"`
}

func toWireMessages(r Request) []wireMessage {
	msgs := make([]wireMessage, 0, len(r.Messages)+1)
	msgs = append(msgs, wireMessage{Role: "system", Content: r.System})
	for _, m := range r.Messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: wireFunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		msgs = append(msgs, wm)
	}
	return msgs
}

// MarshalRequest 返回请求体的确定性序列化。I2 的前缀哈希与 golden 测试都基于它。
func MarshalRequest(r Request) ([]byte, error) {
	return json.Marshal(wireRequest{
		Model:         r.Model,
		Messages:      toWireMessages(r),
		Tools:         r.Tools,
		Stream:        true,
		StreamOptions: wireStreamOptions{IncludeUsage: true},
	})
}

// PrefixHash 是请求体的 SHA-256 十六进制摘要，写入 request/header 事件（I1）。
func PrefixHash(r Request) (string, error) {
	body, err := MarshalRequest(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
