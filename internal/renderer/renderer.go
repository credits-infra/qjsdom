// Package renderer 在纯 Go 进程内解析网页并执行页面 JavaScript。
//
// 设计要点(每一条都有实测依据):
//
//  1. 引擎 = qjs(QuickJS + Wazero WASM),CGO-free,支持 ES2023 / async-await。
//     实测:goja(ES5.1)连 `const` 都解析不了,jsdom 的 bundle 更是要 node:fs/node:vm;
//     qjs 能跑 ES2023,所以「打包成一个自包含 bundle」这条路才成立。
//
//  2. DOM = linkedom,不是 jsdom。jsdom 打包出来 10.8MB 且带 node:fs/node:vm 依赖;
//     linkedom 零 Node 内建依赖,打包仅 0.5MB,ES2020 语法 qjs 直接吃。
//
//  3. 宿主 API 全部在 JS 侧 shim(js/host_shims.js + 打包时注入),Go 侧零 DOM 代码。
//
//  4. **每个页面一个全新 Runtime**。实测:复用同一 Runtime 时,上一页的
//     `var siteName`、`globalThis.helperA` 会泄漏到下一页,连 `Array.prototype`
//     都被改(`{"seenSiteName":"string","arrayPolluted":"function"}`)。
//     新建 Runtime 的开销实测仅 ~211ms(首次含 WASM 加载约 900ms),换来彻底隔离。
package renderer

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fastschema/qjs"

	"qjsdom/internal/fetcher"
)

//go:embed js/host_shims.js
var hostShims string

//go:embed js/page_runner.js
var pageRunner string

//go:embed js/image_fetch.js
var imageFetch string

//go:embed js/fetch_shim.js
var fetchShim string

//go:embed js/document_write.js
var documentWriteShim string

//go:embed assets/linkedom.core.js
var linkedomBundle string

// Mode 决定内容提取策略。
//
// 刻意【不做自动判型】:意图信息本来就由调用方持有 —— 模型调这个工具时
// 知道自己是在"找东西"还是在"读文章"。而"从页面特征反推意图"的启发式
// 必然在混合页上出错(链接密度 15%~70% 的那些页面怎么办?)。
// 选错 mode 的代价只是一次重新渲染,所以默认 all 兜底最稳。
type Mode string

const (
	// ModeAll 整页文本 + 链接列表,什么都不丢(默认)。
	ModeAll Mode = "all"
	// ModeArticle 正文优先:在候选容器里挑链接密度最低的。
	// 适合文章 / 百科 / 博客 / 文档。
	ModeArticle Mode = "article"
	// ModeLinks 链接列表(标题 + URL)。
	// 搜索页 / 目录页 / 导航页的「正文」恰恰就是链接 ——
	// 用 article 的判据反而会把真正的内容全删掉。
	ModeLinks Mode = "links"
)

// ParseMode 解析 mode 字符串。未知值回落到 all,不报错 ——
// 宁可给出一个可能不理想的结果,也不要因为参数写错而什么都不返回。
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(ModeArticle):
		return ModeArticle
	case string(ModeLinks):
		return ModeLinks
	default:
		return ModeAll
	}
}

