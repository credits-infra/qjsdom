// Package markdown 把渲染后的 HTML 转成 Markdown。
//
// 为什么用现成的库而不是自己写:HTML→Markdown 是"一堆边界情况的总和"——
// 嵌套列表、表格、代码块里的反引号、行内元素夹在词中间、需要转义的特殊字符、
// 空链接……每一条都要单独试出来。手写版不可能比一个专门做这件事、带测试和
// 文档的库更准,而这里的目标是给模型一份可读的正文,不是维护转换器。
//
// 选 github.com/JohannesKaufmann/html-to-markdown/v2 的理由:
//   - 纯 Go、无 CGO,依赖只有它自己的 dom 包与 golang.org/x/net;
//   - 插件式:表格要显式开 table 插件,正好符合"不默认给出我们不想要的语法";
//   - 支持 WithDomain,能把相对链接补成绝对链接 —— 模型拿到 /wiki/x
//     这种地址没法自己解析。
package markdown

import (
	"fmt"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)

// 转换器 是进程级单例:构造时要注册插件,不该按次付这个成本。
// 转换本身无状态(库的 Converter 文档写明可并发使用)。
var 转换器 = converter.NewConverter(converter.WithPlugins(
	// base 是其它插件的前置:库会在缺它时报
	// "you registered the ... plugin but the base plugin is also required"。
	base.NewBasePlugin(),
	// ATX 标题(#)、- 列表、``` 代码块 —— 模型与常见渲染器都最认这一套。
	commonmark.NewCommonmarkPlugin(),
	// 表格默认不转(GFM 扩展语法);正文里的数据表往往是重点,显式打开。
	table.NewTablePlugin(
		table.WithHeaderPromotion(true), // <th> 缺失时用首行当表头
		table.WithSkipEmptyRows(true),
	),
))

// Convert 把 HTML 转成 Markdown。
//
// pageURL 用于把相对链接补成绝对链接(传空则保持原样)。不做截断 ——
// 长度限制由调用方按自己的口径施加(renderer 的 MaxTextLen 走同一套字符计数)。
func Convert(htmlInput, pageURL string) (string, error) {
	if strings.TrimSpace(htmlInput) == "" {
		return "", nil
	}
	opts := []converter.ConvertOptionFunc{}
	if pageURL != "" {
		opts = append(opts, converter.WithDomain(pageURL))
	}
	out, err := 转换器.ConvertString(htmlInput, opts...)
	if err != nil {
		return "", fmt.Errorf("HTML 转 Markdown 失败: %w", err)
	}
	// 去掉尾部空行:调用方要在后面接自己的段落(图片清单等),留一堆空行很难看。
	return strings.TrimRight(out, "\n"), nil
}
