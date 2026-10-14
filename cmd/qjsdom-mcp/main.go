// Command qjsdom-mcp 把 qjsdom 暴露为一个 MCP 工具。
//
// 面向不想引入重型基础设施的用户:一个静态二进制,不需要浏览器,也不需要
// Node/Bun 运行时。任何 MCP 客户端(Claude Desktop、Cursor、各类 agent
// 框架)都能用它读网页。
//
// 它比 CLI 更适合共享的一点:MCP 原生支持图片内容块,所以页面插图可以
// 直接作为内容块返回,模型看得见图,调用方不必去读本地文件路径。
//
// 配置全部走环境变量,方便直接写进客户端的 MCP 配置:
//
//	QJSDOM_CACHE_DIR        缓存目录(默认:用户缓存目录/qjsdom)
//	QJSDOM_CACHE_TTL        缓存有效期(默认 10m;设 0 关闭缓存)
//	QJSDOM_CACHE_MAX        缓存容量上限,字节(默认 64MB)
//	QJSDOM_TIMEOUT          抓取超时(默认 20s)
//	QJSDOM_MAX_TEXT         正文截断长度(默认 60000)
//	QJSDOM_MAX_IMAGES       每次返回的图片数上限(默认 5;设 0 不返回图片)
//	QJSDOM_MIN_IMAGE_SIDE   图片最小边长,小于此值视为图标丢弃(默认 100)
//	QJSDOM_MIN_IMAGE_BYTES  图片最小字节数(默认 5120)
//	QJSDOM_MAX_IMAGE_BYTES  单张图片字节上限(默认 2MB)
//	QJSDOM_DEBUG            设为 1 时把诊断信息写到 stderr
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qjsdom/internal/cache"
	"qjsdom/internal/fetcher"
	"qjsdom/internal/renderer"
)

const version = "0.1.0"

// config 保存从环境变量读到的运行参数。
type config struct {
	cacheDir     string
	cacheTTL     time.Duration
	cacheMax     int64
	timeout      time.Duration
	maxText      int
	maxImages    int
	minImageSide int
	minImageByte int64
	maxImageByte int64
	debug        bool
}

func loadConfig() config {
	c := config{
		cacheDir:     envStr("QJSDOM_CACHE_DIR", ""),
		cacheTTL:     envDur("QJSDOM_CACHE_TTL", cache.DefaultTTL),
		cacheMax:     envInt64("QJSDOM_CACHE_MAX", cache.DefaultMaxBytes),
		timeout:      envDur("QJSDOM_TIMEOUT", 20*time.Second),
		maxText:      int(envInt64("QJSDOM_MAX_TEXT", 60000)),
		maxImages:    int(envInt64("QJSDOM_MAX_IMAGES", 5)),
		minImageSide: int(envInt64("QJSDOM_MIN_IMAGE_SIDE", 100)),
		minImageByte: envInt64("QJSDOM_MIN_IMAGE_BYTES", 5120),
		maxImageByte: envInt64("QJSDOM_MAX_IMAGE_BYTES", 2<<20),
		debug:        os.Getenv("QJSDOM_DEBUG") == "1",
	}
	return c
}

// fetchArgs 是 fetch_page 的入参。json/jsonschema 两个 tag 会被 SDK
// 用来生成工具的 JSON Schema 与描述。
type fetchArgs struct {
	URL      string `json:"url" jsonschema:"required. Absolute http(s) URL of the page to read."`
	Mode     string `json:"mode,omitempty" jsonschema:"Extraction strategy. all (default) returns the whole page text plus its links, losing nothing. article picks the main content container (articles, encyclopedias, docs). links returns just the link list, which is what search results and index pages need."`
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector naming the content container directly. Use it when article mode picks the wrong container. For Wikipedia, #mw-content-text."`
	MaxLen   int    `json:"max_len,omitempty" jsonschema:"Truncate the returned text at this many characters. 0 uses the server default."`
	Images   *bool  `json:"images,omitempty" jsonschema:"Return the page's images as image content blocks so the model can see them. Defaults to true. Small icons are filtered out automatically."`
}

// fetchOut 是返回给模型的结构化摘要(图片另外以内容块返回)。
type fetchOut struct {
	Title       string   `json:"title,omitempty"`
	Mode        string   `json:"mode"`
	TextLen     int      `json:"text_len"`
	Degraded    bool     `json:"degraded,omitempty"`
	ScriptsRan  string   `json:"scripts_executed,omitempty"`
	ImagesSent  int      `json:"images_returned"`
	ImagesFound int      `json:"images_found"`
	LinksFound  int      `json:"links_found"`
	FromCache   bool     `json:"from_cache,omitempty"`
	Notice      []string `json:"notice,omitempty"`
}

