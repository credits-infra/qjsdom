package fetcher

import (
	"strings"
	"testing"
)

// TestCheckScheme 钉住"入口共用同一条协议规则"。
//
// 起因(实测):MCP 入口曾经自己写死 `只支持 http/https URL`,于是
// QJSDOM_ALLOW_SCHEMES 在那里形同虚设 —— 配置里放行了 file,fetcher 也会放行,
// 但请求在到达 fetcher 之前就被入口拦掉了。规则只该有一份,所以暴露 CheckScheme。
func TestCheckScheme(t *testing.T) {
	cases := []struct {
		名    string
		url  string
		放行   []string
		想报错  bool
		想含字样 string
	}{
		{"http 恒放行", "http://example.com/x", nil, false, ""},
		{"https 恒放行", "https://example.com/x", nil, false, ""},
		{"大写协议也认", "HTTPS://example.com/x", nil, false, ""},
		{"file 默认拒绝", "file:///etc/passwd", nil, true, "未放行"},
		{"ftp 默认拒绝", "ftp://example.com/x", nil, true, "未放行"},
		{"gopher 默认拒绝", "gopher://example.com/", nil, true, "未放行"},
		{"显式放行 file", "file:///tmp/x.html", []string{"file"}, false, ""},
		{"放行 file 不影响 ftp", "ftp://example.com/x", []string{"file"}, true, "未放行"},
		{"URL 解析失败也报错", "://坏地址", nil, true, "解析失败"},
	}
	for _, c := range cases {
		err := CheckScheme(c.url, c.放行)
		if c.想报错 && err == nil {
			t.Errorf("%s: %s 应当被拒", c.名, c.url)
			continue
		}
		if !c.想报错 && err != nil {
			t.Errorf("%s: %s 不该被拒,实际 %v", c.名, c.url, err)
			continue
		}
		if c.想含字样 != "" && !strings.Contains(err.Error(), c.想含字样) {
			t.Errorf("%s: 拒绝理由里应当含 %q,实际 %v", c.名, c.想含字样, err)
		}
	}
}

// TestFetchUsesCheckScheme 确认 Fetch 走的是同一条规则(措辞也一致),
// 免得以后两处又漂开。
func TestFetchUsesCheckScheme(t *testing.T) {
	_, errFetch := Fetch("file:///etc/passwd", Options{})
	errCheck := CheckScheme("file:///etc/passwd", nil)
	if errFetch == nil || errCheck == nil {
		t.Fatal("两边都该拒绝 file://")
	}
	if errFetch.Error() != errCheck.Error() {
		t.Errorf("Fetch 与 CheckScheme 的拒绝理由不一致:\n  Fetch: %v\n  检查: %v", errFetch, errCheck)
	}
}
