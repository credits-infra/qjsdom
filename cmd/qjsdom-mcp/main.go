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
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	// saveImagesDir 非空时把图片写到磁盘,并在结果里返回路径。
	// 用途是让调用方能发送图片(MCP 的内容块只能让模型看见图,发不出去)。
	saveImagesDir string
	debug         bool
}

func loadConfig() config {
	c := config{
		cacheDir:      envStr("QJSDOM_CACHE_DIR", ""),
		cacheTTL:      envDur("QJSDOM_CACHE_TTL", cache.DefaultTTL),
		cacheMax:      envInt64("QJSDOM_CACHE_MAX", cache.DefaultMaxBytes),
		timeout:       envDur("QJSDOM_TIMEOUT", 20*time.Second),
		maxText:       int(envInt64("QJSDOM_MAX_TEXT", 60000)),
		maxImages:     int(envInt64("QJSDOM_MAX_IMAGES", 5)),
		minImageSide:  int(envInt64("QJSDOM_MIN_IMAGE_SIDE", 100)),
		minImageByte:  envInt64("QJSDOM_MIN_IMAGE_BYTES", 5120),
		maxImageByte:  envInt64("QJSDOM_MAX_IMAGE_BYTES", 2<<20),
		saveImagesDir: envStr("QJSDOM_SAVE_IMAGES_DIR", ""),
		debug:         os.Getenv("QJSDOM_DEBUG") == "1",
	}
	return c
}

// fetchArgs 是 fetch_page 的入参。json/jsonschema 两个 tag 会被 SDK
// 用来生成工具的 JSON Schema 与描述。
type fetchArgs struct {
	URL      string `json:"url" jsonschema:"required. Absolute http(s) URL of the page to read."`
	Mode     string `json:"mode,omitempty" jsonschema:"Extraction strategy. all (default) returns the whole page text plus its links, losing nothing. article picks the main content container (articles, encyclopedias, docs). links returns just the link list, which is what search results and index pages need."`
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector naming the content container directly. Use it when article mode picks the wrong container. For Wikipedia, #mw-content-text."`
	MaxLen   int    `json:"max_len,omitempty" jsonschema:"Truncate the returned text at this many characters. Counted in characters, so multi-byte text is never cut mid-character. 0 uses the server default."`
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
	// ImagePaths 是图片落盘后的绝对路径,只在设置了 QJSDOM_SAVE_IMAGES_DIR
	// 时非空。内容块让模型看见图,路径让调用方把图发出去,这是两件事。
	ImagePaths []string `json:"image_paths,omitempty"`
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
			"illustrations, diagrams, and figures; the text ends with a numbered list giving " +
			"each image's URL, format, pixel size, and saved path. " +
			"To search the web, fetch https://html.duckduckgo.com/html/?q=<keywords> with " +
			"mode=links, then open one of the results with mode=article. This tool cannot run " +
			"fetch or XHR, so search engines that build their results with JavaScript " +
			"(google.com/search, bing.com/search) come back as an empty shell. An empty " +
			"result — text_len 0, no title — means this URL yielded nothing, not that the " +
			"thing you looked for does not exist.",
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
	var contentImgs []mcp.Content
	var pickedImgs []pickedImage
	wantImages := cfg.maxImages > 0
	if args.Images != nil {
		wantImages = *args.Images && cfg.maxImages > 0
	}
	if wantImages && len(res.Images) > 0 {
		imgs, notices := collectImages(cfg, fo, res.Images, url)
		pickedImgs = imgs
		out.ImagesSent = len(imgs)
		out.Notice = append(out.Notice, notices...)
		for _, im := range imgs {
			contentImgs = append(contentImgs, &mcp.ImageContent{
				MIMEType: im.mime,
				Data:     im.data,
			})
			// 下标必须与内容块严格对齐:image_paths[i] 就是第 i 个内容块的路径。
			// 落盘失败时留空串占位 —— 跳过它会让后面每一条都错位一位,
			// 而调用方正是靠这个下标把图和路径对上。
			out.ImagePaths = append(out.ImagePaths, im.path)
		}
	}

	// 文本要在收完路径之后再拼,因为落盘的图片需要在这里告诉模型怎么发出去
	content := []mcp.Content{&mcp.TextContent{Text: composeText(res, out, pickedImgs)}}
	content = append(content, contentImgs...)

	return &mcp.CallToolResult{Content: content}, out, nil
}

// pickedImage 是一张准备作为内容块返回的图片。
type pickedImage struct {
	// url 是这张图的来源地址,也是「清单」里给调用方看的语义标签 ——
	// 路径是 URL 的 sha1,单看文件名认不出是插图还是图标,URL 认得出来。
	url    string
	format string
	width  int
	height int
	mime   string
	data   []byte // 原始图片字节(上 wire 时由 SDK 做 base64)
	// path 是图片落盘后的绝对路径。为空表示没落盘(未启用,或写入失败)——
	// 内容块能让模型看见图,但要让调用方把图发出去还需要一个路径。
	path string
}