// Link 是页面里的一个链接(links 模式的内容主体)。
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Options 控制单次渲染行为。
type Options struct {
	// Mode 提取策略(零值 = all)。由调用方指定,不做自动判型。
	Mode Mode
	// Selector 直接指定正文容器(对 all / article 生效;
	// links 模式忽略它 —— 那种页面要的是全部链接)。
	Selector string
	// MaxTextLen 截断正文长度(0 = 不截断)。按【字符】计,不是字节。
	MaxTextLen int
	// Markdown 为 true 时额外产出 Markdown(Result.Markdown)。
	//
	// 转换交给现成的库(internal/markdown),不在 JS 里另写一份规则:
	// HTML→Markdown 的边界(嵌套列表、表格、转义)不值得自己踩一遍。
	Markdown bool
	// RunScripts 是否执行页面内联脚本(默认 true)。
	RunScripts bool
	// EnableImages 是否让 `new Image()` 真的去取图片尺寸(默认 true)。
	// 用「禁用」而非「启用」表达,是为了让 Go 的零值 = 默认开启。
	DisableImages bool
	// DisableJSFetch 关掉页面脚本的网络能力(globalThis.fetch / XMLHttpRequest)。
	// 默认开启:不少页面靠脚本取数据后才把正文渲染出来,没有 fetch 就只能拿到空壳。
	// 关掉之后页面脚本调用 fetch 会得到 "fetch is not defined",与没有这个能力时一致。
	DisableJSFetch bool
	// DisableScriptSrc 关掉 <script src> 的加载(默认开启)。
	// 默认开:正文常由外链脚本渲染出来,不取这些脚本就只能看到空壳。
	DisableScriptSrc bool
	// FetchOptions 供图片探测使用的网络参数。
	FetchOptions fetcher.Options
	// JSFetchBudget 页面脚本联网的总时间预算(零值 = 10 秒)。
	//
	// 从渲染开始计时,而不是从第一次请求算起。用尽之后后续请求直接失败,并在
	// scriptErrs 里说明原因 —— 宁可少取且说清为什么,也不要为了取全让整页卡住。
	JSFetchBudget time.Duration

	// DownloadImages 是否把页面里的 <img> 下载到 ImagesDir。
	//
	// 图片内容是有价值的产出:模型(Gemini 等)支持图像输入,能直接看图。
	// 视频/音频刻意不实现 —— 体积大、处理慢,且不是我们要的信息载体。
	DownloadImages bool
	// ImagesDir 图片保存目录(DownloadImages 为 true 时生效)。
	ImagesDir string
	// ImageLimits 单页图片下载的资源上限(零值用 DefaultDownloadLimits)。
	ImageLimits fetcher.DownloadLimits
}

// Result 是渲染结果。字段名对应 page_runner.js 的返回 JSON。
type Result struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	// TextLen 是正文的字符数(renderer 统一算,不是字节)。
	TextLen    int      `json:"textLen"`
	ScriptTags int      `json:"scriptTags"`
	ScriptsRan int      `json:"scriptsRan"`
	ScriptErrs []string `json:"scriptErrs"`
	Links      []Link   `json:"links"`
	Images     []string `json:"images"`
	HasBody    bool     `json:"hasBody"`
	// Mode 实际使用的提取策略。
	Mode string `json:"mode"`
	// Degraded 为 true 表示 DOM 解析失败、退回了粗暴的文本提取。
	// 这种情况仍然会返回结果(而不是报错),调用方应按需降级处理。
	Degraded bool `json:"degraded"`

	// HTML 是正文容器的 HTML(已去掉 script/style),仅 Options.Markdown
	// 为 true 时填充。它是转换 Markdown 的中间产物,转换完就清空 ——
	// 留着只会让 -json 输出凭空大一大截。
	HTML string `json:"html,omitempty"`
	// Markdown 是正文的 Markdown 版本,仅 Options.Markdown 为 true 时填充。
	Markdown string `json:"markdown,omitempty"`

	// ImageFiles 是已下载的页面图片(仅 DownloadImages 为 true 时填充)。
	// 每项含本地路径 —— 上层可直接把它交给支持图像输入的模型。
	ImageFiles []fetcher.ImageFile `json:"imageFiles,omitempty"`

	// 以下为 Go 侧补充的诊断信息
	Elapsed time.Duration `json:"-"`
}

// EngineVersion 供 CLI 打印。
const (
	// 页面脚本每页最多发起几次网络请求。死循环里的 fetch 不该把整页拖到超时;
	// 15 次足够覆盖"取一两个 API 再拼出正文"的常见页面。
	maxJSFetchPerPage = 15
	// 页面脚本单次响应的字节上限。这类请求取的多是 JSON,不需要页面级的 4MB 配额 ——
	// 一页 15 次 × 4MB 就是 60MB,那才是真会打爆内存的地方。
	maxJSFetchBytes = 1 << 20 // 1MB
	// 页面脚本联网的总时间预算(零值走这里,可用 Options.JSFetchBudget 覆盖)。
	// 次数上限管"多少次",这一道管"多久":十五次各自等满超时同样能把整页拖住,
	// 实测最坏情况约 150 秒且串行不可打断。
	maxJSFetchTotal = 10 * time.Second
)

const EngineVersion = "qjsdom/0.1 (qjs + linkedom, CGO-free)"

