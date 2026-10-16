package fetcher

// 页面自己声明的字符集优先,没声明才猜。
//
// 为什么需要这一层:Go 的 html 包和我们交给 linkedom 的字符串都只吃 UTF-8,
// 而中文站点里 GBK/GB2312 仍然常见。一律按 UTF-8 解的结果是整页乱码 ——
// 实测过:同一页 GBK 文章,httpx 与 chromium 正常,qjsdom 与 happy-dom 全是乱码。
// 有意思的是这跟 chromium 的另一面是同一个问题:未声明时它按规范退回
// windows-1252,中文页面同样变乱码。两边都说明"按规范猜"和"按习惯猜"都会翻车,
// 唯一稳的做法是读页面自己说的那一个。
//
// 这一层只解码,不做有损的重新编码:解出来是什么就是什么,并把结论报给调用方 ——
// "这页是什么编码"这件事写在结果里比写在日志里有用的多。

import (
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Charset 记录这次解码采用了什么字符集、依据是什么。
// 导出是因为调用方(CLI 与 MCP)要把它写进输出 —— 正文乱码时,
// 第一件想知道的就是"它到底按什么解的"。
type Charset struct {
	// Name 是采用的字符集规范名,如 utf-8、gbk、gb18030、big5、windows-1252。
	Name string
	// Source 说明凭什么这么定,取值是 header、meta、bom 或 guessed 之一。
	// 用短码而不是自然语言:它要进 JSON 输出,调用方未必是中文读者。
	Source string
}

// 元字符集 匹配 <meta charset=x> 与 <meta http-equiv=Content-Type content="…charset=x">
// 两种写法 —— 后者的 charset 参数也在标签属性里,一条正则够用。
var 元字符集 = regexp.MustCompile(`(?i)<meta[^>]{0,400}?charset\s*=\s*["']?\s*([a-z0-9_\-]+)`)

// DecodeHTML 把响应体解成 UTF-8 文本,并给出采用的字符集。
// contentType 是响应头里的 Content-Type(可以为空,本地文件就是这么读的)。
//
// Go 的标识符以汉字开头就是未导出的,所以这里用英文名 —— 调用方要能用上它。
func DecodeHTML(b []byte, contentType string) (string, Charset) {
	if 文本, 结论, ok := 按BOM解(b); ok {
		return 文本, 结论
	}
	if 名, 来源 := 取声明(contentType, b); 名 != "" {
		if 文本, ok := 按名解码(名, b); ok {
			return 文本, Charset{Name: 名, Source: 来源}
		}
	}
	return 未声明时解码(b)
}

// 按BOM解 BOM 比任何声明都硬,而且带着 BOM 的 UTF-8 文件若不去掉,那个
// U+FEFF 会跟着进 DOM,变成正文开头的隐形字符。
func 按BOM解(b []byte) (string, Charset, bool) {
	名 := ""
	体 := b
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		名, 体 = "utf-8", b[3:]
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		名, 体 = "utf-16le", b[2:]
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		名, 体 = "utf-16be", b[2:]
	default:
		return "", Charset{}, false
	}
	文本, ok := 按名解码(名, 体)
	if !ok {
		return "", Charset{}, false
	}
	return 文本, Charset{Name: 名, Source: "bom"}, true
}

// 取声明 按 HTML 规范的优先级取字符集声明:响应头优先于 meta。
// 返回空名表示两处都没声明 —— 这时候才轮到猜。
func 取声明(contentType string, b []byte) (名, 来源 string) {
	if contentType != "" {
		if _, 参数, err := mime.ParseMediaType(contentType); err == nil {
			if cs := strings.TrimSpace(参数["charset"]); cs != "" {
				return cs, "header"
			}
		}
	}
	// 只在开头找:规范也是这么要求的,而且后面的 <meta> 大概率是正文里的字面量。
	头 := b
	if len(头) > 1024 {
		头 = 头[:1024]
	}
	if m := 元字符集.FindSubmatch(头); m != nil {
		return string(m[1]), "meta"
	}
	return "", ""
}

