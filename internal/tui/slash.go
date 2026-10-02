package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sammal/internal/skill"
)

// skillCmd 是 /skill 命令的处理结果类别。
type skillCmd int

const (
	skillNone       skillCmd = iota // 不是 /skill 命令，交回常规命令分发
	skillSend                       // expanded 已就绪，走普通发送路径
	skillShow                       // out 为候选/提示行，不发送
	skillPickerOpen                 // 选择器已打开，本次输入消费完毕
)

// slashSkill 处理 /skill（TUI 专属：展开发生在提交之前，core 无感知）。
// `<name>` 精确同名优先、其余子序列模糊匹配；唯一命中时把「skill 正文 +
// 任务描述」拼成一条 user 消息（骑 user turn 尾部，SPEC 6.10）；无参打开
// 选择器；无命中/歧义输出候选行。
func (m *Model) slashSkill(input string) (skillCmd, string, []string) {
	if input != "/skill" && !strings.HasPrefix(input, "/skill ") {
		return skillNone, "", nil
	}
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/skill"))
	if arg == "" {
		m.openSkillPicker()
		return skillPickerOpen, "", nil
	}
	name, task, _ := strings.Cut(arg, " ")
	name, task = strings.TrimSpace(name), strings.TrimSpace(task)
	skills := m.currentSkills()
	matches := skill.Resolve(name, skills)
	switch {
	case len(matches) == 1:
		return skillSend, skill.Expand(matches[0], task), nil
	case len(matches) == 0 && len(skills) == 0:
		return skillShow, "", []string{"没有找到任何 skill（全局 skills 目录或 <cwd>/.agents/skills）"}
	case len(matches) == 0:
		out := append([]string{fmt.Sprintf("没有匹配 %q 的 skill，可用：", name)}, skillNameLines(skills)...)
		return skillShow, "", out
	default:
		out := append([]string{fmt.Sprintf("%q 匹配到 %d 个 skill，请用全名：", name, len(matches))}, skillNameLines(matches)...)
		return skillShow, "", out
	}
}

// skillNameLines 候选名单行（两空格缩进），用于无命中/歧义的输出行。
func skillNameLines(skills []skill.Skill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, "  "+s.Name)
	}
	return out
}

// handleSlash 处理不需要 agent 参与的 TUI 专属斜杠命令，返回 (输出行, 已处理)。
// /attach 是典型例子：它操作 TUI 的 pendingImages，无需请求模型。
// （/skill 亦为 TUI 专属，但在命令分发改路之前拦截展开，见 slashSkill。）
func (m *Model) handleSlash(input string) ([]string, bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil, false
	}
	switch fields[0] {
	case "/attach":
		return m.slashAttach(fields), true
	}
	return nil, false
}

// slashAttach 处理 /attach 命令：
// - /attach <path...>  注册图片路径（校验扩展名 + 文件存在）
// - /attach（无参数）  列出当前 pending 图片
// - /attach -clear     清空 pending 图片
func (m *Model) slashAttach(fields []string) []string {
	if len(fields) == 1 {
		return m.listPendingImages()
	}
	switch fields[1] {
	case "-clear":
		m.pendingImages = nil
		return []string{"已清空所有待发送图片"}
	default:
		var ok, bad int
		for _, p := range fields[1:] {
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" && ext != ".webp" {
				bad++
				continue
			}
			if _, err := os.Stat(p); err != nil {
				bad++
				continue
			}
			m.pendingImages = append(m.pendingImages, p)
			ok++
		}
		lines := []string{}
		if ok > 0 {
			lines = append(lines, fmt.Sprintf("已添加 %d 张图片", ok))
		}
		if bad > 0 {
			lines = append(lines, fmt.Sprintf("%d 个路径无效（扩展名不支持或文件不存在）", bad))
		}
		return lines
	}
}

func (m *Model) listPendingImages() []string {
	if len(m.pendingImages) == 0 {
		return []string{"暂无待发送图片"}
	}
	lines := []string{"待发送图片："}
	for _, p := range m.pendingImages {
		if info, err := os.Stat(p); err == nil {
			lines = append(lines, fmt.Sprintf("  %s（%s）", filepath.Base(p), humanBytes(info.Size())))
		} else {
			lines = append(lines, "  "+filepath.Base(p))
		}
	}
	return lines
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
	}
}
