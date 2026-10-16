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
	// ⚠️ 早退必须用同一个正则(它是 (?is) 的),不能用 strings.Contains(html, "<script"):
	// HTML 解析器对标签名不区分大小写,而小写包含判断会让写成 <SCRIPT src=...> /
	// <ScRiPt src=...> 的页面【整个外链脚本能力静默失效】—— 既不内联、连一条 note 都没有。
	// 实测后果(第三轮复核):① 12 个 <ScRiPt src> 时,超过数量上限的第 11、12 个照跑
	// (正文 n=12,而文档承诺最多 10 个);② <SCRIPT SRC="x.js"> 完全不执行也不报错,
	// 而属性名本身大小写不敏感(属性值 用 EqualFold),本该正常执行。
	if !外链脚本正则.MatchString(html) {
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
			return 标记跳过(tag, "empty-src")
		}

		类型 := 属性值(属性, "type")
		if 是模块(类型) {
			// module 要有 import 解析,取回来当普通脚本执行是错的
			if !模块提示过 {
				notes = append(notes, "note: 跳过 type=\"module\" 的外链脚本(需要 import 支持)")
				模块提示过 = true
			}
			return 标记跳过(tag, "module")
		}
		if 类型 != "" && !是JS类型(类型) {
			// 数据块(application/json 之类)本就不是可执行脚本
			return 标记跳过(tag, "not-js-type")
		}
		// data: / javascript: 之类的地址没有"取"的意义,留给页面自己
		if strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "javascript:") {
			return 标记跳过(tag, "inline-scheme")
		}

		if 取了 >= maxScriptSrcCount {
			notes = append(notes, fmt.Sprintf(
				"note: 外链脚本超过 %d 个,其余未加载(从 %s 起)", maxScriptSrcCount, src))
			return 标记跳过(tag, "over-count")
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
			return 标记跳过(tag, "fetch-failed")
		}
		// 非 2xx 一律当取不到。实测教训:404 的响应体是 html 错误页,把它当脚本内联
		// 进去执行,只会往 scriptErrs 里灌一行 "expecting ';'" 这种没头没尾的语法错,
		// 掩盖真正的原因(取不到)。
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			notes = append(notes, fmt.Sprintf("note: 外链脚本取不到(%s): HTTP %d", src, res.StatusCode))
			return 标记跳过(tag, "http-status")
		}
		// 超过单脚本上限被截断的不要执行:半个脚本只会报一句没头没尾的语法错,
		// 把真正的原因(脚本太大)埋掉 —— 与"取不到"一样处理:留名、留原因、跳过。
		if res.Truncated {
			notes = append(notes, fmt.Sprintf(
				"note: 外链脚本未执行(%s): 超过单脚本上限 %d 字节,响应被截断", src, maxScriptSrcBytes))
			return 标记跳过(tag, "truncated")
		}
		// 响应明确是 HTML 也不执行(错误页、登录页、反爬拦截页都长这样)。
		// 浏览器会执行非 JS 类型的脚本内容,但那是历史包袱;抓取器不需要这个行为。
		if ct := res.Header.Get("Content-Type"); 是HTML响应(ct) {
			notes = append(notes, fmt.Sprintf(
				"note: 跳过外链脚本(%s): 响应类型是 %s,不是脚本", src, ct))
			return 标记跳过(tag, "text-html")
		}

		// 内联回原位置。带上来源标签:出错时 runner 会把地址写进 scriptErrs ——
		// 页面上的脚本一多,只报一句 "not a function" 根本不知道是哪一个
		// (实测:jquery 与页面自身脚本同页时,就是这么一句,查不出是谁)。
		// 标签只是给报错用的,注释只为可读;两者都不影响执行。
		return `<script data-qjsdom-from="` + 标签转义(abs) + `">/* qjsdom: 外链内联 ` +
			安全注释(abs) + " */\n" + 防截断(res.HTML) + "\n</script>"
	})

	return out, notes
}

// 是HTML响应 判断响应类型是不是 HTML —— 这种"脚本"其实是错误页/登录页/拦截页。
func 是HTML响应(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// 正则结束标签 匹配可能提前结束 <script> 元素的序列。
// ⚠️ 必须大小写不敏感:HTML 解析器对 `</script` 不区分大小写,而原先只替换小写
// 形态 —— 实测源码里出现 `</SCRIPT>` 时内联块被提前截断,后面的代码漏成正文,
// 脚本本身报 "unexpected end of string"(第三轮交叉评审 D3)。
var 正则结束标签 = regexp.MustCompile(`(?i)</script`)

// 防截断 把可能提前结束 <script> 元素的 `</script` 换成 `<\/script`(JS 里等价,
// HTML 解析器不再认它)。脚本源码与内联注释里的 URL 都要过这一道 —— 两者都来自
// 页面,都可能被构造成带结束标签。
func 防截断(code string) string {
	return 正则结束标签.ReplaceAllString(code, `<\/script`)
}

// 正则标签头 定位 <script 标签名的结尾,供 标记跳过 插属性用。
// ⚠️ 必须大小写不敏感:标签匹配是 (?is) 的,页面写 `<SCRIPT` / `<ScRiPt` 一样会被
// Go 侧跳过。若这里用小写敏感的查找,标记就打不上,JS 侧的分轮执行会把这些
// "已判定不执行"的标签重新取回来 —— 而没有标记时 JS 侧只校验状态/类型/大小,
// 【不校验"超过数量上限"】,第 11 个之后的脚本因此会照跑。
var 正则标签头 = regexp.MustCompile(`(?i)<\s*script`)

// 标记跳过 给"不再执行"的 <script> 打个标记,原因用短标识(不塞 URL —— 那会
// 让属性值变得难读,而且 URL 本身已经在 scriptErrs 里点名了)。
//
// 为什么必须有这个标记:JS 侧的分轮执行要处理 document.write 注入的脚本,它看到
// 带 src 的标签就会取回来执行。如果没有标记,Go 侧所有这些"不执行"的判定
// (404 / text-html / 超上限 / 超过数量上限 / module / 数据块)都会被它绕过去 ——
// 第三轮交叉评审实测:404 的 JS 体与超上限脚本的前缀都被重新执行了,而 scriptErrs
// 同时写着"取不到",输出自相矛盾。判定的归属必须唯一:Go 侧说了不执行就不执行。
//
// 标记本身不区分原因的大小写,JS 侧只看"有没有";具体原因在 scriptErrs 里点名。
func 标记跳过(tag, 原因 string) string {
	loc := 正则标签头.FindStringIndex(tag)
	if loc == nil {
		return tag
	}
	j := loc[1]
	return tag[:j] + ` data-qjsdom-skip="` + 标签转义(原因) + `"` + tag[j:]
}

// 标签转义 让属性值里不会出现引号(URL 里的 " 用 %22 表达,不影响可读性,
// 也避免依赖实体解码 —— linkedom 的 getAttribute 不解码实体)。
func 标签转义(s string) string {
	return strings.ReplaceAll(s, `"`, "%22")
}

// 安全注释 让 URL 里的 */ 与 </script 都不会破坏我们拼出来的内联块。
func 安全注释(s string) string {
	s = strings.ReplaceAll(s, "*/", "* /")
	s = strings.ReplaceAll(s, "\n", " ")
	return 防截断(s)
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