// saveImageFile 把图片写到目录,文件名取 URL 的 sha1,避免路径穿越与重名。
func saveImageFile(dir, rawURL, format string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(rawURL))
	name := hex.EncodeToString(sum[:])[:16] + extFromFormat(format)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// extFromFormat 把图片格式转成文件扩展名。
func extFromFormat(format string) string {
	switch strings.ToLower(format) {
	case "png":
		return ".png"
	case "gif":
		return ".gif"
	case "webp":
		return ".webp"
	case "svg":
		return ".svg"
	case "bmp":
		return ".bmp"
	default:
		return ".jpg"
	}
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
	saveFailed := 0

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
		// 图标过滤。
		//
		// 尺寸能解出来时只看尺寸,不看字节:线稿(化学结构式、流程图、示意图)
		// 压得极小 —— 实测同一页里真正该看的 4 张结构图是 1.9–3.7 KB,而站点
		// 页眉那个 LOGO 是 13.4 KB。字节阈值在这里是反着用的:该留的被滤掉,
		// 该扔的放进来。像素尺寸才是"这是不是图标"的正确量具。
		//
		// 尺寸解不出来时才退回字节数:SVG 之类只有体积一个信号。
		hasDims := info.Width > 0 && info.Height > 0
		if hasDims {
			if info.Width < cfg.minImageSide || info.Height < cfg.minImageSide {
				skippedSmall++
				continue
			}
		} else if info.Bytes < cfg.minImageByte {
			skippedSmall++
			continue
		}
		if int64(len(data)) > cfg.maxImageByte {
			skippedBig++
			continue
		}

		// 需要路径时落盘。内容块只能让模型看见图;调用方要把图发给用户,
		// 就得有一个文件路径。
		imgPath := ""
		if cfg.saveImagesDir != "" {
			p, err := saveImageFile(cfg.saveImagesDir, abs, info.Format, data)
			if err != nil {
				saveFailed++
			} else {
				imgPath = p
			}
		}

		picked = append(picked, pickedImage{
			url:    abs,
			format: info.Format,
			width:  info.Width,
			height: info.Height,
			mime:   mimeOf(info.Format),
			// 原始字节,不是 base64 文本。go-sdk 的 ImageContent.Data 是 []byte,
			// 序列化时由 encoding/json 自己 base64 编码上 wire —— 预先编码一次
			// 会让 wire 上出现 base64(base64(png)),接收方解一次只得到一段
			// ASCII,判定为非图片后被丢弃,图片内容块等于没发。
			data: data,
			path: imgPath,
		})
	}

	if saveFailed > 0 {
		notices = append(notices, fmt.Sprintf("%d 张图片写入磁盘失败", saveFailed))
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
func composeText(res *renderer.Result, out fetchOut, imgs []pickedImage) string {
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
	// 图片清单放在最末尾,紧挨着调用方自己追加的东西。
	b.WriteString(imageManifest(imgs))
	// 图片路径作为数据返回,但这里【不】教调用方怎么写 MEDIA:。
	// 那是 Hermes 自己的平台约定,不是 MCP 的一部分 —— 非 Hermes 客户端
	// 看到它只会困惑。怎么用这个路径由客户端自己决定。
	//
	// (内容块已经让模型看见了图;路径是给需要落盘文件的调用方用的。)
	return b.String()
}

// imageManifest 把本次返回的图片列成清单,供调用方按语义取用。
//
// 为什么必须有这份清单:内容块和路径是两条并行的信息,而两边都认不出图是什么。
// 内容块只有字节,没有名字;路径是 URL 的 sha1,看不出是插图还是图标。调用方
// 若只能靠「第几块」对齐,一旦某个环节丢弃或跳过其中一张,编号就全错 ——
// 实测:维基「儿茶酚胺」返回 3 张,某个客户端拒收其中的 SVG 后只附上 2 张,
// 模型随即把站点页眉的 LOGO 当成正文插图发了出去。
//
// URL 与像素尺寸一起给出,是因为这两项通常一眼就能区分图标与插图:同一页里
// 站点 LOGO 是 100×100,而正文的合成通路图是 1000×581。
func imageManifest(imgs []pickedImage) string {
	if len(imgs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## 本页图片\n\n")
	b.WriteString("顺序与本次返回的图片内容块一致。\n\n")
	for i, im := range imgs {
		fmt.Fprintf(&b, "%d. %s", i+1, im.url)
		if d := describeImage(im); d != "" {
			fmt.Fprintf(&b, " [%s]", d)
		}
		b.WriteString("\n")
		if im.path != "" {
			fmt.Fprintf(&b, "   文件:%s\n", im.path)
		} else {
			b.WriteString("   未落盘\n")
		}
	}
	return b.String()
}

// describeImage 给出格式与像素尺寸 —— 判断一张图是不是插图,这两项比字节数可靠。
// 尺寸解不出来时(SVG 等)只给格式。
func describeImage(im pickedImage) string {
	parts := make([]string, 0, 2)
	if im.format != "" {
		parts = append(parts, strings.ToUpper(im.format))
	}
	if im.width > 0 && im.height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", im.width, im.height))
	}
	return strings.Join(parts, " ")
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