// Render 解析 HTML,执行内联脚本,提取正文。
//
// pageURL 用于给页面一个合理的 location;html 是页面源码。
func Render(html, pageURL string, opt Options) (*Result, error) {
	start := time.Now()

	// 补全相对地址用的基准:页面里第一个 <base href> 会顶掉文档地址(浏览器行为)。
	// 文档地址本身仍然交给 location.href,两者不能混。
	基准 := 有效基准(html, pageURL)

	rt, err := qjs.New()
	if err != nil {
		return nil, fmt.Errorf("qjs 运行时启动失败: %w", err)
	}
	// 关键:整页生命周期结束即销毁,状态零残留
	defer rt.Close()
	ctx := rt.Context()

	// 注册宿主网络能力。必须在 eval image_fetch.js 之前 ——
	// 那个脚本靠 typeof __qjsdom_imageProbe 判断网络层是否可用。
	if !opt.DisableImages {
		registerImageProbe(ctx, 基准, opt)
	}

	// 外链脚本先取回来内联,再交给 runner —— 顺序就是源码里的顺序。
	var 脚本提示 []string
	if !opt.DisableScriptSrc {
		html, 脚本提示 = 展开外部脚本(html, 基准, opt)
	}

	// 页面脚本的网络能力(globalThis.fetch / XMLHttpRequest)。在 host_shims
	// 之后注入 —— shim 里用到 TextEncoder(host_shims 提供)。
	var 注入提示 string
	if !opt.DisableJSFetch {
		注入提示 = registerJSFetch(ctx, 基准, opt)
	}

	if _, err := ctx.Eval("host_shims.js", qjs.Code(hostShims)); err != nil {
		return nil, fmt.Errorf("宿主 shim 注入失败: %w", err)
	}
	if _, err := ctx.Eval("linkedom.js", qjs.Code(linkedomBundle)); err != nil {
		return nil, fmt.Errorf("linkedom 注入失败: %w", err)
	}
	// document.write 之类 linkedom 没有的方法的 shim。必须在 page_runner 之前 ——
	// runner 解析完文档就会去装它。这里只定义"安装函数",真正的安装发生在
	// 每页那份 document 建好之后(方法要挂在 document 实例上)。
	if _, err := ctx.Eval("document_write.js", qjs.Code(documentWriteShim)); err != nil {
		return nil, fmt.Errorf("document.write shim 注入失败: %w", err)
	}
	// 用真实网络版 Image 覆盖 host_shims 里的简易实现
	if !opt.DisableImages {
		if _, err := ctx.Eval("image_fetch.js", qjs.Code(imageFetch)); err != nil {
			return nil, fmt.Errorf("Image 网络层注入失败: %w", err)
		}
	}

	// 两次 eval,中间隔着宿主对微任务队列的抽干 —— 顺序是这里全部的意义所在。
	//
	// quickjs 的 js_std_loop 在每次 Eval 返回后跑:它会抽干 Promise 作业队列,
	// 于是页面脚本里 fetch().then(cb) / Promise.then(cb) 的回调都在【第一次
	// Eval 返回之后】才执行。若抽取和脚本挤在一次 eval 里,回调改到的 DOM
	// 永远进不了结果(实测过:then 里写入的文本,正文里还是旧内容)。
	// 所以:阶段一跑脚本 → Eval 返回 → 宿主抽微任务 → 阶段二抽取。
	if v, err := ctx.Eval("page_runner.js", qjs.Code(buildRunner(html, pageURL, 基准, opt, "scripts"))); err != nil {
		return nil, fmt.Errorf("页面脚本阶段失败: %w", err)
	} else {
		v.Free()
	}

	v, err := ctx.Eval("page_runner.js", qjs.Code(buildRunner(html, pageURL, 基准, opt, "extract")))
	if err != nil {
		return nil, fmt.Errorf("页面处理失败: %w", err)
	}
	raw := v.String()
	v.Free()

	var out Result
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("结果解析失败(%v): %s", err, truncate(raw, 300))
	}

	out.ScriptErrs = append(out.ScriptErrs, 脚本提示...)
	if 注入提示 != "" {
		out.ScriptErrs = append(out.ScriptErrs, 注入提示)
	}
	if !opt.RunScripts {
		// RunScripts=false 时,page_runner 依然会跑;这里只用于标注
		out.ScriptErrs = append(out.ScriptErrs, "note: RunScripts=false 尚未实现,脚本仍然执行了")
	}
	if opt.MaxTextLen > 0 {
		out.Text = truncateRunes(out.Text, opt.MaxTextLen)
	}
	// TextLen 由这里统一算,别用 JS 侧给的 textLen,也别让入口自己再算一遍:
	// JS 的 text.length 是 UTF-16 单元,Go 的 len() 是字节 —— 同一个字段两种口径,
	// 中文页面下差三倍(实测 18 个汉字,两个入口一个报 18 一个报 54)。
	// 截断按字符计,这里也按字符计,口径才对得上。
	out.TextLen = utf8.RuneCountInString(out.Text)

	// Markdown 与纯文本取同一份内容(同一个正文容器),截断也走同一套字符口径。
	if opt.Markdown {
		md, err := 转Markdown(&out, 基准)
		if err != nil {
			// 转换失败不该让整次渲染失败:纯文本还在,调用方拿到的仍是有用的结果。
			out.ScriptErrs = append(out.ScriptErrs, fmt.Sprintf("note: Markdown 转换失败,已退回纯文本: %v", err))
		} else if opt.MaxTextLen > 0 {
			out.Markdown = truncateRunes(md, opt.MaxTextLen)
		} else {
			out.Markdown = md
		}
	}
	out.HTML = "" // 中间产物,不留在结果里
	out.Elapsed = time.Since(start)

	// 按需下载页面图片 —— 图片内容是要交给模型看的产出
	if opt.DownloadImages {
		downloadPageImages(&out, 基准, opt)
	}

	return &out, nil
}

