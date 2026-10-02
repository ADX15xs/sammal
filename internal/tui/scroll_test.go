package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"sammal/internal/agent"
)

// closedEvents 返回已关闭的事件通道（listenAgent 立即返回，不阻塞测试）。
func closedEvents() <-chan agent.Event {
	ch := make(chan agent.Event)
	close(ch)
	return ch
}

// ev 应用单个事件并返回更新后的 Model 与其产生的 cmd。
func ev(t *testing.T, m Model, e agent.Event) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.applyAgentEvent(e)
	return next.(Model), cmd
}

// wrappedRows 语义：空行/零宽占 1 行，其余 ceil(显示宽/终端宽)。
func TestWrappedRows(t *testing.T) {
	cases := []struct {
		line string
		w    int
		want int
	}{
		{"", 10, 1},
		{"abc", 10, 1},
		{"中文中文", 10, 1},  // 显示宽 8
		{"中文中文中", 10, 1}, // 显示宽 10 → 恰好铺满一行（延迟换行）
		{strings.Repeat("x", 20), 10, 2},
		{strings.Repeat("x", 21), 10, 3},
		{"abc", 0, 1}, // 宽度未知时按 1 行兜底
	}
	for _, c := range cases {
		if got := wrappedRows(c.line, c.w); got != c.want {
			t.Errorf("wrappedRows(%q, %d) = %d, want %d", c.line, c.w, got, c.want)
		}
	}
}

// 宽度归一化核心性质：补空格后，实际占行数与 insertAbove 的估算一致——
// 不然光标漂移照旧。估算公式是 bubbletea v2.0.9 cursed_renderer.go 的人工
// 转录，并非对真实渲染器的断言：升级 bubbletea / x-ansi 后须重审此快照
// （归一化策略本身在公共正确区，见 DEBT.md scroll.go 条目）。
func TestPrintSafeLineMatchesInsertAbove(t *testing.T) {
	for w := 1; w <= 120; w++ {
		for lw := 0; lw <= 3*w+3; lw++ {
			line := strings.Repeat("x", lw)
			got := wrappedRows(printSafeLine(line, w), w)
			estimate := 1 // insertAbove：每行计 1
			if lw > w {
				estimate += lw / w // 超宽行再加 lineWidth/w
			}
			if got != estimate {
				t.Fatalf("w=%d lw=%d: 归一化后占行 %d != insertAbove 估算 %d", w, lw, got, estimate)
			}
		}
	}
}

// 非整数倍宽度行不得被改动；恰好整宽（lw == w）不算越界。
func TestPrintSafeLineLeavesNormalLines(t *testing.T) {
	for _, line := range []string{"", "abc", strings.Repeat("x", 10), strings.Repeat("x", 21)} {
		if got := printSafeLine(line, 10); got != line {
			t.Errorf("printSafeLine(%q, 10) = %q, 应原样返回", line, got)
		}
	}
	if got := printSafeLine(strings.Repeat("x", 20), 10); got != strings.Repeat("x", 20)+" " {
		t.Errorf("整数倍宽度行应补尾空格: %q", got)
	}
}

// 预算下限：极矮终端预算贴 1 逐行落盘（超预算行走巨行硬拆），绝不虚高
// 超过真实余量——帧高 11 行，h-14 的 3 行余量在 h ≤ 15 时让位于下限。
func TestPrintMarginFloor(t *testing.T) {
	m := Model{}
	for _, c := range []struct{ h, want int }{
		{0, 10}, // WindowSizeMsg 未达：兜底 24
		{30, 16},
		{18, 4},
		{15, 1},
		{14, 1},
		{10, 1},
	} {
		m.height = c.h
		if got := m.printMargin(); got != c.want {
			t.Errorf("printMargin(h=%d) = %d, want %d", c.h, got, c.want)
		}
	}
}

func TestSplitForPrintEmpty(t *testing.T) {
	if got := splitForPrint("", 80, 16); got != nil {
		t.Errorf("空文本应返回 nil: %q", got)
	}
}

