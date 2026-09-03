package agent

import (
	"strings"
	"testing"
)

// 系统提示词不再含日期字段：任意时刻构建字节相同（I2 强不变量恢复）。
// 「今天」由 UserMessageData.Date 随消息落盘、session 投影时注入消息头部
// （session.withDatePrefix），agent 侧提交接线由 m2_test 的 resume 路径覆盖。
func TestSystemPromptIsDateFree(t *testing.T) {
	sys := BuildSystemPrompt(PromptFacts{Cwd: "/w", OS: "linux", Shell: "bash"})
	if strings.Contains(sys, "Today:") {
		t.Errorf("系统提示词不应含日期: %q", sys)
	}
}
