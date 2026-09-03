package human

import (
	"testing"
	"time"
)

// 三档边界：秒 → 分秒 → 时分。补零与 Duration.String() 的关键差异在此固定。
func TestDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{400 * time.Millisecond, "0s"},
		{38 * time.Second, "38s"},
		{59*time.Second + 600*time.Millisecond, "1m00s"}, // 跨档进位
		{90 * time.Second, "1m30s"},
		{2*time.Minute + 14*time.Second, "2m14s"},
		{3599 * time.Second, "59m59s"},
		{3600 * time.Second, "1h00m"},
		{3661 * time.Second, "1h01m"},
		{5 * time.Hour, "5h00m"},
	}
	for _, c := range cases {
		if got := Duration(c.d); got != c.want {
			t.Errorf("Duration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
