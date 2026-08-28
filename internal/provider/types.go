package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// ContentPart 是多模态消息内容的一个片段。
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 表示图片 URL 或 base64 data URI。
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// Message 是模型历史的内存形态；序列化字段顺序即 JSON 键序（I2 依赖确定性）。
type Message struct {
	Role       string        `json:"role"`
	Content    []ContentPart `json:"content"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// ContentFromText 把纯文本包装为单 text part 的 content 数组。
func ContentFromText(s string) []ContentPart {
	return []ContentPart{{Type: "text", Text: s}}
}

// ContentText 把 content 数组的 text part 拼接为单一字符串。
func ContentText(parts []ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ImagePart 把图片字节构造为 image_url content part（data URI 形态），
// 扩展名（大小写不敏感）不支持时返回 false。agent 组装与会话重放共用
// 的唯一构造点：同一字节在两侧必须产出逐字节一致的 part（重放哈希依赖）。
func ImagePart(ext string, data []byte) (ContentPart, bool) {
	mime, ok := imageMIME[strings.ToLower(ext)]
	if !ok {
		return ContentPart{}, false
	}
	return ContentPart{
		Type:     "image_url",
		ImageURL: &ImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)},
	}, true
}

var imageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
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
	Content    []ContentPart  `json:"content"`
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
	msgs = append(msgs, wireMessage{Role: "system", Content: ContentFromText(r.System)})
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