func main() {
	cfg := loadConfig()

	// MCP 服务是长驻进程,缓存默认开启 —— 模型换 mode 重试时不必重新抓取。
	var c *cache.Cache
	if cfg.cacheTTL > 0 {
		c = cache.New(cache.Options{Dir: cfg.cacheDir, TTL: cfg.cacheTTL, MaxBytes: cfg.cacheMax})
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "qjsdom",
		Title:   "qjsdom — read web pages without a browser",
		Version: version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "fetch_page",
		Description: "Fetch a web page and return its content as text, executing the page's " +
			"JavaScript first so that script-generated content is included. Runs in-process " +
			"with no browser and no Node runtime. Use mode=links for search results and index " +
			"pages, mode=article for articles and documentation, and the default mode=all when " +
			"unsure. Page images are returned as image content blocks so you can read " +
			"illustrations, diagrams, and figures.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args fetchArgs) (*mcp.CallToolResult, fetchOut, error) {
		return handleFetch(cfg, c, args)
	})

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[qjsdom-mcp] 启动 v%s cache=%v maxImages=%d\n",
			version, c != nil, cfg.maxImages)
	}
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("qjsdom-mcp: %v", err)
		os.Exit(1)
	}
}

// handleFetch 完成一次抓取 + 渲染 + 组装内容块。
func handleFetch(cfg config, c *cache.Cache, args fetchArgs) (*mcp.CallToolResult, fetchOut, error) {
	var out fetchOut

	url := strings.TrimSpace(args.URL)
	if url == "" {
		return errResult("url 参数为空"), out, nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return errResult("只支持 http/https URL,收到: " + url), out, nil
	}

	mode := renderer.ParseMode(args.Mode)
	out.Mode = string(mode)

	fo := fetcher.DefaultOptions()
	fo.Timeout = cfg.timeout

	// 1. 取 HTML(优先缓存)
	var html string
	if c != nil {
		if h, ok := c.Get(url); ok {
			html = h
			out.FromCache = true
			debugf(cfg, "缓存命中 %s", url)
		}
	}
	if html == "" {
		res, err := fetcher.Fetch(url, fo)
		if err != nil {
			return errResult("抓取失败: " + err.Error()), out, nil
		}
		html = res.HTML
		if c != nil {
			if err := c.Put(url, html); err != nil {
				debugf(cfg, "未缓存: %v", err)
			}
		}
	}

	// 2. 渲染(不落盘图片 —— MCP 要的是字节,不是文件)
	maxText := args.MaxLen
	if maxText <= 0 {
		maxText = cfg.maxText
	}
	// 这里先让渲染器把图片 URL 收集出来,图片字节我们自己在第 3 步取,
	// 因为 MCP 需要 base64 内容块而不是本地文件。
	res, err := renderer.Render(html, url, renderer.Options{
		Mode:          mode,
		Selector:      args.Selector,
		MaxTextLen:    maxText,
		RunScripts:    true,
		DisableImages: true, // 页面脚本的 Image 探测对 MCP 无意义,省掉网络往返
		FetchOptions:  fo,
	})
	if err != nil {
		return errResult("渲染失败: " + err.Error()), out, nil
	}

	out.Title = res.Title
	out.TextLen = len(res.Text)
	out.Degraded = res.Degraded
	out.ScriptsRan = fmt.Sprintf("%d/%d", res.ScriptsRan, res.ScriptTags)
	out.LinksFound = len(res.Links)
	out.ImagesFound = len(res.Images)

	// 3. 组装内容块
	content := []mcp.Content{
		&mcp.TextContent{Text: composeText(res, out)},
	}

	wantImages := cfg.maxImages > 0
	if args.Images != nil {
		wantImages = *args.Images && cfg.maxImages > 0
	}
	if wantImages && len(res.Images) > 0 {
		imgs, notices := collectImages(cfg, fo, res.Images, url)
		out.ImagesSent = len(imgs)
		out.Notice = append(out.Notice, notices...)
		for _, im := range imgs {
			content = append(content, &mcp.ImageContent{
				MIMEType: im.mime,
				Data:     im.data,
			})
		}
	}

	return &mcp.CallToolResult{Content: content}, out, nil
}

// pickedImage 是一张准备作为内容块返回的图片。
type pickedImage struct {
	mime string
	data []byte // base64
}