// downloadPageImages 把页面里的 <img> 下载到 opt.ImagesDir。
//
// 单张失败不影响其它图片,更不影响正文 —— 结果里带 Error 字段即可。
func downloadPageImages(out *Result, 基准 string, opt Options) {
	lim := opt.ImageLimits
	if lim.MaxImages <= 0 {
		lim = fetcher.DefaultDownloadLimits()
	}
	fo := opt.FetchOptions
	if fo.UserAgent == "" {
		fo = fetcher.DefaultOptions()
	}

	var base *url.URL
	if 基准 != "" {
		base, _ = url.Parse(基准)
	}

	seen := make(map[string]bool)
	for _, raw := range out.Images {
		if len(out.ImageFiles) >= lim.MaxImages {
			break
		}
		// 内联 data: URL 本身已含数据,不必再下载
		if strings.HasPrefix(raw, "data:") {
			continue
		}
		abs := raw
		if base != nil && !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			if u, err := url.Parse(raw); err == nil {
				abs = base.ResolveReference(u).String()
			}
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out.ImageFiles = append(out.ImageFiles,
			fetcher.DownloadImage(abs, opt.ImagesDir, fo, lim.MaxBytesPer))
	}
}

// buildRunner 把三个占位符替换为 JSON 字面量。
// 用 JSON 编码而不是裸字符串拼接 —— HTML 里任何引号/反斜杠/换行都会被正确转义。
func buildRunner(html, pageURL, 基准 string, opt Options, phase string) string {
	htmlLit, _ := json.Marshal(html)
	urlLit, _ := json.Marshal(pageURL)
	// 基准与文档地址是两个东西:前者给相对地址补全用(可能被 <base href> 顶掉),
	// 后者就是 location.href。浏览器也是这么分的。
	baseLit, _ := json.Marshal(基准)
	selLit, _ := json.Marshal(opt.Selector)

	mode := opt.Mode
	if mode == "" {
		mode = ModeAll
	}
	modeLit, _ := json.Marshal(string(mode))

	needHTMLLit := "false"
	if opt.Markdown {
		needHTMLLit = "true"
	}

	// 允许外链脚本 = 既没关掉 <script src> 加载,也没有关掉页面脚本的网络能力
	// (document.write 注入的外链脚本只能在 JS 侧取回,需要后者)。
	allowSrcLit := "false"
	if !opt.DisableScriptSrc && !opt.DisableJSFetch {
		allowSrcLit = "true"
	}

	s := pageRunner
	// ⚠️ 顺序要紧:__HTML__ 是 __NEED_HTML__ 的子串,先替换 __HTML__ 会把
	// 后者改写成 __NEED_<html 字面量>,占位符就再也匹配不上了。
	s = strings.ReplaceAll(s, "__NEED_HTML__", needHTMLLit)
	s = strings.ReplaceAll(s, "__HTML__", string(htmlLit))
	s = strings.ReplaceAll(s, "__URL__", string(urlLit))
	s = strings.ReplaceAll(s, "__BASE__", string(baseLit))
	s = strings.ReplaceAll(s, "__SELECTOR__", string(selLit))
	s = strings.ReplaceAll(s, "__MODE__", string(modeLit))
	// 阶段:scripts(解析 + 跑脚本)/ extract(抽取)。见 RunPage 里的两阶段说明。
	phaseLit, _ := json.Marshal(phase)
	s = strings.ReplaceAll(s, "__PHASE__", string(phaseLit))
	s = strings.ReplaceAll(s, "__ALLOW_SCRIPT_SRC__", allowSrcLit)
	// JS 侧取回的外链脚本(只可能是 document.write 注入的)也要守同一条上限 ——
	// 数字从 Go 传过去,免得两处各写一个 512KB。
	s = strings.ReplaceAll(s, "__MAX_SCRIPT_BYTES__", strconv.Itoa(maxScriptSrcBytes))
	// 页面脚本看到的 UA 与抓取用的 UA 必须是同一个值 —— 否则同一个进程里
	// "页面以为你是谁"和"服务器看到你是谁"不一致。
	ua := opt.FetchOptions.UserAgent
	if ua == "" {
		ua = fetcher.DefaultOptions().UserAgent
	}
	uaLit, _ := json.Marshal(ua)
	s = strings.ReplaceAll(s, "__USER_AGENT__", string(uaLit))
	return s
}

