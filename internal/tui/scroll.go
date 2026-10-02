// scroll.go：滚动缓冲区的安全落盘通道。一切进入滚动缓冲区的文本必须经
// printScroll，不得直接 tea.Println。
//
// tea.Println 走 bubbletea 内联渲染器的 insertAbove：它按「逻辑行数 +
// 行宽/终端宽」估算占行数，再用裸 ANSI 序列（上滚 + InsertLine）腾位写入，
// 最后把光标钉回帧顶。该路径有两个已核实缺陷（v2.0.9，main 未修）：
//
//  1. 行数估算对「显示宽度恰为终端宽整数倍」的行多算一行（延迟换行语义下
//     实际少占一行）——打印后真实光标比记录位置高一行的永久漂移；
//  2. 单次打印的折行总行数一旦超过视口余量，CursorUp/InsertLine 被钳制在
//     屏顶，打印后真实光标与帧顶失步——此后所有差分重绘错位（文本窜到
//     屏顶、中部大片空白），且渲染器无自愈机制。
//
// printScroll 的对策：按视口预算分批（每批折行总行数 ≤ printMargin）、
// 整数倍宽度行补尾空格（归一化到新旧估算公式的公共正确区）、超预算巨行
// 硬拆。宽度折算统一用 ansi.StringWidth——与 insertAbove 逐字节同一函数。
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// expandTabs 把单行里的 \t 按终端 8 列制表位展开为空格。ANSI 转义序列
// 原样拷贝、不占列——否则着色行里的 \t 列位会被算错。逐行独立展开
// （每行列号从 1 起算），与终端换行行为一致。
func expandTabs(line string) string {
	runes := []rune(line)
	var b strings.Builder
	col := 1
	for i := 0; i < len(runes); {
		r := runes[i]
		if r == '\x1b' {
			end := escapeEnd(runes, i)
			for ; i < end; i++ {
				b.WriteRune(runes[i])
			}
			continue
		}
		if r == '\t' {
			nextStop := ((col-1)/8+1)*8 + 1
			b.WriteString(strings.Repeat(" ", nextStop-col))
			col = nextStop
			i++
			continue
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
		i++
	}
	return b.String()
}

// escapeEnd 返回从 i 起的 ANSI 转义序列结束下标（i 指向 ESC）。支持 CSI
// （\x1b[ 参数/中间/终结字节）与单字符转义；无法识别时只跳过 ESC 本身。
func escapeEnd(runes []rune, i int) int {
	end := i + 1
	if end >= len(runes) {
		return end
	}
	if runes[end] != '[' {
		return end // 单字符转义
	}
	end++
	for end < len(runes) && runes[end] >= 0x30 && runes[end] <= 0x3f { // 参数字节
		end++
	}
	for end < len(runes) && runes[end] >= 0x20 && runes[end] <= 0x2f { // 中间字节
		end++
	}
	if end < len(runes) && runes[end] >= 0x40 && runes[end] <= 0x7e { // 终结字节
		end++
	}
	return end
}

// streamFlushLines 流式落盘触发线：流式块内闭合行数超过该值即把头部行
// 落进滚动缓冲区，帧内只留尾部窗口。取 6 = 显示上限 maxLines(8) 减去
// 半行与触发当帧的余量，保证触发前帧内永不出现省略号。
const streamFlushLines = 6

// wrappedRows 单行在宽度 w 的终端里的实际占行数。延迟换行语义：恰好铺满
// 整数行时不产生额外空行，故为 ceil(宽度/w) 而非 +1。
func wrappedRows(line string, w int) int {
	lw := ansi.StringWidth(line)
	if w <= 0 || lw == 0 {
		return 1
	}
	return (lw + w - 1) / w
}

// printSafeLine 整数倍宽度行补一个尾空格。宽度恰为 k·w（k≥2）的行实际占
// k 行，insertAbove 却估成 k+1 行；补空格后实际行数变为 k+1，与新旧两种
// 估算公式同时一致。空格不可见，随 insertAbove 自带的 EraseLineRight 清除。
func printSafeLine(line string, w int) string {
	if lw := ansi.StringWidth(line); w > 0 && lw > w && lw%w == 0 {
		return line + " "
	}
	return line
}

// splitForPrint 把待落盘文本切成若干批，每批就是一次 tea.Println 的载荷，
// 保证每批折行总行数 ≤ margin。批序即行序；除巨行硬拆与尾空格补齐外
// 不改写文本。行内 \t 先按 8 列制表位展开为空格，使 ansi.StringWidth 与
// 终端实际占列一致（ansi.StringWidth 把 \t 算成 0，会致 wrappedRows 严重
// 低估、insertAbove 超预算脱轨）。拖尾 \n 产生的空串元素剔除，避免多出
// 一个空行。
func splitForPrint(text string, w, margin int) []string {
	if text == "" {
		return nil
	}
	if margin < 1 {
		margin = 1
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var batches []string
	var batch []string
	used := 0
	flush := func() {
		if len(batch) > 0 {
			batches = append(batches, strings.Join(batch, "\n"))
			batch, used = nil, 0
		}
	}
	for _, line := range lines {
		line = expandTabs(line)
		line = printSafeLine(line, w)
		rows := wrappedRows(line, w)
		if rows > margin {
			flush()
			for _, chunk := range chunkByWidth(line, margin*w-1) {
				batches = append(batches, printSafeLine(chunk, w))
			}
			continue
		}
		if used+rows > margin {
			flush()
		}
		batch = append(batch, line)
		used += rows
	}
	flush()
	return batches
}

// chunkByWidth 把单行按显示宽度上限切成多段（字素簇边界切分，宽字符不切半）。
// ANSI 转义序列作为原子单元整体放入一段，不在中间切断——否则落盘后的着色
// 文本会出现残缺转义导致的乱码。
func chunkByWidth(line string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	var b strings.Builder
	used := 0
	state := -1
	rest := line
	for len(rest) > 0 {
		var seg string
		if rest[0] == 0x1b {
			end := escapeEndBytes(rest)
			seg = rest[:end]
			rest = rest[end:]
			state = -1 // 转义序列边界干净，重置字素状态
		} else {
			var s int
			seg, rest, _, s = uniseg.FirstGraphemeClusterInString(rest, state)
			state = s
		}
		cw := ansi.StringWidth(seg)
		if used+cw > width && b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
			used = 0
		}
		b.WriteString(seg)
		used += cw
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// escapeEndBytes 返回字符串开头 ANSI 转义序列结束的字节下标（s[0] 必须是
// ESC）。支持 CSI（\x1b[ 参数/中间/终结字节）与单字符转义；识别失败只跳过
// ESC 自己。转义序列由 ASCII 组成，字节下标即长度。
func escapeEndBytes(s string) int {
	end := 1
	if end >= len(s) {
		return end
	}
	if s[end] != '[' {
		return end
	}
	end++
	for end < len(s) && s[end] >= 0x30 && s[end] <= 0x3f {
		end++
	}
	for end < len(s) && s[end] >= 0x20 && s[end] <= 0x2f {
		end++
	}
	if end < len(s) && s[end] >= 0x40 && s[end] <= 0x7e {
		end++
	}
	return end
}

// printMargin 单次落盘的安全折行行数预算：视口高减去自绘面最大高度
// （思考行 + 流式尾部 8 行 + 状态栏 + 输入行，共 11 行）再留 3 行余量。
// 多行输入框会多吃行数——按实际可见行数动态扣减。insertAbove 的不变式
// 要求折行总行数 ≤ 视口高 − 帧高，故极矮终端下预算贴着下限 1 逐行落盘，
// 绝不超出真实余量；帧高本身超出视口（h < 12）属应用不可用态，不在保证范围。
func (m Model) printMargin() int {
	h := m.height
	if h <= 0 {
		h = 24 // WindowSizeMsg 未达前的兜底
	}
	// 基础帧高：思考(1) + 流式尾(8) + 状态(1) + 输入(1) + 余量(3) = 14
	frameH := 14
	// 多行输入框比单行多出的行数
	edLines := m.editor.lineCount()
	if max := m.editorMaxLines(); edLines > max {
		edLines = max
	}
	if edLines > 1 {
		frameH += edLines - 1
	}
	if margin := h - frameH; margin > 1 {
		return margin
	}
	return 1
}

// printScroll 文本安全落盘：按预算分批，单批一次 Println，多批经
// tea.Sequence 保序（Batch 无顺序保证）。
func (m Model) printScroll(text string) tea.Cmd {
	batches := splitForPrint(text, m.width, m.printMargin())
	if len(batches) == 0 {
		return nil
	}
	if len(batches) == 1 {
		return tea.Println(batches[0])
	}
	cmds := make([]tea.Cmd, len(batches))
	for i, b := range batches {
		cmds[i] = tea.Println(b)
	}
	return tea.Sequence(cmds...)
}

// printLines 斜杠命令等小段输出的落盘出口，与流式文本同一条安全通道。
func (m Model) printLines(lines []string) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	return m.printScroll(strings.Join(lines, "\n"))
}
