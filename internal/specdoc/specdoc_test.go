package specdoc

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 文档契约测试：把 SPEC 的文档级承诺变成机器把守（superagent tests/docs.spec.ts 范式）。
// 本包无被测代码，`go test ./internal/specdoc` 即跑全部契约。

const repoRoot = "../.."

var invariantHeader = regexp.MustCompile(`(?m)^### (I\d+) `)

// invariants 从 SPEC 第 2 章解析不变量编号——测试派生自文档而非硬编码清单：
// 文档加一条，契约自动跟上；文档编号断档，这里先红。
func invariants(t *testing.T) []string {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join(repoRoot, "docs", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	matches := invariantHeader.FindAllStringSubmatch(string(spec), -1)
	if len(matches) == 0 {
		t.Fatal("SPEC.md 未解析到不变量条目（### I<n> 标题）")
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		if want := "I" + strconv.Itoa(i+1); m[1] != want {
			t.Errorf("不变量编号不连续：第 %d 条是 %s，应为 %s", i+1, m[1], want)
		}
		ids[i] = m[1]
	}
	return ids
}

// walkRepo 收集 internal 与 cmd 下指定后缀的文件。
func walkRepo(t *testing.T, suffix string) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"internal", "cmd"} {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), suffix) {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// TestInvariantTestAnchors 每条不变量必须在某个 *_test.go 有测试锚点
//（「I3 崩溃恢复」式标记或 TestI1 式命名均可）——I6 零隐性债的可执行化：
// 没有锚点的不变量等于静默放弃，要么补测试要么在 SPEC 写明豁免。
func TestInvariantTestAnchors(t *testing.T) {
	for _, id := range invariants(t) {
		anchor := regexp.MustCompile(`\b` + id + `\b`)
		found := false
		for _, path := range walkRepo(t, "_test.go") {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if anchor.Match(data) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s 无任何测试锚点：补可测试定义，或在 SPEC 中写明豁免理由", id)
		}
	}
}

// I4 单渲染路径：非测试代码禁止出现 WithAltScreen——I4 测试定义（代码审查
// 清单项）的机器把守，不再依赖人工记得查。
func TestI4NoAltScreen(t *testing.T) {
	for _, path := range walkRepo(t, ".go") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "WithAltScreen") {
			t.Errorf("%s 出现 WithAltScreen：击穿 I4 单渲染路径", path)
		}
	}
}

// I6 / SPEC 3.3：禁止裸 TODO——代码内 TODO/FIXME 必须同行关联 DEBT 账本
//（写明怎么还），否则视为未入账的隐性妥协。
func TestI6NoBareTODO(t *testing.T) {
	mark := regexp.MustCompile(`\b(?:TODO|FIXME)\b`)
	for _, path := range walkRepo(t, ".go") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if mark.MatchString(line) && !strings.Contains(line, "DEBT") {
				t.Errorf("%s:%d 裸 TODO/FIXME：补原因与 DEBT.md 条目，或直接删除", path, i+1)
			}
		}
	}
}

// 文档交叉引用一致性：README/DEBT/AGENTS/CHANGELOG 与 docs/*.md 的本地相对链接必须可解析
//（superagent docs.spec.ts：编号文档只引更早文档、本地链接可解析）。
func TestDocCrossReferences(t *testing.T) {
	files := []string{
		filepath.Join(repoRoot, "README.md"),
		filepath.Join(repoRoot, "DEBT.md"),
		filepath.Join(repoRoot, "AGENTS.md"),
		filepath.Join(repoRoot, "CHANGELOG.md"),
	}
	docs, err := filepath.Glob(filepath.Join(repoRoot, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, docs...)
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	scheme := regexp.MustCompile(`^[a-z]+:`)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range link.FindAllStringSubmatch(string(data), -1) {
			target := strings.SplitN(m[1], "#", 2)[0]
			if target == "" || scheme.MatchString(target) {
				continue
			}
			// 无扩展名的目标（../../releases 等 GitHub 元页面）解析在仓库站点内，不在文件系统。
			if !strings.Contains(filepath.Base(target), ".") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(file), filepath.FromSlash(target))); err != nil {
				t.Errorf("%s 链接目标不存在：%s", file, target)
			}
		}
	}
}

// 文档地图唯一索引（AGENTS.md §0.4，superagent docs-index.spec.ts 范式）：
// docs/ 下每个 .md 与四个根文档必须登记进 AGENTS.md——旁路新建文档绕过唯一索引即红。
func TestDocMapIndex(t *testing.T) {
	agents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs, err := filepath.Glob(filepath.Join(repoRoot, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"README.md", "DEBT.md", "CHANGELOG.md", "AGENTS.md"}
	for _, d := range docs {
		names = append(names, filepath.ToSlash(filepath.Join("docs", filepath.Base(d))))
	}
	for _, name := range names {
		if !strings.Contains(string(agents), name) {
			t.Errorf("%s 未登记进 AGENTS.md §0.4 文档地图：新增 / 移动文档必须同步唯一索引", name)
		}
	}
}

// 800 行棘轮（SPEC 第 10 章预警信号，Reasonix 同规则）：全仓 .go（含测试）
// 超 800 行即红——从人工收尾检查升级为机器把守。
func TestLineLimitRatchet(t *testing.T) {
	const limit = 800
	for _, path := range walkRepo(t, ".go") {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(data), "\n") + 1; n > limit {
			t.Errorf("%s %d 行 > %d：拆分，或入 DEBT 说明", path, n, limit)
		}
	}
}
