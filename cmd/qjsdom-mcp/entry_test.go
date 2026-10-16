package main

// MCP 入口的两条行为,第三轮交叉评审(B2/B5/C4)用变异测试证明它们当时
// 【没有任何回归锁】—— 把行为改回旧样子,测试依然全绿。这里各补一条:
//   ① 入口的协议门:默认拒 file://,显式放行后必须能过
//      (旧样子是 MCP 自己写死"只支持 http/https",把 QJSDOM_ALLOW_SCHEMES 架空)
//   ② text_len 是【字符数】且与 CLI 同一口径
//      (旧样子是 MCP 用 len() 覆盖成字节数,中文页面下差三倍)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func 入口测试配置() config {
	return config{timeout: 5 * time.Second, maxText: 60000, jsFetch: true, scriptSrc: true}
}

func 结果文本(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r == nil || len(r.Content) == 0 {
		t.Fatal("结果里没有内容块")
	}
	tc, ok := r.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("第一个内容块不是文本:%T", r.Content[0])
	}
	return tc.Text
}

func Test入口协议门默认拒放行后过(t *testing.T) {
	cfg := 入口测试配置()

	// 默认(allowSchemes 为空):file:// 必须被拒
	res, _, err := handleFetch(cfg, nil, fetchArgs{URL: "file:///etc/hostname"})
	if err != nil {
		t.Fatalf("handleFetch 返回错误: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("默认配置下 file:// 必须被拒,实际 %+v", res)
	}
	if !strings.Contains(结果文本(t, res), "协议") {
		t.Errorf("拒绝理由应当说明是协议问题,实际:%s", 结果文本(t, res))
	}

	// 显式放行之后必须真的能过这道门 —— 否则 QJSDOM_ALLOW_SCHEMES 就是摆设
	cfg.allowSchemes = []string{"file"}
	dir := t.TempDir()
	f := filepath.Join(dir, "本页.html")
	if err := os.WriteFile(f, []byte(`<!DOCTYPE html><html lang="zh"><head><title>本地</title></head><body><article><p>本地文件正文在此</p></article></body></html>`), 0o644); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}

	res2, _, err := handleFetch(cfg, nil, fetchArgs{URL: "file://" + f})
	if err != nil {
		t.Fatalf("handleFetch 返回错误: %v", err)
	}
	if res2.IsError {
		t.Fatalf("放行 file 之后不该被拒:%s", 结果文本(t, res2))
	}
	if !strings.Contains(结果文本(t, res2), "本地文件正文在此") {
		t.Errorf("没拿到本地文件正文:%q", 结果文本(t, res2))
	}
}

func Test入口的text_len是字符数(t *testing.T) {
	// 用 file:// 而不是本地 httptest:入口的 SSRF 守卫会拒掉 127.0.0.1,
	// 而这一条要测的是 text_len 的口径,不该被网络层的门挡住。
	正文 := strings.Repeat("糖酵解", 20) // 60 个汉字,180 字节
	dir := t.TempDir()
	f := filepath.Join(dir, "中文页.html")
	if err := os.WriteFile(f, []byte(`<!DOCTYPE html><html lang="zh"><head></head><body><article><p>`+正文+`</p></article></body></html>`), 0o644); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}

	cfg := 入口测试配置()
	cfg.allowSchemes = []string{"file"}
	否 := false
	res, out, err := handleFetch(cfg, nil, fetchArgs{URL: "file://" + f, Images: &否})
	if err != nil {
		t.Fatalf("handleFetch 返回错误: %v", err)
	}
	if res.IsError {
		t.Fatalf("不该出错:%s", 结果文本(t, res))
	}
	文本 := 结果文本(t, res)
	t.Logf("TextLen=%d 文本字符数=%d 文本字节数=%d", out.TextLen, utf8.RuneCountInString(文本), len(文本))

	if out.TextLen == len(文本) && len(文本) != utf8.RuneCountInString(文本) {
		t.Errorf("text_len 是字节数(等于 len(文本)=%d),应当是字符数", len(文本))
	}
	if !strings.Contains(文本, 正文) {
		t.Errorf("正文没取全:%q", 文本)
	}
	// 正文 60 个汉字:字符数口径下 TextLen 应当是 60 上下,字节口径会是 180
	if out.TextLen < 55 || out.TextLen > 70 {
		t.Errorf("text_len 应当是正文的字符数(约 60),实际 %d", out.TextLen)
	}
}
