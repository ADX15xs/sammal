package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	cfg := t.TempDir()
	proj := t.TempDir()
	global := filepath.Join(cfg, "skills")
	project := filepath.Join(proj, ".agents", "skills")
	writeSkill(t, global, "alpha", "---\nname: alpha\ndescription: 全局说明\n---\n正文A")
	writeSkill(t, global, "beta", "无 frontmatter 正文")
	writeSkill(t, project, "alpha", "---\ndescription: 项目覆盖\n---\n项目正文")
	// 不构成 skill 的条目：无 SKILL.md 的目录、文件、隐藏目录。
	if err := os.MkdirAll(filepath.Join(project, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, project, ".hidden", "---\nname: hidden\n---\nx")
	if err := os.WriteFile(filepath.Join(project, "loose.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := Scan(cfg, proj)
	if len(skills) != 2 {
		t.Fatalf("skills = %+v, want [alpha beta]", skills)
	}
	if skills[0].Name != "alpha" || skills[0].Description != "项目覆盖" || skills[0].Body != "项目正文" {
		t.Errorf("alpha = %+v，项目级应覆盖全局", skills[0])
	}
	if skills[1].Name != "beta" || skills[1].Description != "" || skills[1].Body != "无 frontmatter 正文" {
		t.Errorf("beta = %+v，无 frontmatter 时用目录名 + 全文", skills[1])
	}
}

func TestScanMissingDirs(t *testing.T) {
	if skills := Scan(t.TempDir(), t.TempDir()); len(skills) != 0 {
		t.Errorf("缺目录应为空，got %+v", skills)
	}
}

func TestParseCRLF(t *testing.T) {
	s := parse("demo", []byte("---\r\nname: demo\r\ndescription: 说明\r\n---\r\n正文行\r\n"))
	if s.Name != "demo" || s.Description != "说明" || s.Body != "正文行" {
		t.Errorf("CRLF 解析 = %+v", s)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	s := parse("demo", []byte("--- 不是分隔符的开头\n正文"))
	if s.Name != "demo" || s.Body != "--- 不是分隔符的开头\n正文" {
		t.Errorf("伪 frontmatter 应按正文处理，got %+v", s)
	}
}

func TestMatch(t *testing.T) {
	if !Match("qwen3-local", "q3l") {
		t.Error("子序列应命中")
	}
	if Match("qwen3-local", "localx") {
		t.Error("超出子序列不应命中")
	}
	if !Match("DeepSeek", "deep") {
		t.Error("大小写不敏感")
	}
	if !Match("anything", "") {
		t.Error("空过滤命中全部")
	}
}

func TestResolve(t *testing.T) {
	skills := []Skill{
		{Name: "alpha"},
		{Name: "alpha-one"},
		{Name: "beta"},
	}
	if got := Resolve("alpha-one", skills); len(got) != 1 || got[0].Name != "alpha-one" {
		t.Errorf("精确同名应短路模糊命中（alpha 也是其子序列），got %+v", got)
	}
	if got := Resolve("alp", skills); len(got) != 2 {
		t.Errorf("alp 应模糊命中 alpha 与 alpha-one，got %+v", got)
	}
	if got := Resolve("beta", skills); len(got) != 1 || got[0].Name != "beta" {
		t.Errorf("got %+v", got)
	}
	if got := Resolve("zzz", skills); len(got) != 0 {
		t.Errorf("无命中应返回空，got %+v", got)
	}
}

func TestFilter(t *testing.T) {
	skills := []Skill{{Name: "alpha"}, {Name: "beta"}}
	got := Filter(skills, "alp")
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Errorf("got %+v", got)
	}
	if got := Filter(skills, ""); len(got) != 2 {
		t.Errorf("空过滤命中全部，got %+v", got)
	}
}

func TestExpand(t *testing.T) {
	s := Skill{Name: "purity", Body: "正文第一行\n正文第二行"}
	got := Expand(s, "重构这个函数")
	want := "<skill name=\"purity\">\n正文第一行\n正文第二行\n</skill>\n\n重构这个函数"
	if got != want {
		t.Errorf("Expand:\n got: %q\nwant: %q", got, want)
	}
	if got := Expand(s, ""); !strings.HasSuffix(got, "</skill>") {
		t.Errorf("无任务时应只有正文包裹，got %q", got)
	}
}
