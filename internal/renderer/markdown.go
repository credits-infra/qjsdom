package renderer

import (
	"fmt"
	"strings"

	"qjsdom/internal/markdown"
)

// 转Markdown 把渲染结果转成 Markdown。
//
// 转换交给现成的库(internal/markdown),不在 JS 侧另写一份规则 ——
// HTML→Markdown 的边界(嵌套列表、表格、词中间的行内元素、转义)手写一遍
// 就会一条条还回来。
//
// links 模式是唯一的特例:它没有"正文容器"(那种页面要的就是链接列表),
// 所以按链接列表生成 —— 与纯文本模式下 links 模式的语义保持一致,否则
// 同一个 mode 在两种输出里会给出完全不同的东西。
func 转Markdown(out *Result, pageURL string) (string, error) {
	if out.Mode == string(ModeLinks) {
		return 链接列表Markdown(out.Links), nil
	}
	if strings.TrimSpace(out.HTML) == "" {
		// 拿不到容器 HTML(例如 DOM 解析降级)时退回纯文本,宁可少一层结构,
		// 也不要给出一份空白的 Markdown。
		return out.Text, nil
	}
	return markdown.Convert(out.HTML, pageURL)
}

// 链接列表Markdown 把 links 模式的结果列成 Markdown 列表。
func 链接列表Markdown(links []Link) string {
	var b strings.Builder
	for _, l := range links {
		if l.URL == "" {
			continue
		}
		标题 := 转义Markdown文本(strings.TrimSpace(l.Title))
		if 标题 == "" {
			标题 = l.URL
		}
		fmt.Fprintf(&b, "- [%s](%s)\n", 标题, 转义Markdown链接(l.URL))
	}
	return strings.TrimRight(b.String(), "\n")
}

// 转义Markdown文本 处理链接文本里会破坏语法的字符。
func 转义Markdown文本(s string) string {
	return strings.NewReplacer(
		`\`, `\\`, `[`, `\[`, `]`, `\]`, "\n", " ", "\r", " ",
	).Replace(s)
}

// 转义Markdown链接 让地址在任何情况下都不破坏 "(...)" 的边界。
//
// 地址里出现空格或括号时(少见但合法)Markdown 会提前结束链接,
// 用尖括号包起来是规范里的写法。
func 转义Markdown链接(u string) string {
	if strings.ContainsAny(u, " ()<>") {
		return "<" + strings.ReplaceAll(u, ">", "%3E") + ">"
	}
	return u
}
