package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/skill"
)

var testSkills = []skill.Skill{
	{Name: "alpha", Description: "说明A", Body: "alpha 正文"},
	{Name: "alpha-two", Body: "alpha-two 正文"},
	{Name: "beta", Body: "beta 正文"},
}

func skillTestDeps(sent *[]string) Deps {
	return Deps{
		ModelName: "test-model",
		Send: func(text string, images []string) {
			*sent = append(*sent, text)
		},
		Skills: func() []skill.Skill { return testSkills },
	}
}

func pressEnter(m Model) Model {
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	return out.(Model)
}

// 唯一命中（精确同名短路模糊）：正文 + 任务展开为一条 user 消息提交，
// 回显与历史仍记录原始命令。
func TestSlashSkillSendsExpanded(t *testing.T) {
	var sent []string
	m := New(skillTestDeps(&sent))
	m.input.Insert("/skill alpha 重构这个函数")
	m = pressEnter(m)

	if len(sent) != 1 {
		t.Fatalf("sent = %v, want 1 条", sent)
	}
	want := "<skill name=\"alpha\">\nalpha 正文\n</skill>\n\n重构这个函数"
	if sent[0] != want {
		t.Errorf("展开文本:\n got: %q\nwant: %q", sent[0], want)
	}
	if !m.busy {
		t.Error("展开后应进入生成中状态")
	}
	if got := m.history[len(m.history)-1]; got != "/skill alpha 重构这个函数" {
		t.Errorf("历史应记录原命令，got %q", got)
	}
}

// 无任务描述：仅提交 skill 正文。
func TestSlashSkillSendsBodyOnly(t *testing.T) {
	var sent []string
	m := New(skillTestDeps(&sent))
	m.input.Insert("/skill beta")
	m = pressEnter(m)
	if len(sent) != 1 || sent[0] != "<skill name=\"beta\">\nbeta 正文\n</skill>" {
		t.Errorf("sent = %v", sent)
	}
}

// 歧义（alph 同时模糊命中 alpha 与 alpha-two，且无精确同名）：输出候选
// 行，不发送。
func TestSlashSkillAmbiguous(t *testing.T) {
	var sent []string
	m := New(skillTestDeps(&sent))
	m.input.Insert("/skill alph extra")
	m = pressEnter(m)

	if len(sent) != 0 {
		t.Errorf("歧义时不应发送，sent = %v", sent)
	}
	if m.busy {
		t.Error("歧义时不应进入生成中状态")
	}
	if m.InputText() != "" {
		t.Errorf("输入应已清空，got %q", m.InputText())
	}
}

// 无命中与零 skill：提示行，不发送。
func TestSlashSkillNoMatch(t *testing.T) {
	var sent []string
	m := New(skillTestDeps(&sent))
	cmd, _, out := m.slashSkill("/skill zzz")
	if cmd != skillShow || len(out) < 2 || !strings.Contains(out[0], "没有匹配") {
		t.Errorf("cmd = %v out = %v", cmd, out)
	}

	empty := New(Deps{ModelName: "x", Send: func(string, []string) {}})
	cmd, _, out = empty.slashSkill("/skill x")
	if cmd != skillShow || len(out) != 1 || !strings.Contains(out[0], "没有找到任何 skill") {
		t.Errorf("零 skill 提示: cmd = %v out = %v", cmd, out)
	}
}

// 无参：打开选择器，输入消费完毕。
func TestSlashSkillOpensPicker(t *testing.T) {
	var sent []string
	m := New(skillTestDeps(&sent))
	m.input.Insert("/skill")
	m = pressEnter(m)

	if m.popup != popupSkillPicker {
		t.Fatalf("popup = %v, want popupSkillPicker", m.popup)
	}
	if m.InputText() != "" {
		t.Errorf("选择器应独占输入框，got %q", m.InputText())
	}
	if len(sent) != 0 {
		t.Errorf("打开选择器不应发送，sent = %v", sent)
	}
}

// 选择器 Enter：回填 "/skill <name> " 待补任务，不发送；Esc 还原空输入。
func TestSkillPickerEnterFillsInput(t *testing.T) {
	var sent []string
	m := New(skillTestDeps(&sent))
	m.popup = popupSkillPicker
	m.inputBeforePopup = ""
	m.input.Insert("alp") // 模糊过滤同时命中 alpha 与 alpha-two

	out, _ := m.handlePopupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = out.(Model)

	if m.popup != popupNone {
		t.Errorf("popup = %v, want 关闭", m.popup)
	}
	if got := m.InputText(); got != "/skill alpha " {
		t.Errorf("回填 = %q, want %q", got, "/skill alpha ")
	}
	if len(sent) != 0 {
		t.Errorf("回填不发送，sent = %v", sent)
	}

	// 回填后直接 Enter：无任务，提交 skill 正文。
	m = pressEnter(m)
	if len(sent) != 1 || !strings.Contains(sent[0], "alpha 正文") {
		t.Errorf("sent = %v", sent)
	}
}

// 非 /skill 命令不受影响：交给常规命令分发。
func TestSlashSkillPassThrough(t *testing.T) {
	var slashCalls []string
	deps := Deps{
		ModelName: "x",
		Send:      func(string, []string) {},
		Skills:    func() []skill.Skill { return testSkills },
		Slash: func(text string) []string {
			slashCalls = append(slashCalls, text)
			return []string{"ok"}
		},
	}
	m := New(deps)
	if cmd, _, _ := m.slashSkill("/skillfoo"); cmd != skillNone {
		t.Errorf("/skillfoo 应透传，cmd = %v", cmd)
	}
	if cmd, _, _ := m.slashSkill("/model x"); cmd != skillNone {
		t.Errorf("/model 应透传，cmd = %v", cmd)
	}

	m.input.Insert("/help")
	m = pressEnter(m)
	if len(slashCalls) != 1 || slashCalls[0] != "/help" {
		t.Errorf("slash 调用 = %v", slashCalls)
	}
}

// Down 导航以当前弹窗的列表长度为界：模型列表更短时仍可走到 skill 末项，
// 更长时不越过（回归：上限曾误用 filteredModels）。
func TestSkillPickerNavBoundsFollowPopup(t *testing.T) {
	var sent []string
	deps := skillTestDeps(&sent)
	deps.Models = func() []string { return []string{"only-model"} }
	m := New(deps)
	m.popup = popupSkillPicker
	m.inputBeforePopup = ""

	for i := 0; i < 5; i++ {
		out, _ := m.handlePopupKey(tea.KeyPressMsg{Code: tea.KeyDown})
		m = out.(Model)
	}
	if m.pickerSel != 2 {
		t.Errorf("pickerSel = %d, want 2（3 个 skill 的末项）", m.pickerSel)
	}
	for i := 0; i < 5; i++ {
		out, _ := m.handlePopupKey(tea.KeyPressMsg{Code: tea.KeyUp})
		m = out.(Model)
	}
	if m.pickerSel != 0 {
		t.Errorf("pickerSel = %d, want 0", m.pickerSel)
	}
}