// 短文本单批落盘且内容逐行保留。
func TestSplitForPrintSingleBatch(t *testing.T) {
	text := "第一行\n\n第三行"
	got := splitForPrint(text, 80, 16)
	if len(got) != 1 {
		t.Fatalf("短文本应单批: %q", got)
	}
	if got[0] != text {
		t.Errorf("内容应原样保留: %q", got[0])
	}
}

// 多行文本按预算分批：每批折行总行数 ≤ margin，行序跨批保持。
func TestSplitForPrintRespectsBudget(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, strings.Repeat("x", 15)) // 每行 2 折行
	}
	batches := splitForPrint(strings.Join(lines, "\n"), 10, 5)
	if len(batches) < 2 {
		t.Fatalf("30 行 × 2 折行应分多批: %d 批", len(batches))
	}
	var got []string
	for i, b := range batches {
		rows := 0
		for _, ln := range strings.Split(b, "\n") {
			rows += wrappedRows(ln, 10)
			got = append(got, ln)
		}
		if rows > 5 {
			t.Errorf("批 %d 折行总行数 %d 超预算 5: %q", i, rows, b)
		}
	}
	var want []string
	for _, ln := range lines {
		want = append(want, printSafeLine(ln, 10))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("跨批拼接应等于逐行归一化的原文:\n got %q\nwant %q", got, want)
	}
}

// 巨行（折行后超预算）硬拆：每段单行成批、段占行 ≤ margin、
// 拼回去总折行数与总显示宽不变（内容无丢失）。
func TestSplitForPrintChunksMonsterLine(t *testing.T) {
	monster := strings.Repeat("m", 300) // w=10 → 30 折行，margin=4
	batches := splitForPrint(monster, 10, 4)
	rows, width := 0, 0
	for i, b := range batches {
		if n := strings.Count(b, "\n") + 1; n != 1 {
			t.Fatalf("巨行段应单行成批，批 %d 含 %d 行: %q", i, n, b)
		}
		if r := wrappedRows(b, 10); r > 4 {
			t.Errorf("批 %d 占行 %d 超预算 4: %q", i, r, b)
		} else {
			rows += r
		}
		width += ansi.StringWidth(strings.TrimSuffix(b, " "))
	}
	// 段界各自起行，总折行数只会 ≥ 30；内容保全由总显示宽兜底。
	if rows < 30 {
		t.Errorf("硬拆后总折行数应 ≥ 30: %d", rows)
	}
	if width != 300 {
		t.Errorf("硬拆拼接后总显示宽应保持 300: %d", width)
	}
}

