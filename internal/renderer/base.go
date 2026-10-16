package renderer

import (
	"net/url"
	"regexp"
	"strings"
)

// 正则base标签 匹配 <base ...> 的开始标签(大小写不敏感)。
// 与 外链脚本正则 一样,属性值里出现 `>` 会提前收尾 —— 这种写法在真实站点里不出现,
// 而为此写一个完整解析器不值得。
var 正则base标签 = regexp.MustCompile(`(?is)<base\b([^>]*)>`)

// 注释与脚本体 做标签扫描前先摘掉它们。
//
// 为什么必须摘:扫的是原始文本,而注释里或 JS 字符串里的 `<base>` 也是文本。
// 第四轮交叉评审实测:`<!-- <base href="https://假.example/"> -->` 会被当真标签,
// 于是整页的相对地址(外链脚本、页面脚本的 fetch、图片)全被改到那个主机上 ——
// 一句注释就能劫持整页的地址解析。浏览器的解析器不会这么做,我们也不该。
//
// 注意:Go 的正则(RE2)不支持反向引用,所以收尾标签只能再写一遍候选名,
// 不能用 `\1`。配对不严的代价是"少见的畸形嵌套可能多摘一点文本",对这里的用途
// (只为了不把注释/代码里的 `<base>` 当真元素)可以接受。
var (
	正则注释  = regexp.MustCompile(`(?is)<!--.*?-->`)
	正则脚本体 = regexp.MustCompile(`(?is)<(?:script|style|textarea|title)\b[^>]*>.*?</(?:script|style|textarea|title)\s*>`)
)

// 有效基准 返回补全相对地址时要用的基地址。
//
// 浏览器不是拿文档地址直接当基准的:页面里第一个带 href 的 <base> 会把它顶掉
// (HTML 规范:文档基准地址 = 树序中第一个带 href 的 base 元素的 frozen base URL,
// 与它在 head 还是 body 无关)。忽略它的后果实测过 —— `<base href="/zh/wiki/">`
// 的页面里,相对地址会被补到站点根目录上,拿回一堆 404 而正文是空的。
//
// 返回空串表示"没有可用的基准"(既没有文档地址,也没有可解析的 <base>),
// 调用方据此跳过补全。注意 location.href 仍然是文档地址,不受 <base> 影响 ——
// 浏览器就是这么分的:一个管"我是谁",一个管"相对地址相对谁"。
func 有效基准(html, pageURL string) string {
	href := 抓baseHref(html)
	if href == "" {
		return pageURL
	}
	if pageURL == "" {
		// 没有文档地址可依:本来就是绝对地址的可以直接用
		return href
	}
	文档地址, err := url.Parse(pageURL)
	if err != nil {
		return pageURL
	}
	基准, err := url.Parse(href)
	if err != nil {
		return pageURL
	}
	// <base href> 自己也可以是相对地址,按文档地址补全(浏览器同样如此)
	return 文档地址.ResolveReference(基准).String()
}

// 抓baseHref 取第一个带 href 的 <base> 的 href 值。href 为空的跳过 —— 规范里
// 「有 href 属性」才算,而 `<base target="_blank">` 这种没有 href 的不该顶掉基准。
func 抓baseHref(html string) string {
	// 先摘掉注释与脚本体:里面的 `<base>` 只是文本,不是元素。
	干净 := 正则注释.ReplaceAllString(html, " ")
	干净 = 正则脚本体.ReplaceAllString(干净, " ")
	for _, m := range 正则base标签.FindAllStringSubmatch(干净, -1) {
		if v := strings.TrimSpace(属性值(m[1], "href")); v != "" {
			return 解实体(v)
		}
	}
	return ""
}

// 实体表 还原属性值里常见的 HTML 实体。浏览器会把属性值解实体,而我们用正则读的是
// 原文:`<base href="/s?a=1&amp;b=2">` 不解实体就会得到一条带 `&amp;` 的错地址。
var 实体表 = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">",
	"&quot;", `"`, "&#39;", "'", "&#x27;", "'",
)

func 解实体(s string) string { return 实体表.Replace(s) }
