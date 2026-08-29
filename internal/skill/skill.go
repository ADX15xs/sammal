// Package skill 管理 skill 的发现与展开（/skill 命令的支撑层）。
// skill 是 prompt 简化器：<目录>/SKILL.md（name/description frontmatter），
// 正文由 TUI 展开进 user turn 尾部——不进系统提示词、不进工具目录，
// I2 的前缀纪律与此无涉（SPEC 6.10）。
package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill 是一个已解析的 skill。Scan 一次性读入全部正文：个人规模的
// skill 集合（几十个、KB 级）内存可忽略，换取展开时零 IO。
type Skill struct {
	Name        string // frontmatter name，缺省 = 目录名
	Description string // frontmatter description（列表/选择器展示）
	Body        string // frontmatter 之后的正文
}

// Scan 扫描两级目录：configDir/skills（全局）与 cwd/.agents/skills（项目）。
// 目录不存在或不可读视为空；同名 skill 项目级覆盖全局；返回按名称排序。
func Scan(configDir, cwd string) []Skill {
	found := map[string]Skill{}
	for _, dir := range []string{
		filepath.Join(configDir, "skills"),
		filepath.Join(cwd, ".agents", "skills"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
			if err != nil {
				continue // 无 SKILL.md 的目录不构成 skill
			}
			s := parse(e.Name(), data)
			found[s.Name] = s
		}
	}
	out := make([]Skill, 0, len(found))
	for _, s := range found {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parse 解析 SKILL.md：可选 `---` 包裹的 frontmatter（逐行 key: value，
// 只认 name/description），其余为正文。frontmatter 缺失或缺 name 时
// 用目录名。
func parse(dirName string, data []byte) Skill {
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	s := Skill{Name: dirName, Body: strings.TrimSpace(strings.Join(lines, "\n"))}
	if strings.TrimSpace(lines[0]) != "---" {
		return s
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return s
	}
	for _, ln := range lines[1:end] {
		key, val, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "name":
			if val != "" {
				s.Name = val
			}
		case "description":
			s.Description = val
		}
	}
	s.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return s
}

// Match 子序列模糊匹配：大小写不敏感，sub 的字符按序出现在 s 中即命中；
// 空 sub 命中全部。
func Match(s, sub string) bool {
	s, sub = strings.ToLower(s), strings.ToLower(sub)
	if sub == "" {
		return true
	}
	i := 0
	for j := 0; j < len(s) && i < len(sub); j++ {
		if s[j] == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

// Resolve 按参数解析 skill：精确同名优先（哪怕还模糊命中了别人），
// 其余子序列模糊匹配。返回全部命中：0 = 无匹配，1 = 唯一，>1 = 歧义。
func Resolve(arg string, skills []Skill) []Skill {
	for _, s := range skills {
		if s.Name == arg {
			return []Skill{s}
		}
	}
	var out []Skill
	for _, s := range skills {
		if Match(s.Name, arg) {
			out = append(out, s)
		}
	}
	return out
}

// Filter 返回名称模糊命中 filter 的子集（选择器实时过滤用）。
func Filter(skills []Skill, filter string) []Skill {
	var out []Skill
	for _, s := range skills {
		if Match(s.Name, filter) {
			out = append(out, s)
		}
	}
	return out
}

// Expand 把 skill 正文与任务描述拼成一条 user 消息（骑 user turn 尾部，
// I2 明文豁免的动态注入点）。正文以 <skill> 包裹，与用户原话明确分区。
func Expand(s Skill, task string) string {
	var b strings.Builder
	b.WriteString("<skill name=\"")
	b.WriteString(s.Name)
	b.WriteString("\">\n")
	b.WriteString(s.Body)
	b.WriteString("\n</skill>")
	if task != "" {
		b.WriteString("\n\n")
		b.WriteString(task)
	}
	return b.String()
}
