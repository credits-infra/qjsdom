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
	"net/url"
	"strings"
	"time"

	"github.com/fastschema/qjs"

	"qjsdom/internal/fetcher"
)

//go:embed js/host_shims.js
var hostShims string

//go:embed js/page_runner.js
var pageRunner string

//go:embed js/image_fetch.js
var imageFetch string

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
	// MaxTextLen 截断正文长度(0 = 不截断)。
	MaxTextLen int
	// RunScripts 是否执行页面内联脚本(默认 true)。
	RunScripts bool
	// EnableImages 是否让 `new Image()` 真的去取图片尺寸(默认 true)。
	// 用「禁用」而非「启用」表达,是为了让 Go 的零值 = 默认开启。
	DisableImages bool
	// FetchOptions 供图片探测使用的网络参数。
	FetchOptions fetcher.Options

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
	Title      string   `json:"title"`
	Text       string   `json:"text"`
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

	// ImageFiles 是已下载的页面图片(仅 DownloadImages 为 true 时填充)。
	// 每项含本地路径 —— 上层可直接把它交给支持图像输入的模型。
	ImageFiles []fetcher.ImageFile `json:"imageFiles,omitempty"`

	// 以下为 Go 侧补充的诊断信息
	Elapsed time.Duration `json:"-"`
}

// EngineVersion 供 CLI 打印。
const EngineVersion = "qjsdom/0.1 (qjs + linkedom, CGO-free)"

// Render 解析 HTML,执行内联脚本,提取正文。
//
// pageURL 用于给页面一个合理的 location;html 是页面源码。
func Render(html, pageURL string, opt Options) (*Result, error) {
	start := time.Now()

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
		registerImageProbe(ctx, pageURL, opt)
	}

	if _, err := ctx.Eval("host_shims.js", qjs.Code(hostShims)); err != nil {
		return nil, fmt.Errorf("宿主 shim 注入失败: %w", err)
	}
	if _, err := ctx.Eval("linkedom.js", qjs.Code(linkedomBundle)); err != nil {
		return nil, fmt.Errorf("linkedom 注入失败: %w", err)
	}
	// 用真实网络版 Image 覆盖 host_shims 里的简易实现
	if !opt.DisableImages {
		if _, err := ctx.Eval("image_fetch.js", qjs.Code(imageFetch)); err != nil {
			return nil, fmt.Errorf("Image 网络层注入失败: %w", err)
		}
	}

	script := buildRunner(html, pageURL, opt)
	v, err := ctx.Eval("page_runner.js", qjs.Code(script))
	if err != nil {
		return nil, fmt.Errorf("页面处理失败: %w", err)
	}
	raw := v.String()
	v.Free()

	var out Result
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("结果解析失败(%v): %s", err, truncate(raw, 300))
	}

	if !opt.RunScripts {
		// RunScripts=false 时,page_runner 依然会跑;这里只用于标注
		out.ScriptErrs = append(out.ScriptErrs, "note: RunScripts=false 尚未实现,脚本仍然执行了")
	}
	if opt.MaxTextLen > 0 && len(out.Text) > opt.MaxTextLen {
		out.Text = out.Text[:opt.MaxTextLen] + "…(已截断)"
	}
	out.Elapsed = time.Since(start)

	// 按需下载页面图片 —— 图片内容是要交给模型看的产出
	if opt.DownloadImages {
		downloadPageImages(&out, pageURL, opt)
	}

	return &out, nil
}

// downloadPageImages 把页面里的 <img> 下载到 opt.ImagesDir。
//
// 单张失败不影响其它图片,更不影响正文 —— 结果里带 Error 字段即可。
func downloadPageImages(out *Result, pageURL string, opt Options) {
	lim := opt.ImageLimits
	if lim.MaxImages <= 0 {
		lim = fetcher.DefaultDownloadLimits()
	}
	fo := opt.FetchOptions
	if fo.UserAgent == "" {
		fo = fetcher.DefaultOptions()
	}

	var base *url.URL
	if pageURL != "" {
		base, _ = url.Parse(pageURL)
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
func buildRunner(html, pageURL string, opt Options) string {
	htmlLit, _ := json.Marshal(html)
	urlLit, _ := json.Marshal(pageURL)
	selLit, _ := json.Marshal(opt.Selector)

	mode := opt.Mode
	if mode == "" {
		mode = ModeAll
	}
	modeLit, _ := json.Marshal(string(mode))

	s := pageRunner
	s = strings.ReplaceAll(s, "__HTML__", string(htmlLit))
	s = strings.ReplaceAll(s, "__URL__", string(urlLit))
	s = strings.ReplaceAll(s, "__SELECTOR__", string(selLit))
	s = strings.ReplaceAll(s, "__MODE__", string(modeLit))
	return s
}

// registerImageProbe 把 Go 侧的网络能力暴露给页面 JS。
//
// JS 侧拿到的是 JSON 字符串(而不是对象)—— 避免在两种运行时之间
// 传递复杂对象,也让失败情况能统一用 {ok:false,error:...} 表达。
func registerImageProbe(ctx *qjs.Context, pageURL string, opt Options) {
	fo := opt.FetchOptions
	if fo.UserAgent == "" {
		fo = fetcher.DefaultOptions()
	}
	// 单张图不该拖住整页:把探测超时压到最多 8 秒
	if fo.Timeout <= 0 || fo.Timeout > 8*time.Second {
		fo.Timeout = 8 * time.Second
	}

	var base *url.URL
	if pageURL != "" {
		base, _ = url.Parse(pageURL)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
