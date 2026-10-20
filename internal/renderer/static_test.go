package renderer

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStaticModeExtractsWholeBodyAndLinks(t *testing.T) {
	html := `<!doctype html><html><head><title>静态页</title></head><body>
<nav><a href="/a?x=1&amp;y=2">链接 A</a></nav>
<article><p>正文 &amp; 更多</p><img src="/figure.png"></article>
<script>document.body.textContent = "脚本跑了";</script>
<style>.x { color: red }</style>
<noscript>无脚本内容</noscript>
<script type="application/ld+json">{"x":1}</script>
</body></html>`

	res, err := Render(html, "https://example.com/page", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	if res.Mode != string(ModeStatic) {
		t.Errorf("mode 应当是 static,实际 %q", res.Mode)
	}
	if res.Title != "静态页" {
		t.Errorf("标题不对: %q", res.Title)
	}
	if !strings.Contains(res.Text, "正文 & 更多") {
		t.Errorf("正文缺失或没有解实体: %q", res.Text)
	}
	for _, bad := range []string{"脚本跑了", "无脚本内容", "color: red", `{"x":1}`} {
		if strings.Contains(res.Text, bad) {
			t.Errorf("正文里不该出现 %q: %q", bad, res.Text)
		}
	}
	if res.ScriptTags != 2 {
		t.Errorf("页面有 2 个 script(内联 + JSON-LD),实际 %d", res.ScriptTags)
	}
	if res.ScriptsRan != 0 {
		t.Errorf("静态模式不该执行脚本,scriptsRan=%d", res.ScriptsRan)
	}
	if len(res.Images) != 0 {
		t.Errorf("静态模式不收集图片,实际 %v", res.Images)
	}
	if len(res.Links) != 1 || res.Links[0].URL != "/a?x=1&y=2" {
		t.Errorf("链接不对: %+v", res.Links)
	}
	if res.TextLen != utf8.RuneCountInString(res.Text) {
		t.Errorf("textLen 口径不一致: %d vs %d", res.TextLen, utf8.RuneCountInString(res.Text))
	}
	if !res.HasBody {
		t.Error("这份 HTML 有 body,hasBody 不该是 false")
	}
}

func TestStaticModeCleansLinks(t *testing.T) {
	html := `<body>
<a href="//example.com/x">协议相对</a>
<a href="https://duckduckgo.com/?q=go&amp;uddg=https%3A%2F%2Fexample.com%2Fhit">重定向</a>
<a href="#frag">片段</a>
<a href="javascript:void(0)">脚本链接</a>
<a href="/short">普通链接</a>
</body>`

	res, err := Render(html, "https://site.example/page", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	想要 := []string{
		"https://example.com/x",
		"https://example.com/hit",
		"/short",
	}
	if len(res.Links) != len(想要) {
		t.Fatalf("应当收集 %d 条链接,实际 %d: %+v", len(想要), len(res.Links), res.Links)
	}
	for i, w := range 想要 {
		if res.Links[i].URL != w {
			t.Errorf("第 %d 条链接应当是 %q,实际 %q", i+1, w, res.Links[i].URL)
		}
	}
}

func TestStaticModeUsesBaseForProtocolRelativeLinks(t *testing.T) {
	html := `<html><head><base href="https://cdn.example/dir/"></head><body>
<a href="//example.com/x">协议相对</a>
</body></html>`

	res, err := Render(html, "http://site.example/page", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	if len(res.Links) != 1 || res.Links[0].URL != "https://example.com/x" {
		t.Errorf("协议相对地址应当按 <base href> 的 https 补全,实际 %+v", res.Links)
	}
}

func TestStaticModeCapsLinksAt200(t *testing.T) {
	var b strings.Builder
	b.WriteString("<body>")
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, `<a href="/p%d">链接 %d</a>`, i, i)
	}
	b.WriteString("</body>")

	res, err := Render(b.String(), "https://example.com/", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	if len(res.Links) != 200 {
		t.Errorf("链接应当封顶 200 条,实际 %d", len(res.Links))
	}
}

func TestStaticModeRejectsSelectorAndImageDownload(t *testing.T) {
	html := `<body><p>正文</p></body>`
	if _, err := Render(html, "https://example.com/", Options{Mode: ModeStatic, Selector: "#c"}); err == nil {
		t.Error("mode=static + selector 应当报错,不能静默返回整页")
	}
	if _, err := Render(html, "https://example.com/", Options{Mode: ModeStatic, DownloadImages: true}); err == nil {
		t.Error("mode=static + DownloadImages 应当报错,不能静默返回空图片")
	}
}

func TestStaticModeSupportsMarkdown(t *testing.T) {
	html := `<html><body><h1>标题</h1><p>正文 <strong>加粗</strong></p>
<script>document.body.textContent = "脚本跑了";</script></body></html>`

	res, err := Render(html, "https://example.com/", Options{Mode: ModeStatic, Markdown: true})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	if !strings.Contains(res.Markdown, "# 标题") {
		t.Errorf("Markdown 缺标题: %q", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "**加粗**") {
		t.Errorf("Markdown 缺加粗: %q", res.Markdown)
	}
	if strings.Contains(res.Markdown, "脚本跑了") {
		t.Errorf("Markdown 里混进了脚本源码: %q", res.Markdown)
	}
	if res.HTML != "" {
		t.Errorf("HTML 是中间产物,不该留在结果里: %q", res.HTML)
	}
}

func TestStaticModeTruncatesText(t *testing.T) {
	res, err := Render(`<body><p>abcdef</p></body>`, "https://example.com/",
		Options{Mode: ModeStatic, MaxTextLen: 3})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	if !strings.HasPrefix(res.Text, "abc") || strings.Contains(res.Text, "abcdef") {
		t.Errorf("文本没有按 3 字截断: %q", res.Text)
	}
	if res.TextLen != utf8.RuneCountInString(res.Text) {
		t.Errorf("截断后 textLen 口径不一致: %d vs %d", res.TextLen, utf8.RuneCountInString(res.Text))
	}
}

func TestStaticModeDoesNotFetchExternalScripts(t *testing.T) {
	srv, 见过 := 记录服务(t, map[string]string{
		"/app.js": `document.body.textContent = "外链脚本跑过了";`,
	})
	html := `<body><p>正文</p><script src="/app.js"></script></body>`

	res, err := Render(html, srv.URL+"/page", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	if got := 见过(); len(got) != 0 {
		t.Errorf("静态模式不该取外链脚本,实际请求 %v", got)
	}
	if res.ScriptTags != 1 {
		t.Errorf("scriptTags 应当照实统计,实际 %d", res.ScriptTags)
	}
	if strings.Contains(res.Text, "外链脚本跑过了") {
		t.Errorf("外链脚本被送进了正文: %q", res.Text)
	}
}

func TestParseModeStatic(t *testing.T) {
	for _, in := range []string{"static", "STATIC", " static "} {
		if got := ParseMode(in); got != ModeStatic {
			t.Errorf("ParseMode(%q) = %q,想要 static", in, got)
		}
	}
	if got := ParseMode(""); got != ModeAll {
		t.Errorf("ParseMode(\"\") = %q,想要 all", got)
	}
	if got := ParseMode("nonsense"); got != ModeAll {
		t.Errorf("未知 mode 应当回落 all,实际 %q", got)
	}
}

func TestStaticModeEmptyHTML(t *testing.T) {
	res, err := Render("", "", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("空 HTML 不该报错: %v", err)
	}
	if res.Text != "" || res.Title != "" {
		t.Errorf("空 HTML 应当返回空文本,实际 text=%q title=%q", res.Text, res.Title)
	}
	if res.Mode != string(ModeStatic) {
		t.Errorf("mode 应当是 static,实际 %q", res.Mode)
	}
}
