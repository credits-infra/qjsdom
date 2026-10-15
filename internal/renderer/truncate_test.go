package renderer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMaxTextLenCountsRunes 钉住「MAX_TEXT 按字符截断」。
//
// 曾经用 len(s) 与 s[:n] 截断 —— 那是字节下标,中文正文被切在字符中间:
// 输出的最后一段不是合法 UTF-8(实测 `qjsdom -file 中文页 -max 10` 直接
// 产出坏字节),经过 JSON 序列化则变成 U+FFFD,文字被静默损坏。
func TestMaxTextLenCountsRunes(t *testing.T) {
	正文 := "中国的生物化学考试大纲很重要必须完整读完才能作答"
	for _, n := range []int{1, 2, 5, 10, 11, 12} {
		got := truncateRunes(正文, n)
		if !utf8.ValidString(got) {
			t.Errorf("截断到 %d 个字符后不是合法 UTF-8: %q", n, got)
			continue
		}
		body := strings.TrimSuffix(got, "…(已截断)")
		首段 := []rune(body)
		if len(首段) != n {
			t.Errorf("截断到 %d 个字符,实际留下 %d 个: %q", n, len(首段), body)
		}
		if !strings.HasPrefix(正文, body) {
			t.Errorf("截断结果不是原文的前缀(切到了字符内部?): %q", body)
		}
	}
}

// TestCutRunesEdgeCases 覆盖零值与边界:不截断时不该加提示,超长时不该多切。
func TestCutRunesEdgeCases(t *testing.T) {
	cases := []struct {
		名  string
		输入 string
		上限 int
		想  string
		截了 bool
	}{
		{"上限为 0 表示不截断", "中国的生物化学", 0, "中国的生物化学", false},
		{"上限为负也不截断", "abc", -3, "abc", false},
		{"字符数与上限相等时不加提示", "中国的", 3, "中国的", false},
		{"字符数少于上限时不加提示", "中国", 9, "中国", false},
		{"字符数超上限时按字符切", "中国的生物化学", 3, "中国的", true},
		{"emoji 按一个字符算", "图🖼️例", 2, "图🖼", true},
		{"纯 ASCII 行为不变", "abcdef", 3, "abc", true},
	}
	for _, c := range cases {
		got, cut := cutRunes(c.输入, c.上限)
		if got != c.想 || cut != c.截了 {
			t.Errorf("%s: cutRunes(%q, %d) = (%q, %v),想要 (%q, %v)",
				c.名, c.输入, c.上限, got, cut, c.想, c.截了)
		}
	}
	if got := truncateRunes("中国的", 3); got != "中国的" {
		t.Errorf("没截断时不该追加提示,实际 %q", got)
	}
}

// TestTruncateKeepsErrorTextValid 钉住错误信息截断也不再切出坏字节。
func TestTruncateKeepsErrorTextValid(t *testing.T) {
	got := truncate("解析失败:这是一段很长的中文错误说明,用于确认截断发生在字符边界上", 8)
	if !utf8.ValidString(got) {
		t.Fatalf("错误信息截断后不是合法 UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("截断了却没给出省略号: %q", got)
	}
	if n := len([]rune(strings.TrimSuffix(got, "…"))); n != 8 {
		t.Errorf("只想留 8 个字符,实际 %d 个: %q", n, got)
	}
}
