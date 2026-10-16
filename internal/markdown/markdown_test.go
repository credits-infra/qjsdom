package markdown

import (
	"strings"
	"testing"
)

// TestConvertKeepsStructure 钉住结构转换:标题、粗体、链接、列表(含嵌套)、
// 表格、引用、代码块、图片 —— 这些是"正文可读"的全部理由,少一条就等于退回
// 纯文本。期望值来自实测输出。
func TestConvertKeepsStructure(t *testing.T) {
	html := `<article>
  <h1>标题一</h1>
  <p>正文里有 <b>粗体</b> 和 <a href="https://example.com/x">链接</a>,还有 <code>code</code>。</p>
  <ul><li>第一项</li><li>第二项<ul><li>嵌套项</li></ul></li></ul>
  <table><thead><tr><th>列A</th><th>列B</th></tr></thead>
         <tbody><tr><td>1</td><td>2</td></tr></tbody></table>
  <blockquote><p>引用一段话</p></blockquote>
  <pre><code>fmt.Println("hi")</code></pre>
  <img src="https://example.com/figure.png" alt="插图">
</article>`

	got, err := Convert(html, "https://example.com/page")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	for _, 想要 := range []string{
		"# 标题一",
		"**粗体**",
		"[链接](https://example.com/x)",
		"`code`",
		"- 第一项",
		"- 嵌套项",
		"| 列A | 列B |",
		"> 引用一段话",
		"```",
		"![插图](https://example.com/figure.png)",
	} {
		if !strings.Contains(got, 想要) {
			t.Errorf("Markdown 里缺少 %q\n完整输出:\n%s", 想要, got)
		}
	}
}

// TestConvertMakesLinksAbsolute 钉住相对链接会被补全。
//
// 模型拿到 /wiki/糖酵解 这种地址无法自己解析 —— 它没有"当前页面"的概念。
func TestConvertMakesLinksAbsolute(t *testing.T) {
	html := `<p><a href="/wiki/x">相对</a> 与 <a href="https://other.example/y">绝对</a></p>`
	got, err := Convert(html, "https://zh.wikipedia.org/wiki/糖酵解")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if !strings.Contains(got, "https://zh.wikipedia.org/wiki/x") {
		t.Errorf("相对链接没被补成绝对: %s", got)
	}
	if !strings.Contains(got, "https://other.example/y") {
		t.Errorf("别的域的绝对链接被改坏了: %s", got)
	}

	// 不给 pageURL 时保持原样(调用方可能根本没有页面地址)
	got, err = Convert(html, "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if !strings.Contains(got, "(/wiki/x)") {
		t.Errorf("没有 pageURL 时不该改动相对链接: %s", got)
	}
}

// TestConvertDropsScriptAndStyle 钉住脚本与样式不会漏进正文。
func TestConvertDropsScriptAndStyle(t *testing.T) {
	html := `<html><head><style>body{color:red}</style></head>
<body><p>正文</p><script>var secret = "不该出现";</script></body></html>`
	got, err := Convert(html, "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if strings.Contains(got, "不该出现") || strings.Contains(got, "color:red") {
		t.Errorf("脚本或样式漏进了 Markdown: %s", got)
	}
	if !strings.Contains(got, "正文") {
		t.Errorf("正文丢了: %s", got)
	}
}

// TestConvertEmptyAndPlainText 覆盖边界:空输入、纯文本、没有 pageURL。
func TestConvertEmptyAndPlainText(t *testing.T) {
	got, err := Convert("", "https://example.com")
	if err != nil || got != "" {
		t.Errorf("空输入应当返回空串且不报错,实际 (%q, %v)", got, err)
	}
	got, err = Convert("   \n  ", "")
	if err != nil || got != "" {
		t.Errorf("纯空白应当返回空串且不报错,实际 (%q, %v)", got, err)
	}
	got, err = Convert("<p>就是一句话</p>", "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if strings.TrimSpace(got) != "就是一句话" {
		t.Errorf("纯文本被改动了: %q", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("尾部不该留空行: %q", got)
	}
}
