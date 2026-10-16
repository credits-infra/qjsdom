package renderer

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"qjsdom/internal/fetcher"
)

// 外部脚本(<script src>)的加载。
//
// 为什么在 Go 侧取回来内联,而不是让页面脚本自己 fetch:
//   - 顺序 —— 浏览器按文档顺序执行外链脚本;把源码放回原来的位置就天然保序,
//     走 JS 的异步 fetch 反倒要自己实现一套串行调度(还得处理失败的顺序语义)。
//   - 守卫 —— 请求走与抓页面完全相同的那条通路(协议白名单 + SSRF 守卫 +
//     大小/超时上限)。页面若要拿 <script src> 当绕过守卫的出口,这条路也不通。
//
// 明确不做的:async / defer(一律按文档顺序执行,拿到的顺序是确定的那一种)、
// type="module"(需要 import 解析支持,遇到就跳过并记一条)、integrity / CSP
// (没有"源"的概念)。
var 外链脚本正则 = regexp.MustCompile(
	`(?is)<script\b([^>]*?)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))([^>]*)>\s*</script\s*>`)

const (
	// 单页最多取几个外链脚本
	maxScriptSrcCount = 10
	// 单个脚本的字节上限(压缩前的源码,512KB 足够正常站点)
	maxScriptSrcBytes = 512 << 10
)

// 展开外部脚本 把所有能取到的 <script src> 替换成内联源码,返回新 HTML 与提示。
//
// 取不到的保持原样(原样意味着 runner 会跳过它,与改造前的行为一致),
// 并在提示里说明原因 —— 失败必须带原因,否则"这个页面脚本没跑"没法查。
func 展开外部脚本(html, pageURL string, opt Options) (string, []string) {
	if !strings.Contains(html, "<script") {
		return html, nil
	}

	var notes []string
	var base *url.URL
	if pageURL != "" {
		base, _ = url.Parse(pageURL)
	}

	fo := opt.FetchOptions
	if fo.UserAgent == "" {
		fo = fetcher.DefaultOptions()
	}
	// 单个外链脚本不该拖住整页
	if fo.Timeout <= 0 || fo.Timeout > 8*time.Second {
		fo.Timeout = 8 * time.Second
	}
	if fo.MaxBytes <= 0 || fo.MaxBytes > maxScriptSrcBytes {
		fo.MaxBytes = maxScriptSrcBytes
	}

	取了 := 0
	模块提示过 := false
	out := 外链脚本正则.ReplaceAllStringFunc(html, func(tag string) string {
		m := 外链脚本正则.FindStringSubmatch(tag)
		if m == nil {
			return tag
		}
		属性 := m[1] + " " + m[5]
		src := 首个非空(m[2], m[3], m[4])
		if src == "" {
			return tag
		}

		类型 := 属性值(属性, "type")
		if 是模块(类型) {
			// module 要有 import 解析,取回来当普通脚本执行是错的
			if !模块提示过 {
				notes = append(notes, "note: 跳过 type=\"module\" 的外链脚本(需要 import 支持)")
				模块提示过 = true
			}
			return tag
		}
		if 类型 != "" && !是JS类型(类型) {
			return tag // 数据块(application/json 之类),本就不是可执行脚本
		}
		// data: / javascript: 之类的地址没有"取"的意义,留给页面自己
		if strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "javascript:") {
			return tag
		}

		if 取了 >= maxScriptSrcCount {
			notes = append(notes, fmt.Sprintf(
				"note: 外链脚本超过 %d 个,其余未加载(从 %s 起)", maxScriptSrcCount, src))
			return tag
		}
		取了++

		abs := src
		if base != nil {
			if u, err := url.Parse(src); err == nil {
				abs = base.ResolveReference(u).String()
			}
		}
		res, err := fetcher.Fetch(abs, fo)
		if err != nil {
			notes = append(notes, fmt.Sprintf("note: 外链脚本取不到(%s): %v", src, err))
			return tag
		}
		// 非 2xx 一律当取不到。实测教训:404 的响应体是 html 错误页,把它当脚本内联
		// 进去执行,只会往 scriptErrs 里灌一行 "expecting ';'" 这种没头没尾的语法错,
		// 掩盖真正的原因(取不到)。
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			notes = append(notes, fmt.Sprintf("note: 外链脚本取不到(%s): HTTP %d", src, res.StatusCode))
			return tag
		}
		// 超过单脚本上限被截断的不要执行:半个脚本只会报一句没头没尾的语法错,
		// 把真正的原因(脚本太大)埋掉 —— 与"取不到"一样处理:留名、留原因、跳过。
		if res.Truncated {
			notes = append(notes, fmt.Sprintf(
				"note: 外链脚本未执行(%s): 超过单脚本上限 %d 字节,响应被截断", src, maxScriptSrcBytes))
			return tag
		}
		// 响应明确是 HTML 也不执行(错误页、登录页、反爬拦截页都长这样)。
		// 浏览器会执行非 JS 类型的脚本内容,但那是历史包袱;抓取器不需要这个行为。
		if ct := res.Header.Get("Content-Type"); 是HTML响应(ct) {
			notes = append(notes, fmt.Sprintf(
				"note: 跳过外链脚本(%s): 响应类型是 %s,不是脚本", src, ct))
			return tag
		}

		// 内联回原位置。带上来源标签:出错时 runner 会把地址写进 scriptErrs ——
		// 页面上的脚本一多,只报一句 "not a function" 根本不知道是哪一个
		// (实测:jquery 与页面自身脚本同页时,就是这么一句,查不出是谁)。
		// 标签只是给报错用的,注释只为可读;两者都不影响执行。
		return `<script data-qjsdom-from="` + 标签转义(abs) + `">/* qjsdom: 外链内联 ` +
			安全注释(abs) + " */\n" + 去尾标签(res.HTML) + "\n</script>"
	})

	return out, notes
}

