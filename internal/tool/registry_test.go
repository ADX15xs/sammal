package tool

import "testing"

func namesOf(ts []Tool) []string {
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		names = append(names, t.Name())
	}
	return names
}

func assertNames(t *testing.T, ts []Tool, want []string) {
	t.Helper()
	got := namesOf(ts)
	if len(got) != len(want) {
		t.Fatalf("工具 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("工具 = %v, want %v", got, want)
		}
	}
}

// I2：缺省集合的注册顺序即 Defs() 序列化顺序，不得漂移。
func TestResolveDefaultOrder(t *testing.T) {
	assertNames(t, Resolve("/work", "bash", nil), defaultToolNames)
	assertNames(t, Resolve("/work", "bash", []string{}), defaultToolNames)
}

// 子集：只保留配置命中的工具，顺序与配置一致。
func TestResolveSubset(t *testing.T) {
	assertNames(t, Resolve("/work", "bash", []string{"read", "write"}), []string{"read", "write"})
	// 配置序优先，非默认序。
	assertNames(t, Resolve("/work", "bash", []string{"glob", "grep"}), []string{"glob", "grep"})
}

// 未知工具名静默跳过：配置写错不崩，但不应产生未知工具。
func TestResolveUnknownSkipped(t *testing.T) {
	assertNames(t, Resolve("/work", "bash", []string{"read", "nope", "write"}), []string{"read", "write"})
	// 全部未知：空工具集（agent 仍可对话，只是无工具）。
	if ts := Resolve("/work", "bash", []string{"nope"}); len(ts) != 0 {
		t.Errorf("全未知应空: %v", namesOf(ts))
	}
}

// 实例装配：cwd/shell 正确注入（bash 的 shellNote 插值依赖 shell 定格）。
func TestResolveInstantiatesFields(t *testing.T) {
	ts := Resolve("/work/dir", "powershell", []string{"bash", "read"})
	bt, ok := ts[0].(*BashTool)
	if !ok || bt.WorkDir != "/work/dir" || bt.Shell != "powershell" {
		t.Errorf("bash tool = %+v", bt)
	}
	rt, ok := ts[1].(*ReadTool)
	if !ok || rt.WorkDir != "/work/dir" {
		t.Errorf("read tool = %+v", rt)
	}
}
