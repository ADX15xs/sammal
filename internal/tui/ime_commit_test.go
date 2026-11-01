package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// driveInput 把若干段 VT 字节（每段模拟控制台输入的一次批量读取）经真实
// EventDecoder 解码为按键消息并依次喂给 Model。解码器与运行时
// （ultraviolet TerminalReader）共用同一个，不在此复刻第二份字节→事件映射。
func driveInput(t *testing.T, m Model, chunks []string) (Model, tea.Cmd) {
	t.Helper()
	dec := &uv.EventDecoder{}
	var lastCmd tea.Cmd
	for _, chunk := range chunks {
		buf := []byte(chunk)
		for len(buf) > 0 {
			n, ev := dec.Decode(buf)
			if n <= 0 {
				t.Fatalf("解码停滞：chunk %q 剩余 %d 字节", chunk, len(buf))
			}
			if ke, ok := ev.(uv.KeyPressEvent); ok {
				next, cmd := m.Update(tea.KeyPressMsg(ke))
				m = next.(Model)
				if cmd != nil {
					lastCmd = cmd
				}
			}
			buf = buf[n:]
		}
	}
	return m, lastCmd
}

// TestIMECommitDeliveryShapes 锁定 IME 中文上屏的各到达形态经输入链路
// （VT 字节 → 解码 → handleKey → editor）后缓冲与渲染的正确性：
// 行中插入不得覆盖、丢失或错位光标后的已有文字。
func TestIMECommitDeliveryShapes(t *testing.T) {
	const width = 80
	tests := []struct {
		name     string
		prefill  string
		chunks   []string
		wantText string
		wantLine string
		wantCol  int // 终端列（含前缀），Render 返回的 cursorCol
	}{
		{
			name:     "逐字上屏_每字一批",
			prefill:  "abcd",
			chunks:   []string{"\x1b[D", "\x1b[D", "你", "好"},
			wantText: "ab你好cd",
			wantLine: "> ab你好cd",
			wantCol:  8,
		},
		{
			name:     "整串上屏_单批多字",
			prefill:  "abcd",
			chunks:   []string{"\x1b[D", "\x1b[D", "你好"},
			wantText: "ab你好cd",
			wantLine: "> ab你好cd",
			wantCol:  8,
		},
		{
			name:     "编辑与上屏混在同一批量读",
			prefill:  "",
			chunks:   []string{"abcd\x1b[D\x1b[D你好"},
			wantText: "ab你好cd",
			wantLine: "> ab你好cd",
			wantCol:  8,
		},
		{
			name:     "两次插入累积",
			prefill:  "abcd",
			chunks:   []string{"\x1b[D", "\x1b[D", "你好", "\x1b[D", "\x1b[D", "再见"},
			wantText: "ab再见你好cd",
			wantLine: "> ab再见你好cd",
			wantCol:  8,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(Deps{ModelName: "t"})
			if tt.prefill != "" {
				m.editor.Insert(tt.prefill)
			}
			m, lastCmd := driveInput(t, m, tt.chunks)

			// 每个形状都以文本上屏收尾：必须返回重绘 Cmd（IME 显示失步对策）。
			if lastCmd == nil {
				t.Fatal("文本键后未返回重绘 Cmd")
			}
			if got := m.editor.Text(); got != tt.wantText {
				t.Fatalf("editor.Text() = %q, 期望 %q", got, tt.wantText)
			}
			lines, idx, col := m.editor.Render("> ", width, 4)
			if lines[idx] != tt.wantLine {
				t.Fatalf("渲染行 = %q, 期望 %q", lines[idx], tt.wantLine)
			}
			if col != tt.wantCol {
				t.Fatalf("光标列 = %d, 期望 %d", col, tt.wantCol)
			}
		})
	}
}