// 是HTML响应 判断响应类型是不是 HTML —— 这种"脚本"其实是错误页/登录页/拦截页。
func 是HTML响应(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// 去尾标签 防止脚本源码里的 </script> 把我们的内联块提前截断。
// 拼接处的风险只在这一处,所以只在这一处处理:HTML 规范里必须转义。
func 去尾标签(code string) string {
	return strings.ReplaceAll(code, "</script", `<\/script`)
}

// 标签转义 让属性值里不会出现引号(URL 里的 " 用 %22 表达,不影响可读性,
// 也避免依赖实体解码 —— linkedom 的 getAttribute 不解码实体)。
func 标签转义(s string) string {
	return strings.ReplaceAll(s, `"`, "%22")
}

// 安全注释 让 URL 里的 */ 不会把注释提前闭合。
func 安全注释(s string) string {
	s = strings.ReplaceAll(s, "*/", "* /")
	return strings.ReplaceAll(s, "\n", " ")
}

var 属性正则 = regexp.MustCompile(`(?is)\b([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// 属性值 取标签属性值(取不到返回空串)。属性名大小写不敏感。
func 属性值(attrs, name string) string {
	for _, m := range 属性正则.FindAllStringSubmatch(attrs, -1) {
		if !strings.EqualFold(m[1], name) {
			continue
		}
		return 首个非空(m[2], m[3], m[4])
	}
	return ""
}

func 首个非空(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func 是模块(类型 string) bool {
	return strings.EqualFold(strings.TrimSpace(类型), "module")
}

// 是JS类型 判断 type 属性是不是(可执行的)JavaScript。
// 空 type 表示 JavaScript;带 charset/参数的类型名也算。
func 是JS类型(类型 string) bool {
	t := strings.ToLower(strings.TrimSpace(类型))
	t = strings.TrimSpace(strings.SplitN(t, ";", 2)[0])
	switch t {
	case "", "text/javascript", "application/javascript", "application/x-javascript",
		"text/ecmascript", "application/ecmascript", "text/jscript":
		return true
	}
	return false
}
