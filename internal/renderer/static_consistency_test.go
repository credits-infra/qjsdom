package renderer

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// renderBothLinks 用同一份 HTML 分别走静态路径与 JS 路径,返回两边的链接。
//
// 这个测试只比较链接提取这一个契约。静态路径不执行脚本,所以 fixture 必须是
// 服务端已经渲染好的页面;脚本新增/删除的链接不在"两条路径应当一致"的范围内。
func renderBothLinks(t *testing.T, html string) ([]Link, []Link) {
	t.Helper()
	const pageURL = "https://site.example/page"

	static, err := Render(html, pageURL, Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	js, err := Render(html, pageURL, Options{Mode: ModeLinks, DisableImages: true})
	if err != nil {
		t.Fatalf("JS 渲染失败: %v", err)
	}
	return static.Links, js.Links
}

// TestStaticLinksMatchJSLinks 钉住 staticLinks 与 page_runner.js 的 collectLinks
// 在同一份 HTML 上返回相同的链接。
//
// 两条路径是两份实现,规则现在一样,但以后改了一边很容易忘记另一边。这个
// 一致性测试就是那条警报:标题长度边界、重定向解包、协议相对、<base>、常见
// 实体、CRLF、Unicode 标题、重复/顺序,全放在同一份 fixture 里。
func TestStaticLinksMatchJSLinks(t *testing.T) {
	longTitle := strings.Repeat("字", 300)
	tooLongTitle := strings.Repeat("字", 301)
	crlfTitle := "第一行\r\n第二行"

	html := fmt.Sprintf(`<!doctype html><html><head>
<base href="https://cdn.example/dir/">
<title>links</title>
</head><body>
<nav>
<a href="/a?x=1&amp;y=2">链接 A</a>
<a href="//example.com/x">协议相对</a>
<a href="https://duckduckgo.com/?q=go&amp;uddg=https%%3A%%2F%%2Fexample.com%%2Fhit">重定向</a>
</nav>
<article>
<a href="#frag">片段</a>
<a href="javascript:void(0)">脚本链接</a>
<a href="/nested"><span>嵌套</span>链接</a>
<a href="/two">AB</a>
<a href="/one">A</a>
<a href="/long">%s</a>
<a href="/toolong">%s</a>
<a href="/ent">A &amp; B</a>
<a href="/emoji">😀</a>
<a href="/crlf">%s</a>
<a href="/dup">重复</a>
<a href="/dup">重复</a>
</article>
</body></html>`, longTitle, tooLongTitle, crlfTitle)

	static, js := renderBothLinks(t, html)
	if !reflect.DeepEqual(static, js) {
		t.Fatalf("静态与 JS 的链接不一致\nstatic(%d)=%+v\njs(%d)=%+v", len(static), static, len(js), js)
	}
}

// TestStaticLinksMatchJSLinksAtCap 单独钉住 200 条封顶:主 fixture 里的链接
// 不够多,改错封顶值不会触发上面的测试。
func TestStaticLinksMatchJSLinksAtCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("<body>")
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, `<a href="/p%d">链接 %03d</a>`, i, i)
	}
	b.WriteString("</body>")

	static, js := renderBothLinks(t, b.String())
	if len(static) != 200 || len(js) != 200 {
		t.Fatalf("两边都应当封顶 200 条,实际 static=%d js=%d", len(static), len(js))
	}
	if !reflect.DeepEqual(static, js) {
		t.Fatalf("封顶处的链接不一致\nstatic=%+v\njs=%+v", static, js)
	}
}

// TestRenderTextNormalizesCRLF 钉住 normText 的 CRLF 归一:静态路径的 HTML
// 解析器本来就会把 CRLF 归一成 LF,JS 路径的 textContent 保留原文。不归一
// 的话,同一页的 textLen 会被换行符放大,链接标题也会多一个 \r。
func TestRenderTextNormalizesCRLF(t *testing.T) {
	const html = "<!doctype html><html><body><p>第一行\r\n第二行</p></body></html>"
	for _, mode := range []Mode{ModeAll, ModeStatic} {
		res, err := Render(html, "https://example.com/", Options{Mode: mode, DisableImages: true})
		if err != nil {
			t.Fatalf("mode=%s 渲染失败: %v", mode, err)
		}
		if strings.Contains(res.Text, "\r") {
			t.Errorf("mode=%s 的正文仍含 CR: %q", mode, res.Text)
		}
		if res.Text != "第一行\n第二行" {
			t.Errorf("mode=%s 的正文=%q,想要 %q", mode, res.Text, "第一行\n第二行")
		}
	}
}

// TestStaticLinksMatchJSLinksWithoutBase 钉住没有 base / 没有文档地址时的
// 协议相对默认值。主 fixture 的 <base> 是 https,会掩盖"默认协议"这一格;
// 这里 base 为空,两条路径都必须回落到 https。
func TestStaticLinksMatchJSLinksWithoutBase(t *testing.T) {
	const html = `<body><a href="//example.com/x">协议相对</a></body>`

	static, err := Render(html, "", Options{Mode: ModeStatic})
	if err != nil {
		t.Fatalf("静态渲染失败: %v", err)
	}
	js, err := Render(html, "", Options{Mode: ModeLinks, DisableImages: true})
	if err != nil {
		t.Fatalf("JS 渲染失败: %v", err)
	}
	if !reflect.DeepEqual(static.Links, js.Links) {
		t.Fatalf("无 base 时链接不一致\nstatic=%+v\njs=%+v", static.Links, js.Links)
	}
	if len(static.Links) != 1 || static.Links[0].URL != "https://example.com/x" {
		t.Errorf("无 base 时协议相对地址应当补 https,实际 %+v", static.Links)
	}
}

// TestStaticLinksMatchJSLinksOnWikipediaFixture 用仓库里的真实维基页再比一次。
// 合成 fixture 覆盖规则边界,这一条覆盖真实页面的实体、相对地址、链接密度和
// 200 条封顶。它是服务端渲染的页面,脚本不会新增链接,所以两条路径应当一致。
func TestStaticLinksMatchJSLinksOnWikipediaFixture(t *testing.T) {
	html, err := os.ReadFile("../../testdata/wiki.html")
	if err != nil {
		t.Fatalf("读取 wiki fixture 失败: %v", err)
	}
	static, js := renderBothLinks(t, string(html))
	if !reflect.DeepEqual(static, js) {
		t.Fatalf("真实页面的链接不一致\nstatic(%d)=%+v\njs(%d)=%+v", len(static), static, len(js), js)
	}
	if len(static) != 200 {
		t.Errorf("wiki fixture 应当触发 200 条封顶,实际 %d", len(static))
	}
}
