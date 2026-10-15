package fetcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemeAllowlistDefaultsToHTTPOnly 钉住协议白名单的默认姿态:
// 只有 http/https 放行,其余一律拒绝 —— 不靠"黑名单补漏"。
func TestSchemeAllowlistDefaultsToHTTPOnly(t *testing.T) {
	cases := []struct {
		协议 string
		想  bool
	}{
		{"http", true},
		{"https", true},
		{"HTTP", true},   // 大小写不敏感
		{" https", true}, // 带空白也认
		{"file", false},
		{"ftp", false},
		{"gopher", false},
		{"data", false},
		{"javascript", false},
		{"", false},
	}
	for _, c := range cases {
		if got := schemeAllowed(c.协议, nil); got != c.想 {
			t.Errorf("默认配置下 schemeAllowed(%q) = %v,想要 %v", c.协议, got, c.想)
		}
	}
	if schemeAllowed("file", []string{"FILE:"}) != true {
		t.Error("显式放行 file 之后应当允许,且大小写与冒号都不该影响判定")
	}
	if schemeAllowed("gopher", []string{"file"}) {
		t.Error("只放行 file 不该顺带把 gopher 也放出去")
	}
}

// TestSplitSchemes 覆盖配置解析:逗号分隔、空白、大小写、去重、尾冒号。
func TestSplitSchemes(t *testing.T) {
	got := SplitSchemes(" file , FTP:,file,, http ")
	want := []string{"file", "ftp", "http"}
	if len(got) != len(want) {
		t.Fatalf("SplitSchemes 结果 %v,想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitSchemes 结果 %v,想要 %v", got, want)
		}
	}
	if n := len(SplitSchemes("")); n != 0 {
		t.Errorf("空配置应当解析成空列表,实际 %d 项", n)
	}
}

// TestFetchRejectsOtherSchemesByDefault 钉住入口行为:未放行的协议在
// 联网之前就被拒,并且错误信息点明"默认禁止、需要显式打开"。
func TestFetchRejectsOtherSchemesByDefault(t *testing.T) {
	for _, u := range []string{
		"file:///etc/passwd",
		"ftp://example.com/x.html",
		"gopher://example.com/",
	} {
		_, err := Fetch(u, Options{})
		if err == nil {
			t.Fatalf("%s 居然被放行", u)
		}
		if !strings.Contains(err.Error(), "未放行") {
			t.Errorf("%s 的拒绝理由没点明协议问题: %v", u, err)
		}
	}
}

// TestFetchLocalFileOnlyWhenEnabled 钉住 file:// 是"默认关闭、显式开启"的:
// 开启后真能读到文件,读不到时给出可读的错误。
func TestFetchLocalFileOnlyWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "snapshot.html")
	const 内容 = "<html><body><p>本地快照</p></body></html>"
	if err := os.WriteFile(p, []byte(内容), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Fetch("file://"+p, Options{}); err == nil {
		t.Fatal("默认配置下 file:// 不该被放行")
	}

	res, err := Fetch("file://"+p, Options{AllowSchemes: []string{"file"}})
	if err != nil {
		t.Fatalf("显式放行 file 后应当能读: %v", err)
	}
	if res.HTML != 内容 {
		t.Errorf("读到的内容不对: %q", res.HTML)
	}
	if res.StatusCode != 200 {
		t.Errorf("file:// 的 StatusCode 应当是 200,实际 %d", res.StatusCode)
	}
	if res.Bytes != int64(len(内容)) {
		t.Errorf("Bytes 应当是 %d,实际 %d", len(内容), res.Bytes)
	}

	if _, err := Fetch("file://"+filepath.Join(dir, "不存在.html"),
		Options{AllowSchemes: []string{"file"}}); err == nil {
		t.Error("读不到的文件应当报错,而不是返回空页面")
	}
}

// TestFetchLocalFileHonoursMaxBytes 钉住 file:// 也受大小上限约束
// (不能因为读的是本地文件就把上限绕过去)。
func TestFetchLocalFileHonoursMaxBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.html")
	big := strings.Repeat("A", 4096)
	if err := os.WriteFile(p, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Fetch("file://"+p, Options{AllowSchemes: []string{"file"}, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if res.Bytes > 1024 {
		t.Errorf("超过了 MaxBytes 仍读回 %d 字节", res.Bytes)
	}
	if len(res.HTML) > 1024+64 {
		t.Errorf("返回内容没被截断: %d 字节", len(res.HTML))
	}
	if !strings.Contains(res.HTML, "已截断") {
		t.Error("截断后应当留下标记")
	}
}