// registerImageProbe 把 Go 侧的网络能力暴露给页面 JS。
//
// JS 侧拿到的是 JSON 字符串(而不是对象)—— 避免在两种运行时之间
// 传递复杂对象,也让失败情况能统一用 {ok:false,error:...} 表达。
func registerImageProbe(ctx *qjs.Context, 基准 string, opt Options) {
	fo := opt.FetchOptions
	if fo.UserAgent == "" {
		fo = fetcher.DefaultOptions()
	}
	// 单张图不该拖住整页:把探测超时压到最多 8 秒
	if fo.Timeout <= 0 || fo.Timeout > 8*time.Second {
		fo.Timeout = 8 * time.Second
	}

	var base *url.URL
	if 基准 != "" {
		base, _ = url.Parse(基准)
	}

	ctx.SetFunc("__qjsdom_imageProbe", func(this *qjs.This) (*qjs.Value, error) {
		c := this.Context()
		args := this.Args()
		if len(args) == 0 {
			return c.NewString(`{"ok":false,"error":"缺少 url 参数"}`), nil
		}

		raw := strings.TrimSpace(args[0].String())
		abs := raw
		// 页面里的图片多是相对路径,按页面 URL 补全
		if base != nil && !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			if u, err := url.Parse(raw); err == nil {
				abs = base.ResolveReference(u).String()
			}
		}

		info := fetcher.ProbeImage(abs, fo)
		b, err := json.Marshal(info)
		if err != nil {
			return c.NewString(`{"ok":false,"error":"结果编码失败"}`), nil
		}
		return c.NewString(string(b)), nil
	})
}

