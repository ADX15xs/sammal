// Package human 提供面向终端用户的紧凑数值渲染，供 TUI、agent、provider
// 共用——三者都会把时长打给同一个用户，格式必须一致。
package human

import (
	"fmt"
	"time"
)

// Duration 把时长渲染为定宽紧凑形式：38s / 2m14s / 1h02m。
// 与 time.Duration.String() 的区别是补零（1m00s 而非 1m0s）、且小时级
// 丢弃秒——状态栏与落款都靠它保持列宽稳定。
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}
