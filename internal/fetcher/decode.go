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
	// Source 说明凭什么这么定:响应头 / meta / BOM / 未声明(按 UTF-8) / 推断。
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
	return 文本, Charset{Name: 名, Source: "BOM"}, true
}

// 取声明 按 HTML 规范的优先级取字符集声明:响应头优先于 meta。
// 返回空名表示两处都没声明 —— 这时候才轮到猜。
func 取声明(contentType string, b []byte) (名, 来源 string) {
	if contentType != "" {
		if _, 参数, err := mime.ParseMediaType(contentType); err == nil {
			if cs := strings.TrimSpace(参数["charset"]); cs != "" {
				return cs, "响应头"
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

// 未声明时解码 没声明就猜。这一步在下一个提交里补全试探顺序。
func 未声明时解码(b []byte) (string, Charset) {
	return strings.ToValidUTF8(string(b), "\uFFFD"), Charset{Name: "utf-8", Source: "未声明(按 UTF-8)"}
}