// registerJSFetch 把网络能力交给页面脚本(globalThis.fetch / XMLHttpRequest)。
//
// 走的是【与抓页面完全相同的那条通路】:fetcher.Fetch —— 协议白名单、SSRF 守卫、
// 大小上限、超时、重定向逐跳检查一个都不少。页面脚本不该拥有一条绕过守卫的捷径,
// 否则"页面能读什么"就等于"攻击者的 JS 能扫什么"。
//
// 三条限制(都在实测里用过):
//   - 只放行 GET —— POST/PUT 是"用我们的服务器向第三方发写请求",默认不给;
//   - 每页次数上限 —— 一个死循环里的 fetch 不该把整页拖到超时;
//   - 单次响应字节上限 —— 页面脚本取的多是 JSON,不需要 4MB 的页面级配额。
//
// 返回值为非空时表示有需要注意的情况(shim 注入失败),由调用方记进 scriptErrs。
func registerJSFetch(ctx *qjs.Context, 基准 string, opt Options) string {
	fo := opt.FetchOptions
	if fo.UserAgent == "" {
		fo = fetcher.DefaultOptions()
	}
	// 页面脚本自己的请求不该拖垮整页
	if fo.Timeout <= 0 || fo.Timeout > 10*time.Second {
		fo.Timeout = 10 * time.Second
	}
	if fo.MaxBytes <= 0 || fo.MaxBytes > maxJSFetchBytes {
		fo.MaxBytes = maxJSFetchBytes
	}

	var base *url.URL
	if 基准 != "" {
		base, _ = url.Parse(基准)
	}

	预算 := opt.JSFetchBudget
	if 预算 <= 0 {
		预算 = maxJSFetchTotal
	}
	起点 := time.Now()

	剩余 := maxJSFetchPerPage
	ctx.SetFunc("__qjsdom_httpFetch", func(this *qjs.This) (*qjs.Value, error) {
		c := this.Context()
		args := this.Args()
		if len(args) == 0 {
			return c.NewString(`{"error":"缺少 url 参数"}`), nil
		}
		raw := strings.TrimSpace(args[0].String())
		method := "GET"
		if len(args) > 1 {
			method = strings.ToUpper(strings.TrimSpace(args[1].String()))
		}
		if method != "GET" {
			return c.NewString(错误JSON("只支持 GET 请求(收到 " + method + ")")), nil
		}
		if 剩余 <= 0 {
			return c.NewString(错误JSON(fmt.Sprintf(
				"本页页面脚本发起的请求已达上限(%d),后续请求不再转发", maxJSFetchPerPage))), nil
		}
		// 时间预算:次数上限管"多少次",这一道管"多久" —— 两者都要。
		if 用时 := time.Since(起点); 用时 >= 预算 {
			return c.NewString(错误JSON(fmt.Sprintf(
				"本页页面脚本的网络时间预算已用尽(%s):为免整页被拖住,后续请求不再转发",
				预算))), nil
		}
		剩余--

		// 相对地址按页面地址补全 —— 与浏览器一致,也是页面脚本默认的写法
		abs := raw
		if base != nil {
			if u, err := url.Parse(raw); err == nil {
				abs = base.ResolveReference(u).String()
			}
		}

		res, err := fetcher.Fetch(abs, fo)
		if err != nil {
			return c.NewString(错误JSON(err.Error())), nil
		}
		hdr := map[string]string{}
		for k, v := range res.Header {
			hdr[strings.ToLower(k)] = strings.Join(v, ", ")
		}
		b, err := json.Marshal(map[string]any{
			"status":     res.StatusCode,
			"statusText": http.StatusText(res.StatusCode),
			"url":        res.URL,
			"redirected": res.Redirected,
			"headers":    hdr,
			"body":       res.HTML,
			// 让 JS 侧也能按同一套规则判定:被截断的/超大的脚本不执行
			"bytes":     res.Bytes,
			"truncated": res.Truncated,
		})
		if err != nil {
			return c.NewString(错误JSON("响应编码失败")), nil
		}
		return c.NewString(string(b)), nil
	})

	if _, err := ctx.Eval("fetch_shim.js", qjs.Code(fetchShim)); err != nil {
		// shim 注入失败不该让整页失败:页面脚本拿不到 fetch,但正文照旧能抽。
		return fmt.Sprintf("note: fetch shim 注入失败: %v", err)
	}
	return ""
}

func 错误JSON(msg string) string {
	b, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		return `{"error":"错误信息编码失败"}`
	}
	return string(b)
}

func truncate(s string, n int) string {
	if cut, ok := cutRunes(s, n); ok {
		return cut + "…"
	}
	return s
}

// cutRunes 取字符串的前 n 个【字符】(不是字节),并报告是否真的截掉了东西。
//
// 为什么不能用 len(s) 与 s[:n]:Go 的字符串索引是字节下标,按字节切断会把
// 一个多字节字符切成两半 —— 结果既不是合法 UTF-8(中文正文实测:截出来是
// 半个汉字),序列化成 JSON 时坏字节被替换成 U+FFFD,等于文字被静默损坏。
// 只有落在字符边界上,才配得上 MAX_TEXT 文档里写的 "characters"。
func cutRunes(s string, n int) (string, bool) {
	if n <= 0 {
		return s, false
	}
	if len(s) <= n {
		return s, false // 字节数都没超,字符数必然没超
	}
	count := 0
	for i := range s { // range 按字符迭代,i 是该字符的起始字节下标
		if count == n {
			return s[:i], true
		}
		count++
	}
	return s, false // 字符数其实不超过 n(只是字节数超了)
}

// truncateRunes 按字符截断,并在真的截断时追加提示。
func truncateRunes(s string, n int) string {
	if cut, ok := cutRunes(s, n); ok {
		return cut + "…(已截断)"
	}
	return s
}
