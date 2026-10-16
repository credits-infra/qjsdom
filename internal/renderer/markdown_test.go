package renderer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const markdown夹具 = `<!DOCTYPE html>
<html lang="zh"><head><title>Markdown 测试</title>
<style>body{color:red}</style></head><body>
  <header><img src="https://example.com/logo.png" alt="站点 LOGO"></header>
  <article id="content">
    <h1>正文标题</h1>
    <p>%s</p>
    <p>这里有 <b>粗体</b>、<a href="/rel">相对链接</a> 和 <code>代码</code>。</p>
    <table><thead><tr><th>列A</th><th>列B</th></tr></thead>
           <tbody><tr><td>1</td><td>2</td></tr></tbody></table>
    <script>var secret = "脚本内容不该出现";</script>
  </article>
  <aside class="sidebar"><p>侧栏内容不该出现</p></aside>
</body></html>`

func 渲染Markdown(t *testing.T, opt Options) *Result {
	t.Helper()
	opt.DisableImages = true
	opt.Markdown = true
	res, err := Render(strings.Replace(markdown夹具, "%s", 长段落, 1),
		"https://example.com/page", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	return res
}

// TestMarkdownKeepsStructureAndContainer 钉住两件事:
// ① Markdown 保留了结构(否则退回纯文本就够了);
// ② 它取的是与纯文本同一个正文容器 —— 页眉 LOGO、侧栏、脚本都不该进来。
func TestMarkdownKeepsStructureAndContainer(t *testing.T) {
	res := 渲染Markdown(t, Options{Mode: ModeArticle, Selector: "#content"})
	md := res.Markdown
	if md == "" {
		t.Fatal("没有产出 Markdown")
	}
	for _, 想要 := range []string{
		"# 正文标题",
		"**粗体**",
		"[相对链接](https://example.com/rel)", // 相对链接补成绝对(WithDomain)
		"`代码`",
		"| 列A | 列B |",
	} {
		if !strings.Contains(md, 想要) {
			t.Errorf("Markdown 里缺少 %q\n完整输出:\n%s", 想要, md)
		}
	}
	for _, 不该有 := range []string{"站点 LOGO", "侧栏内容不该出现", "脚本内容不该出现", "color:red"} {
		if strings.Contains(md, 不该有) {
			t.Errorf("Markdown 里混进了 %q(应当只取正文容器)\n%s", 不该有, md)
		}
	}
}

// TestMarkdownLinksModeIsAList 钉住 links 模式的语义一致:
// 纯文本模式下列的是"标题 + 地址"的清单,Markdown 也必须是清单,
// 不能变成整页 Markdown(那是 all 模式该给的)。
func TestMarkdownLinksModeIsAList(t *testing.T) {
	res := 渲染Markdown(t, Options{Mode: ModeLinks})
	if res.Markdown == "" {
		t.Fatal("links 模式没有产出 Markdown")
	}
	for _, 行 := range strings.Split(res.Markdown, "\n") {
		if 行 == "" {
			continue
		}
		if !strings.HasPrefix(行, "- [") {
			t.Errorf("links 模式的每一行都该是 Markdown 列表项,实际: %q", 行)
		}
	}
}

// TestMarkdownSharesTextTruncation 钉住截断口径一致:同一个 MaxTextLen
// 对 Markdown 也按【字符】生效,且不切出非法 UTF-8(与纯文本同一套规则)。
func TestMarkdownSharesTextTruncation(t *testing.T) {
	res := 渲染Markdown(t, Options{Mode: ModeArticle, Selector: "#content", MaxTextLen: 12})
	if !utf8.ValidString(res.Markdown) {
		t.Fatalf("截断后的 Markdown 不是合法 UTF-8: %q", res.Markdown)
	}
	正文 := strings.TrimSuffix(res.Markdown, "…(已截断)")
	if n := len([]rune(正文)); n != 12 {
		t.Errorf("想留 12 个字符,实际 %d 个: %q", n, 正文)
	}
	if !strings.Contains(res.Markdown, "已截断") {
		t.Errorf("截断了却没有提示: %q", res.Markdown)
	}
}

// TestMarkdownIsOptIn 钉住"按需":没要 Markdown 时既不算、也不把中间
// 产物(容器 HTML)留在结果里 —— 否则 -json 输出会凭空大一大截。
func TestMarkdownIsOptIn(t *testing.T) {
	res, err := Render(strings.Replace(markdown夹具, "%s", 长段落, 1),
		"https://example.com/page", Options{Mode: ModeArticle, Selector: "#content", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if res.Markdown != "" {
		t.Error("没开 Markdown 却产出了 Markdown")
	}
	if res.HTML != "" {
		t.Errorf("没开 Markdown 却回传了容器 HTML(%d 字节)", len(res.HTML))
	}

	// 开了 Markdown 之后,HTML 只作中间产物,不留在最终结果里
	res2 := 渲染Markdown(t, Options{Mode: ModeArticle, Selector: "#content"})
	if res2.HTML != "" {
		t.Errorf("转换完成后容器 HTML 应当清空,实际还有 %d 字节", len(res2.HTML))
	}
}
