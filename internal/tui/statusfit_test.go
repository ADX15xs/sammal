package tui

import (
	"strings"
	"testing"
)

// mkSegs 构造与 statusLine 相同形状的完整段序列（模型名 + 全部信息段，
// 按真实顺序排列），用于纯函数级的裁剪断点测试。
func mkSegs() []statusSeg {
	return []statusSeg{
		{text: "model"},                                        // pri 0（隐式）模型名
		{text: "in 1234 out 567", pri: 2},                      // in/out
		{text: "cache 87%", pri: 3},                            // cache
		{text: "ctx 43%", pri: 1},                              // ctx
		{text: "工具 5", pri: 5},                                // 工具数
		{text: "* 2m14s", pri: 4},                              // 计时器
	}
}

func joined(segs []statusSeg) string { return strings.Join(segTexts(segs), " | ") }

// 断点矩阵：从宽到窄逐步收窄，验证每一步丢的段与注释声明一致：
// 工具(5) → 计时(4) → cache(3) → in/out(2) → ctx(1)，模型名永存。
// 用逐 token 收窄而非跳变采样，保证任何宽度下都不出现顺序错乱。
func TestDropToFitBreakpointMatrix(t *testing.T) {
	base := mkSegs()

	// 从全量可见的最小宽度开始，一路收到只剩模型名，记录每个宽度下的存活段。
	type state struct {
		width int
		live  []string
	}
	var states []state
	for w := 120; w >= 4; w-- {
		got := dropToFit(append([]statusSeg(nil), base...), w)
		states = append(states, state{width: w, live: segTexts(got)})
	}

	// 单调性：随宽度收窄，存活段集合只减不增、不换序。
	for i := 1; i < len(states); i++ {
		prev, cur := states[i-1].live, states[i].live
		if len(cur) > len(prev) {
			t.Fatalf("宽度 %d 段数比 %d 还多: %v -> %v", states[i].width, states[i-1].width, prev, cur)
		}
		// cur 必须是 prev 的子序列。
		j := 0
		for _, p := range prev {
			if j < len(cur) && cur[j] == p {
				j++
			}
		}
		if j != len(cur) {
			t.Fatalf("宽度 %d 存活段 %v 不是上一步 %v 的子序列（发生换序）", states[i].width, cur, prev)
		}
	}

	// 关键断点抽查：最宽处全存活；最窄处只剩模型名；中间态按优先级丢。
	all := strings.Join(segTexts(dropToFit(append([]statusSeg(nil), base...), 200)), " | ")
	if !strings.Contains(all, "工具 5") || !strings.Contains(all, "ctx 43%") {
		t.Fatalf("超宽处应全段存活: %q", all)
	}
	bare := segTexts(dropToFit(append([]statusSeg(nil), base...), len("model")))
	if len(bare) != 1 || bare[0] != "model" {
		t.Fatalf("预算只够模型名时应只剩模型名: %v", bare)
	}
	// 工具数先于计时器消失：找一个工具已丢但计时还在的中间态。
	sawMiddle := false
	for _, st := range states {
		hasTool, hasTimer := false, false
		for _, s := range st.live {
			if s == "工具 5" {
				hasTool = true
			}
			if s == "* 2m14s" {
				hasTimer = true
			}
		}
		if !hasTool && hasTimer {
			sawMiddle = true
			break
		}
	}
	if !sawMiddle {
		t.Error("从未观察到「工具已丢、计时仍在」的中间态：丢弃顺序错误")
	}
}

// 负优先级段在任何预算下都不可被丢弃（生成中标记 = -1）。
func TestDropToFitNeverDropsNegativePriority(t *testing.T) {
	segs := []statusSeg{
		{text: "m"},
		{text: "in 123456 out 123456", pri: 2},
		{text: "cache 87%", pri: 3},
		{text: "ctx 100%", pri: 1},
		{text: "工具 99", pri: 5},
		{text: "* 生成中", pri: -1},
	}
	got := segTexts(dropToFit(append([]statusSeg(nil), segs...), 5))
	joined0 := strings.Join(got, " | ")
	if !strings.Contains(joined0, "* 生成中") {
		t.Errorf("负优先级段被丢弃: %q", joined0)
	}
	if !strings.Contains(joined0, "m") {
		t.Errorf("首段（模型名）被丢弃: %q", joined0)
	}
}