// 行内 \t 展开为空格（8 列制表位），ANSI 转义原样保留且不占列位。
func TestExpandTabs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\tb", "a       b"},                             // 列位 1 后 \t → 下一制表位 9，7 空格
		{"1234567\tb", "1234567 b"},                       // 列位 8 后 \t → 9，1 空格
		{"12345678\tb", "12345678        b"},              // 列位 9 后 \t → 17，8 空格
		{"\x1b[2ma\tb\x1b[0m", "\x1b[2ma       b\x1b[0m"}, // ANSI 不计列位
		{"中文\tb", "中文    b"},                              // CJK 双宽：列位 5 → 9，4 空格
	}
	for _, c := range cases {
		if got := expandTabs(c.in); got != c.want {
			t.Errorf("expandTabs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 含 tab 的行展开后折行数不再被 0 宽度低估（insertAbove 超预算的根因）。
func TestSplitForPrintExpandsTabsBeforeBudget(t *testing.T) {
	line := "12345678\t9" // 8 列后 \t → 制表位 17，展开成 8 空格，显示宽 17
	batches := splitForPrint(line, 10, 3)
	if len(batches) != 1 {
		t.Fatalf("应单批: %q", batches)
	}
	if got := ansi.StringWidth(strings.TrimSuffix(batches[0], " ")); got != 17 {
		t.Errorf("展开后显示宽 = %d, want 17（终端 tab 展开后的真实宽度）", got)
	}
	if strings.Contains(batches[0], "\t") {
		t.Errorf("输出不应残留字面 tab: %q", batches[0])
	}
}

// 拖尾 \n 不产生空批 / 多余空行（原 strings.Split 会附加空串元素）。
func TestSplitForPrintTrailingNewline(t *testing.T) {
	got := splitForPrint("hello\n", 80, 16)
	if len(got) != 1 {
		t.Fatalf("拖尾换行应单批: %q", got)
	}
	if got[0] != "hello" {
		t.Errorf("批尾不应有空行: %q", got[0])
	}
	// 中间空行（分段）应保留。
	got = splitForPrint("a\n\nb", 80, 16)
	if len(got) != 1 || got[0] != "a\n\nb" {
		t.Errorf("中间空行应保留: %q", got)
	}
}

// chunkByWidth 不切断 ANSI：着色巨行硬拆后每个转义序列保持完整。
func TestChunkByWidthANSISafe(t *testing.T) {
	colored := "\x1b[2m" + strings.Repeat("x", 20) + "\x1b[0m"
	chunks := chunkByWidth(colored, 5)
	for i, c := range chunks {
		if strings.Contains(c, "\x1b") && !strings.Contains(c, "\x1b[0m") {
			// 简单断言：开头转义 \x1b[2m 和结尾 \x1b[0m 都不该被拆半。
			if !strings.HasSuffix(c, "m") && !strings.HasPrefix(c, "\x1b") {
				t.Errorf("段 %d 可能切断 ANSI: %q", i, c)
			}
		}
	}
	// 拼接后应保留全部转义与内容。
	joined := strings.Join(chunks, "")
	if strings.Count(joined, "\x1b[") != 2 {
		t.Errorf("ANSI 转义数量应保持 2: %q", joined)
	}
	if !strings.Contains(joined, "xxxxx") && !strings.Contains(joined, "x") {
		t.Errorf("内容应保留: %q", joined)
	}
}

// 流式渐进落盘：闭合行未达触发线不动，超过即把头部行落盘、帧内只留尾部。
func TestStreamProgressiveFlush(t *testing.T) {
	m := New(Deps{ModelName: "m", Events: closedEvents()})
	m.width, m.height = 80, 30
	m, _ = ev(t, m, agent.TurnStartedEvent{})

	m, cmd := ev(t, m, agent.TextDeltaEvent{Text: "l1\nl2\nl3\nl4\nl5\nl6\n半"})
	if prints := printedLines(cmd); len(prints) != 0 {
		t.Fatalf("6 闭合行不应触发落盘: %q", prints)
	}
	if got := m.stream.String(); got != "l1\nl2\nl3\nl4\nl5\nl6\n半" {
		t.Fatalf("触发线内流式块应原样保留: %q", got)
	}

	m, cmd = ev(t, m, agent.TextDeltaEvent{Text: "\nl7"})
	prints := printedLines(cmd)
	if len(prints) != 1 || prints[0] != "l1\nl2\nl3\nl4\nl5\nl6\n半" {
		t.Fatalf("第 7 行闭合应落盘头部 7 行: %q", prints)
	}
	if got := m.stream.String(); got != "l7" {
		t.Errorf("落盘后流式块应只剩尾部: %q", got)
	}
	if !m.streamPrinted {
		t.Error("落盘发生应置 streamPrinted")
	}
}

// 定稿补余：流式块剩余（含未闭合行）落盘后清空、标记复位；
// 渐进前缀 + 补余 == 全文。
func TestFinalizeFlushesRemainder(t *testing.T) {
	m := New(Deps{ModelName: "m", Events: closedEvents()})
	m.width, m.height = 80, 30
	m, _ = ev(t, m, agent.TurnStartedEvent{})

	full := strings.Repeat("行内容\n", 10) + "结尾"
	var prints []string
	m, cmd := ev(t, m, agent.TextDeltaEvent{Text: full})
	prints = append(prints, printedLines(cmd)...)
	m, cmd = ev(t, m, agent.MessageFinalEvent{Text: full})
	prints = append(prints, printedLines(cmd)...)

	var got strings.Builder
	for _, p := range prints {
		got.WriteString(p)
		got.WriteString("\n")
	}
	if want := strings.Repeat("行内容\n", 10) + "结尾\n"; got.String() != want {
		t.Fatalf("渐进前缀 + 补余应等于全文:\n got %q\nwant %q", got.String(), want)
	}
	if m.stream.Len() != 0 || m.streamPrinted {
		t.Errorf("定稿后流式块与标记应复位: stream=%q printed=%v", m.stream.String(), m.streamPrinted)
	}
}

// 重试作废：已落盘过则打标记并复位；未落盘过则静默清块（与旧行为一致）。
func TestStreamRestartedMarker(t *testing.T) {
	m := New(Deps{ModelName: "m", Events: closedEvents()})
	m.width, m.height = 80, 30
	m, _ = ev(t, m, agent.TurnStartedEvent{})

	m, cmd := ev(t, m, agent.TextDeltaEvent{Text: strings.Repeat("x\n", 8)})
	if len(printedLines(cmd)) == 0 {
		t.Fatal("前置：应已触发过落盘")
	}
	m, cmd = ev(t, m, agent.StreamRestartedEvent{})
	prints := printedLines(cmd)
	if len(prints) != 1 || !strings.Contains(prints[0], "已作废") {
		t.Fatalf("已落盘后重连应打作废标记: %q", prints)
	}
	if m.stream.Len() != 0 || m.streamPrinted {
		t.Errorf("重连后流式块与标记应复位: %q %v", m.stream.String(), m.streamPrinted)
	}

	m, cmd = ev(t, m, agent.StreamRestartedEvent{})
	if prints := printedLines(cmd); len(prints) != 0 {
		t.Fatalf("无落盘时重连不应打标记: %q", prints)
	}
}

// 中止定稿三分支：有剩余 → 剩余 + 中断标记；无剩余但已落盘 → 仅标记；
// 全空 → 内容未定稿。
func TestInterruptedFinalize(t *testing.T) {
	m := New(Deps{ModelName: "m", Events: closedEvents()})
	m.width, m.height = 80, 30
	m, _ = ev(t, m, agent.TurnStartedEvent{})
	m, _ = ev(t, m, agent.TextDeltaEvent{Text: "abc"})

	m, cmd := ev(t, m, agent.MessageFinalEvent{Text: "abc", Interrupted: true})
	prints := printedLines(cmd)
	if len(prints) != 2 || prints[0] != "abc" || !strings.Contains(prints[1], "以上内容被中断") {
		t.Fatalf("有剩余的中止应打印剩余 + 标记: %q", prints)
	}

	m, _ = ev(t, m, agent.TurnStartedEvent{})
	m, _ = ev(t, m, agent.TextDeltaEvent{Text: strings.Repeat("x\n", 8)}) // 触发落盘
	m, cmd = ev(t, m, agent.MessageFinalEvent{Text: "x", Interrupted: true})
	prints = printedLines(cmd)
	if len(prints) != 1 || !strings.Contains(prints[0], "以上内容被中断") {
		t.Fatalf("无剩余但已落盘的中止应只打标记: %q", prints)
	}

	m, _ = ev(t, m, agent.TurnStartedEvent{})
	m, cmd = ev(t, m, agent.MessageFinalEvent{Text: "", Interrupted: true})
	prints = printedLines(cmd)
	if len(prints) != 1 || !strings.Contains(prints[0], "内容未定稿") {
		t.Fatalf("全空中止应打未定稿: %q", prints)
	}
}

// 多行长回显经安全通道分批落盘，每批不超过视口预算（用户粘贴超长文本
// 与长回答同属 insertAbove 的风险面）。
func TestUserEchoBatchedSafePrint(t *testing.T) {
	m := New(Deps{
		ModelName: "m",
		Events:    closedEvents(),
		Send:      func(string, []string) {},
	})
	m.width, m.height = 80, 30
	m.editor.Insert(strings.Repeat("粘贴行\n", 40))

	next, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	prints := printedLines(cmd)
	if len(prints) < 2 {
		t.Fatalf("40 行回显应分多批: %q", prints)
	}
	for i, p := range prints {
		if rows := wrappedRows(p, m.width); rows > m.printMargin() {
			t.Errorf("回显批 %d 占行 %d 超预算 %d", i, rows, m.printMargin())
		}
	}
}
