package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/skill"
)

// popupKind 弹窗状态集中管理（第 6.7 节：避开 Reasonix 的 nil 链互斥）。
type popupKind int

const (
	popupNone popupKind = iota
	popupModelPicker
	popupSkillPicker
)

const pickerMaxShown = 8

// pickerWindow 取选择器窗口的可见项（至多 pickerMaxShown 项）。
func pickerWindow[T any](items []T, offset int) []T {
	start := min(offset, len(items))
	if start < 0 {
		start = 0
	}
	end := min(start+pickerMaxShown, len(items))
	return items[start:end]
}

// openModelPicker 打开 Ctrl+P 选择器：输入框转为过滤器，原输入 Esc 时还原。
func (m Model) openModelPicker() (tea.Model, tea.Cmd) {
	m.popup = popupModelPicker
	m.pickerSel = 0
	m.pickerOffset = 0
	m.inputBeforePopup = m.editor.Text()
	m.editor.Clear()
	return m, nil
}

// openSkillPicker 打开 skill 选择器：列表展示名称 + 描述，Enter 回填
// 输入框（与模型选择器的差异：回填而非提交——skill 主用法是正文 + 任务
// 拼接）。
func (m *Model) openSkillPicker() {
	m.popup = popupSkillPicker
	m.pickerSel = 0
	m.pickerOffset = 0
	m.inputBeforePopup = m.editor.String()
	m.editor.Clear()
}

// handlePopupKey 处理弹窗内的键盘输入。
func (m Model) handlePopupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEscape:
		m.popup = popupNone
		m.editor.Clear()
		m.editor.Insert(m.inputBeforePopup)
		return m, nil
	case msg.Code == tea.KeyUp:
		if m.pickerSel > 0 {
			m.pickerSel--
			m.clampPickerOffset()
		}
		return m, nil
	case msg.Code == tea.KeyDown:
		n := len(m.filteredModels())
		if m.popup == popupSkillPicker {
			n = len(m.filteredSkills())
		}
		if m.pickerSel < n-1 {
			m.pickerSel++
			m.clampPickerOffset()
		}
		return m, nil
	case msg.Code == tea.KeyEnter:
		if m.popup == popupSkillPicker {
			skills := m.filteredSkills()
			if len(skills) == 0 {
				return m, nil
			}
			chosen := skills[min(m.pickerSel, len(skills)-1)]
			m.popup = popupNone
			m.editor.Clear()
			m.editor.Insert("/skill " + chosen.Name + " ") // 回填待补任务描述，不直接发送
			return m, nil
		}
		models := m.filteredModels()
		if len(models) == 0 {
			return m, nil
		}
		chosen := models[min(m.pickerSel, len(models)-1)]
		m.popup = popupNone
		m.editor.Clear()
		lines := m.deps.Slash("/model " + chosen)
		return m, m.printLines(lines)
	case msg.Code == tea.KeyBackspace:
		m.editor.Backspace()
		m.pickerSel = 0
		m.pickerOffset = 0
		return m, nil
	default:
		if s := msg.Text; s != "" {
			m.editor.Insert(s)
			m.pickerSel = 0
			m.pickerOffset = 0
		}
		return m, nil
	}
}

// clampPickerOffset 让可视窗口跟随选中滚动：选中越过窗口末端时窗口下移，
// 反之窗口顶部回退，保证选中始终在可见范围内（pickerMaxShown 项窗口）。
func (m *Model) clampPickerOffset() {
	n := len(m.filteredModels())
	if m.popup == popupSkillPicker {
		n = len(m.filteredSkills())
	}
	maxShown := pickerMaxShown
	if n <= maxShown {
		m.pickerOffset = 0
		return
	}
	maxOffset := n - maxShown
	switch {
	case m.pickerSel > m.pickerOffset+maxShown-1:
		m.pickerOffset = m.pickerSel - maxShown + 1
	case m.pickerSel < m.pickerOffset:
		m.pickerOffset = m.pickerSel
	}
	if m.pickerOffset > maxOffset {
		m.pickerOffset = maxOffset
	}
	if m.pickerOffset < 0 {
		m.pickerOffset = 0
	}
}

// filteredModels 返回过滤后的模型列表（当前输入作为过滤器）。
func (m Model) filteredModels() []string {
	if m.deps.Models == nil {
		return nil
	}
	filter := m.editor.String()
	var out []string
	for _, name := range m.deps.Models() {
		if skill.Match(name, filter) {
			out = append(out, name)
		}
	}
	return out
}

// filteredSkills 返回过滤后的技能列表（当前输入作为过滤器）。
func (m Model) filteredSkills() []skill.Skill {
	return skill.Filter(m.currentSkills(), m.editor.String())
}

// currentSkills 返回当前可用的技能列表。
func (m Model) currentSkills() []skill.Skill {
	if m.deps.Skills == nil {
		return nil
	}
	return m.deps.Skills()
}

// modelPickerLines 模型选择器列表（内嵌于可变区，自带模糊过滤，不依赖 fzf）。
func (m Model) modelPickerLines(width int) []string {
	lines := []string{dim(" 选择模型（输入过滤 | Enter 确认 | Esc 取消）")}
	models := m.filteredModels()
	shown := pickerWindow(models, m.pickerOffset)
	for i, name := range shown {
		mark := " "
		if name == m.modelName {
			mark = "*"
		}
		entry := " " + mark + " " + name
		if i == m.pickerSel-m.pickerOffset {
			entry = ansiCyan + "> " + strings.TrimLeft(entry, " *") + ansiReset
		}
		lines = append(lines, clipLine(entry, width))
	}
	if len(models) > pickerMaxShown {
		lines = append(lines, dim(fmt.Sprintf("   ... 共 %d 个", len(models))))
	}
	if len(models) == 0 {
		lines = append(lines, dim("   （无匹配模型）"))
	}
	return lines
}

// skillPickerLines skill 选择器列表：名称 + 一行描述帮助辨认（正文按需
// 展开，列表是唯一的发现入口）。
func (m Model) skillPickerLines(width int) []string {
	lines := []string{dim(" 选择 skill（输入过滤 | Enter 回填 | Esc 取消）")}
	skills := m.filteredSkills()
	shown := pickerWindow(skills, m.pickerOffset)
	for i, s := range shown {
		entry := "   " + s.Name
		if s.Description != "" {
			entry += "  " + s.Description
		}
		if i == m.pickerSel-m.pickerOffset {
			entry = ansiCyan + "> " + strings.TrimLeft(entry, " ") + ansiReset
		}
		lines = append(lines, clipLine(entry, width))
	}
	if len(skills) > pickerMaxShown {
		lines = append(lines, dim(fmt.Sprintf("   ... 共 %d 个", len(skills))))
	}
	if len(skills) == 0 {
		lines = append(lines, dim("   （无匹配 skill）"))
	}
	return lines
}
