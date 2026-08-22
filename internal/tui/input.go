package tui

import (
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// InputLine 是 CJK 安全的单行输入：uniseg 切分字素簇、runewidth 计算
// 终端列宽，光标交给终端原生绘制（bar 形状），自绘面不覆盖宽字符。
// 粘贴的多行内容按字素平铺存储（提交时完整发送），展示层只显示首行。
type InputLine struct {
	clusters []string
	cursor   int // 光标左侧的字素数，0..len(clusters)
}

func graphemes(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	state := -1
	for len(s) > 0 {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		out = append(out, cluster)
	}
	return out
}

func (l *InputLine) Insert(s string) {
	l.clusters = append(l.clusters[:l.cursor], append(graphemes(s), l.clusters[l.cursor:]...)...)
	l.cursor += countGraphemes(s)
}

func countGraphemes(s string) int {
	n := 0
	state := -1
	for len(s) > 0 {
		_, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		n++
	}
	return n
}

func (l *InputLine) Backspace() bool {
	if l.cursor == 0 {
		return false
	}
	l.clusters = append(l.clusters[:l.cursor-1], l.clusters[l.cursor:]...)
	l.cursor--
	return true
}

func (l *InputLine) Delete() bool {
	if l.cursor >= len(l.clusters) {
		return false
	}
	l.clusters = append(l.clusters[:l.cursor], l.clusters[l.cursor+1:]...)
	return true
}

func (l *InputLine) Left() bool {
	if l.cursor == 0 {
		return false
	}
	l.cursor--
	return true
}

func (l *InputLine) Right() bool {
	if l.cursor >= len(l.clusters) {
		return false
	}
	l.cursor++
	return true
}

func (l *InputLine) Home() { l.cursor = 0 }

func (l *InputLine) End() { l.cursor = len(l.clusters) }

func (l *InputLine) Clear() {
	l.clusters = nil
	l.cursor = 0
}

func (l *InputLine) Empty() bool { return len(l.clusters) == 0 }

func (l *InputLine) String() string { return strings.Join(l.clusters, "") }

// firstLineLen 返回首行（到第一个换行字素前）的字素数，用于展示与光标钳制。
func (l *InputLine) firstLineLen() int {
	for i, c := range l.clusters {
		if c == "\n" || c == "\r" {
			return i
		}
	}
	return len(l.clusters)
}

// Render 返回输入行展示文本与光标列（宽度上限 maxWidth，含 prefix）。
// 超宽时窗口滚动保持光标可见；多行粘贴只显示首行与换行数标注。
func (l *InputLine) Render(prefix string, maxWidth int) (display string, cursorCol int) {
	first := l.firstLineLen()
	suffix := ""
	if len(l.clusters) > first {
		newlines := 1
		for _, c := range l.clusters[first:] {
			if c == "\n" {
				newlines++
			}
		}
		suffix = " +" + strconv.Itoa(newlines) + "L"
	}

	// 预留省略号（"..." 3 列）与光标槽（光标不越过最后一个单元格）。
	budget := maxWidth - widthOf(prefix) - widthOf(suffix) - 4
	if budget < 1 {
		budget = 1
	}

	end := min(l.cursor, first)
	start := end
	used := 0
	for start > 0 && used+widthOf(l.clusters[start-1]) <= budget {
		used += widthOf(l.clusters[start-1])
		start--
	}
	for end < first && used+widthOf(l.clusters[end]) <= budget {
		used += widthOf(l.clusters[end])
		end++
	}

	ellipsis := ""
	if start > 0 {
		ellipsis = "..."
	}
	body := strings.Join(l.clusters[start:end], "")
	beforeCursor := strings.Join(l.clusters[start:min(l.cursor, end)], "")
	display = prefix + ellipsis + body + suffix
	cursorCol = widthOf(prefix) + widthOf(ellipsis) + widthOf(beforeCursor)
	return display, cursorCol
}

func widthOf(s string) int { return runewidth.StringWidth(s) }