// collectImages 下载并筛选页面图片。
//
// 筛选是必须的:真实页面上多数 <img> 是图标、logo、占位符 —— 实测维基
// 条目收集到 87 张,其中绝大多数对模型毫无价值。全丢过去既浪费上下文,
// 又会让真正的插图淹没在噪声里。
func collectImages(cfg config, fo fetcher.Options, urls []string, pageURL string) ([]pickedImage, []string) {
	var picked []pickedImage
	var notices []string
	skippedSmall, skippedBig, failed := 0, 0, 0

	// 记下失败原因 —— 只报「N 张失败」等于没说,调用方无法判断是网络问题、
	// 被站点拒绝、还是格式不支持。这里保留前几条,让排查有线索。
	type failure struct{ url, reason string }
	var failures []failure

	// URL 去重。CLI 侧一直有,这里漏了 —— 实测维基条目的 87 个 <img> 地址
	// 里只有 64 个是唯一的(同一张图在导航、正文、页脚各出现一次)。
	// 重复下载既浪费带宽,也更容易触发站点限流。
	seen := make(map[string]bool)
	deduped := 0

	base := pageURL
	for _, raw := range urls {
		if len(picked) >= cfg.maxImages {
			break
		}
		abs := absoluteURL(base, raw)
		if abs == "" {
			continue
		}
		if seen[abs] {
			deduped++
			continue
		}
		seen[abs] = true

		data, info := fetcher.FetchImageBytes(abs, fo, cfg.maxImageByte)
		if info.Error != "" {
			failed++
			if len(failures) < 5 {
				failures = append(failures, failure{abs, info.Error})
			}
			continue
		}
		// 图标过滤:尺寸与字节双阈值。
		// 尺寸解不出来(SVG 等)时只看字节数。
		if info.Bytes < cfg.minImageByte {
			skippedSmall++
			continue
		}
		if info.Width > 0 && info.Height > 0 &&
			(info.Width < cfg.minImageSide || info.Height < cfg.minImageSide) {
			skippedSmall++
			continue
		}
		if int64(len(data)) > cfg.maxImageByte {
			skippedBig++
			continue
		}

		picked = append(picked, pickedImage{
			mime: mimeOf(info.Format),
			data: []byte(base64.StdEncoding.EncodeToString(data)),
		})
	}

	if deduped > 0 {
		notices = append(notices, fmt.Sprintf("跳过了 %d 个重复的图片地址", deduped))
	}
	if skippedSmall > 0 {
		notices = append(notices, fmt.Sprintf("跳过了 %d 张过小的图片(图标/占位符)", skippedSmall))
	}
	if skippedBig > 0 {
		notices = append(notices, fmt.Sprintf("跳过了 %d 张过大的图片", skippedBig))
	}
	if failed > 0 {
		reasons := make([]string, 0, len(failures))
		for _, f := range failures {
			reasons = append(reasons, fmt.Sprintf("%s → %s", shortenURL(f.url), f.reason))
		}
		more := ""
		if failed > len(failures) {
			more = fmt.Sprintf("(另有 %d 张未列出)", failed-len(failures))
		}
		notices = append(notices,
			fmt.Sprintf("%d 张图片下载失败%s: %s", failed, more, strings.Join(reasons, "; ")))
	}
	return picked, notices
}

// shortenURL 把 URL 缩短到便于在提示里显示的长度。
func shortenURL(u string) string {
	if len(u) <= 60 {
		return u
	}
	return u[:57] + "…"
}

// composeText 把正文与元信息拼成给模型看的文本。
func composeText(res *renderer.Result, out fetchOut) string {
	var b strings.Builder

	if res.Title != "" {
		b.WriteString("# ")
		b.WriteString(res.Title)
		b.WriteString("\n\n")
	}

	if out.Degraded {
		b.WriteString("> 注意:DOM 解析失败,以下内容来自降级提取(仅去标签的纯文本)。\n\n")
	}

	b.WriteString(res.Text)

	// 链接模式已经把链接写进正文了,其它模式附上链接列表
	if res.Mode != string(renderer.ModeLinks) && len(res.Links) > 0 {
		b.WriteString("\n\n## 页面链接\n\n")
		limit := 40
		for i, l := range res.Links {
			if i >= limit {
				fmt.Fprintf(&b, "\n…另有 %d 条链接\n", len(res.Links)-limit)
				break
			}
			fmt.Fprintf(&b, "- [%s](%s)\n", l.Title, l.URL)
		}
	}
	return b.String()
}

// absoluteURL 把页面里的相对地址补全成绝对地址。
func absoluteURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if strings.HasPrefix(raw, "//") {
		if strings.HasPrefix(base, "http://") {
			return "http:" + raw
		}
		return "https:" + raw
	}
	// 相对路径:拼到页面目录下
	i := strings.Index(base, "://")
	if i < 0 {
		return ""
	}
	rest := base[i+3:]
	slash := strings.Index(rest, "/")
	origin := base
	if slash >= 0 {
		origin = base[:i+3+slash]
	}
	if strings.HasPrefix(raw, "/") {
		return base[:i+3+strings.Index(rest, "/")] + raw
	}
	if !strings.HasSuffix(origin, "/") {
		origin += "/"
	}
	return origin + raw
}

// mimeOf 把图片格式转成 MIME 类型。
func mimeOf(format string) string {
	switch strings.ToLower(format) {
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "bmp":
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func debugf(cfg config, format string, args ...any) {
	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[qjsdom-mcp] "+format+"\n", args...)
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
