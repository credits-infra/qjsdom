package renderer

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

var (
	staticSpace     = regexp.MustCompile(`[ \t\x{00A0}]+`)
	staticBlankLine = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// renderStatic 是不创建 qjs 运行时的提取路径。
//
// 它只做三件事:解析 HTML、取整页文本、收集链接。article 容器选择、图片收集、
// 页面脚本执行都不在这里 —— 那些能力属于 JS 路径。把静态路径做成 all 的
// 廉价实现会让同一个 mode 在两条路径上给出不同内容,所以它是一个独立的 mode,
// 有独立的契约。
//
// selector 与 DownloadImages 在静态模式下没有定义,直接拒绝而不是静默忽略:
// 调用方传了 selector 却拿到整页,是比报错更糟的失败。
func renderStatic(docHTML, base string, opt Options) (*Result, error) {
	if opt.Selector != "" {
		return nil, fmt.Errorf("mode=static 不支持 selector:它只提取整页正文")
	}
	if opt.DownloadImages {
		return nil, fmt.Errorf("mode=static 不收集图片,不能与 DownloadImages 同用")
	}

	doc, err := html.Parse(strings.NewReader(docHTML))
	if err != nil {
		return nil, fmt.Errorf("HTML 解析失败: %w", err)
	}

	title := ""
	if n := findFirst(doc, "title"); n != nil {
		title = strings.TrimSpace(staticText(n))
	}

	body := findFirst(doc, "body")
	hasBody := body != nil
	if body == nil {
		body = doc
	}

	// scriptTags 在删除节点之前统计:它回答的是"页面里有多少个 script",
	// 不是"跑了几个"。scriptsRan 永远是 0,因为这条路径没有脚本执行阶段。
	scriptTags := countTag(doc, "script")

	// 与 page_runner.js 的抽取口径一致:script/style/noscript/template 不属于正文。
	// 在解析出来的副本上删除,不动原始字符串;没有脚本会看到这份 DOM。
	removeTags(body, map[string]bool{
		"script": true, "style": true, "noscript": true, "template": true,
	})

	text := staticNorm(staticText(body))
	out := &Result{
		Title:      title,
		Text:       text,
		TextLen:    utf8.RuneCountInString(text),
		ScriptTags: scriptTags,
		ScriptsRan: 0,
		ScriptErrs: []string{},
		Links:      staticLinks(body, base),
		Images:     []string{},
		HasBody:    hasBody,
		Mode:       string(ModeStatic),
	}

	if opt.MaxTextLen > 0 {
		out.Text = truncateRunes(out.Text, opt.MaxTextLen)
		out.TextLen = utf8.RuneCountInString(out.Text)
	}

	if opt.Markdown {
		var b strings.Builder
		if err := html.Render(&b, body); err != nil {
			out.ScriptErrs = append(out.ScriptErrs,
				fmt.Sprintf("note: 静态正文序列化失败,Markdown 已退回纯文本: %v", err))
		} else {
			out.HTML = b.String()
		}
		md, err := 转Markdown(out, base)
		if err != nil {
			out.ScriptErrs = append(out.ScriptErrs,
				fmt.Sprintf("note: Markdown 转换失败,已退回纯文本: %v", err))
		} else if opt.MaxTextLen > 0 {
			out.Markdown = truncateRunes(md, opt.MaxTextLen)
		} else {
			out.Markdown = md
		}
		out.HTML = "" // 中间产物,不留在结果里
	}

	return out, nil
}

func findFirst(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findFirst(c, tag); got != nil {
			return got
		}
	}
	return nil
}

func countTag(n *html.Node, tag string) int {
	count := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			count++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return count
}

// removeTags 删除匹配的元素。遍历时先记下 next,删除当前节点后仍能继续。
func removeTags(n *html.Node, tags map[string]bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && tags[c.Data] {
			n.RemoveChild(c)
		} else {
			removeTags(c, tags)
		}
		c = next
	}
}

// staticText 取节点的文本内容,跳过 script/style/noscript/template。
// 删除已经做过一遍,这里再判一次,是为了让链接标题这类在删除之后仍被调用的
// 场景也不会带进脚本源码。
func staticText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// staticNorm 与 page_runner.js 的 normText 对齐:折叠行内空白、最多保留一个空行。
// 额外把 CRLF/CR 归一成 LF:HTML 解析器本来就这么做,显式写出来是为了让静态
// 文本的换行口径不依赖解析器的实现细节。
func staticNorm(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = staticSpace.ReplaceAllString(s, " ")
	s = staticBlankLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func staticAttr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

// staticLinks 按 page_runner.js 的 collectLinks 规则收集:
// 跳过片段与 javascript: 地址、标题短于 2 字或长于 300 字、最多 200 条。
// 相对地址保持原样(JS 路径也不在这里补全);协议相对与搜索页重定向包装
// 按 base 处理,规则与 cleanURL 一致。
func staticLinks(root *html.Node, base string) []Link {
	out := make([]Link, 0)
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := staticAttr(n, "href")
			if href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") {
				title := staticNorm(staticText(n))
				title = strings.ReplaceAll(title, "\n", " ")
				if l := utf8.RuneCountInString(title); l >= 2 && l <= 300 {
					if u := staticURL(href, base); u != "" {
						out = append(out, Link{Title: title, URL: u})
						if len(out) >= 200 {
							return false
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if !walk(c) {
				return false
			}
		}
		return true
	}
	walk(root)
	return out
}

// staticURL 处理两件 JS 侧 cleanURL 也在做的事:
//   - 协议相对地址按页面协议补全;
//   - 解开搜索结果页的重定向参数(uddg / url / q / u / target / redirect)。
//
// 属性值已经由 HTML 解析器解过实体,这里不能再解一次,否则 `&amp;amp;` 会多降一层。
func staticURL(raw, base string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		proto := "https:"
		if b, err := url.Parse(base); err == nil && b.Scheme != "" {
			proto = b.Scheme + ":"
		}
		u = proto + u
	}
	if q := strings.Index(u, "?"); q >= 0 {
		query := u[q+1:]
		for _, key := range []string{"uddg", "url", "q", "u", "target", "redirect"} {
			v, ok := queryValue(query, key)
			if !ok {
				continue
			}
			decoded, err := url.PathUnescape(v)
			if err != nil {
				decoded = v
			}
			if strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://") {
				return decoded
			}
		}
	}
	return u
}

func queryValue(query, key string) (string, bool) {
	for _, part := range strings.Split(query, "&") {
		if strings.HasPrefix(part, key+"=") {
			return part[len(key)+1:], true
		}
	}
	return "", false
}