// 按名解码 用一个规范名去解。名字认不出来就返回 false,交给上层猜。
func 按名解码(名 string, b []byte) (string, bool) {
	enc, err := htmlindex.Get(名)
	if err != nil || enc == nil {
		return "", false
	}
	if enc == unicode.UTF8 || strings.EqualFold(名, "utf-8") || strings.EqualFold(名, "utf8") {
		// 声明 UTF-8 也可能真有坏字节:换成替换符,别把非法序列喂给 JS 引擎
		// (linkedom 与 QuickJS 对非法 UTF-8 的处理是未定义的)。
		return strings.ToValidUTF8(string(b), "\uFFFD"), true
	}
	出, _, err := transform.String(enc.NewDecoder(), string(b))
	if err != nil {
		return "", false
	}
	return 出, true
}

// 未声明时解码 头和 meta 都没说、也没有 BOM,只能猜。
//
// 猜的顺序按"猜错有多难看"排,不按规范:
//
//  1. 字节本身是合法 UTF-8 —— 直接就是答案,今天的页面绝大多数如此;
//  2. 非法 UTF-8 但字节结构像 GBK 汉字 —— 按 GB18030 解(GBK 的超集)。
//     中文老站点不声明编码是常态,而按规范退回 windows-1252 只会给出一页乱码;
//  3. 其余按 windows-1252 —— 西欧页面的通行默认。
//
// 实测对照(同一页,不声明编码):chromium 两种都猜对(它有自己的探测与地区默认),
// qjsdom / httpx / happy-dom 改前两种都塌。这一步做完,前两种我们和 chromium 齐平。
//
// 边界:未声明的 Big5 与 Shift_JIS 不在这里猜 —— 它们的双字节结构与 GBK 不重叠,
// 硬猜会把港台与日文页面解坏,不如老实退回 UTF-8/西欧默认并把结论报出去。
func 未声明时解码(b []byte) (string, Charset) {
	if utf8.Valid(b) {
		return string(b), Charset{Name: "utf-8", Source: "guessed"}
	}
	if 像GBK汉字(b) {
		if 文本, ok := 按名解码("gb18030", b); ok {
			return 文本, Charset{Name: "gb18030", Source: "guessed"}
		}
	}
	if 文本, ok := 按名解码("windows-1252", b); ok {
		return 文本, Charset{Name: "windows-1252", Source: "guessed"}
	}
	return strings.ToValidUTF8(string(b), "\uFFFD"), Charset{Name: "utf-8", Source: "guessed"}
}

// 像GBK汉字 判断这串字节像不像 GBK/GB18030 的双字节汉字。
//
// 为什么不直接看"解出来是不是汉字":GB18030 能把任意字节序列解成一片汉字,
// 西欧页面(每个重音字母一个高位字节)也能解成"像汉字的东西",那样判必然误伤。
// 结构判据硬得多:GBK 常用汉字(GB2312 一级字区,覆盖日常用字的绝大多数)是
// 首字节 0xB0-0xF7 配 尾字节 0xA1-0xFE 的两个字节成对出现;而西欧重音字母
// (如 é=0xE9)后面跟的是 ASCII 字母或空格,配不成对。
func 像GBK汉字(b []byte) bool {
	高, 成对 := 0, 0
	for i := 0; i < len(b); i++ {
		if b[i] < 0x80 {
			continue
		}
		高++
		if i+1 < len(b) && b[i] >= 0xB0 && b[i] <= 0xF7 && b[i+1] >= 0xA1 && b[i+1] <= 0xFE {
			成对 += 2
			i++
		}
	}
	// 样本太少不判(一个孤立的 0xE9 也可能碰巧成对),够多才认七成以上
	return 高 >= 4 && 成对*10 >= 高*7
}
