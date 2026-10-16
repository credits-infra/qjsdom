// qjsdom —— 纯 Go 的网页渲染与正文提取(QuickJS + linkedom,无 Node、无浏览器)。
//
// 用法:
//
//	qjsdom https://example.com                       抓取并提取正文
//	qjsdom -selector "article" https://example.com   指定正文容器
//	qjsdom -file testdata/wiki.html                  渲染本地 HTML(不联网)
//	qjsdom -json https://example.com                 以 JSON 输出完整结果
//	qjsdom -stats https://example.com                附带诊断信息到 stderr
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qjsdom/internal/cache"
	"qjsdom/internal/fetcher"
	"qjsdom/internal/renderer"
)

func main() {
	var (
		modeFlag    = flag.String("mode", "all", "提取策略: all(整页,默认) / article(正文) / links(链接列表)")
		selector    = flag.String("selector", "", "正文容器 CSS 选择器(对 all/article 生效)")
		maxLen      = flag.Int("max", 0, "正文截断长度,0=不截断")
		asJSON      = flag.Bool("json", false, "以 JSON 输出(含脚本执行统计、链接、图片)")
		markdown    = flag.Bool("markdown", false, "输出 Markdown(保留标题/列表/表格等结构)")
		filePath    = flag.String("file", "", "渲染本地 HTML 文件,不联网")
		noScripts   = flag.Bool("no-scripts", false, "不执行页面内联脚本(仅静态解析)")
		noImages    = flag.Bool("no-images", false, "不启用 Image 网络探测(不做图片 fetch)")
		noJSFetch   = flag.Bool("no-js-fetch", false, "不让页面脚本联网(关掉 fetch / XMLHttpRequest)")
		noScriptSrc = flag.Bool("no-script-src", false, "不加载 <script src> 外部脚本")
		jsBudget    = flag.Duration("js-fetch-budget", 0,
			"页面脚本联网的总时间预算(默认 10s;0 = 用默认值)")
		dlImages     = flag.Bool("download-images", false, "下载页面图片到 -images-dir(交给模型看)")
		imgDir       = flag.String("images-dir", "", "图片保存目录(配合 -download-images)")
		maxImages    = flag.Int("max-images", 20, "最多下载几张图片")
		stats        = flag.Bool("stats", false, "把诊断信息打印到 stderr")
		allowPriv    = flag.Bool("allow-private", false, "允许访问内网地址(仅本地测试)")
		allowSchemes = flag.String("allow-schemes", "",
			"额外放行的 URL 协议,逗号分隔(默认只允许 http/https;如 file 读本机文件)")
		timeout    = flag.Duration("timeout", 20*time.Second, "抓取超时")
		useCache   = flag.Bool("cache", false, "启用页面源码缓存(同一 URL 换 mode 重试时不必重新抓取)")
		cacheDir   = flag.String("cache-dir", "", "缓存目录(默认:用户缓存目录/qjsdom)")
		cacheTTL   = flag.Duration("cache-ttl", cache.DefaultTTL, "缓存有效期(默认 10m)")
		cacheMax   = flag.Int64("cache-max", cache.DefaultMaxBytes, "缓存总容量上限,字节(默认 64MB)")
		cacheClear = flag.Bool("cache-clear", false, "清空缓存后退出")
		showVer    = flag.Bool("version", false, "打印版本")
	)
	// Go 的 flag 包在遇到第一个非 flag 参数时就停止解析:
	//   qjsdom <url> -json   → -json 被静默丢掉
	//   qjsdom -json <url>   → 正常
	// 这个坑太隐蔽,所以先把参数重排成「flags 在前、位置参数在后」。
	// 注意必须放在所有 flag.Xxx() 定义之后。
	flag.CommandLine.Parse(reorderArgs(os.Args[1:]))

	if *showVer {
		fmt.Println(renderer.EngineVersion)
		return
	}

	var (
		html    string
		pageURL string
	)

	// 网络参数在两条路径(联网抓取 / 本地文件)都要用 —— 后者用于图片探测
	fetchOpts := fetcher.DefaultOptions()
	fetchOpts.Timeout = *timeout
	fetchOpts.AllowPrivate = *allowPriv
	fetchOpts.AllowSchemes = fetcher.SplitSchemes(*allowSchemes)

	// 缓存:两道防线(TTL + 容量上限)在 cache.Purge 里,写入前自动执行。
	var c *cache.Cache
	if *useCache || *cacheClear {
		c = cache.New(cache.Options{Dir: *cacheDir, TTL: *cacheTTL, MaxBytes: *cacheMax})
	}
	if *cacheClear {
		if err := c.Clear(); err != nil {
			fatal("清空缓存失败: %v", err)
		}
		fmt.Fprintf(os.Stderr, "已清空缓存: %s\n", c.Dir())
		return
	}

	if *filePath != "" {
		b, err := os.ReadFile(*filePath)
		if err != nil {
			fatal("读取文件失败: %v", err)
		}
		html = string(b)
		pageURL = "file://" + *filePath
	} else {
		if flag.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "用法: qjsdom [flags] <url>")
			flag.PrintDefaults()
			os.Exit(2)
		}
		reqURL := flag.Arg(0)
		pageURL = reqURL

		// 缓存命中:抓取与渲染是分开的两步,缓存的是 HTML 源码,
		// 所以换 mode 重试时只需重跑渲染,不必重新抓取。
		if c != nil {
			if h, ok := c.Get(reqURL); ok {
				html = h
				if *stats {
					fmt.Fprintf(os.Stderr, "[cache]  命中  %s\n", c.Dir())
				}
			}
		}
		if html == "" {
			res, err := fetcher.Fetch(reqURL, fetchOpts)
			if err != nil {
				fatal("%v", err)
			}
			html = res.HTML
			pageURL = res.URL
			if *stats {
				fmt.Fprintf(os.Stderr, "[fetch]  HTTP %d  %.1f KB  %.2fs  %s\n",
					res.StatusCode, float64(res.Bytes)/1024, res.Elapsed.Seconds(), res.URL)
			}
			if c != nil {
				if err := c.Put(reqURL, html); err != nil && *stats {
					fmt.Fprintf(os.Stderr, "[cache]  未缓存: %v\n", err)
				}
			}
		}
	}

	// 下载图片需要明确的保存目录 —— 不默认写到当前工作目录,避免意外落文件
	if *dlImages && *imgDir == "" {
		fatal("-download-images 需要配合 -images-dir 指定保存目录")
	}

	out, err := renderer.Render(html, pageURL, renderer.Options{
		Mode:             renderer.ParseMode(*modeFlag),
		Selector:         *selector,
		MaxTextLen:       *maxLen,
		Markdown:         *markdown,
		RunScripts:       !*noScripts,
		DisableImages:    *noImages,
		DisableJSFetch:   *noJSFetch,
		DisableScriptSrc: *noScriptSrc,
		JSFetchBudget:    *jsBudget,
		FetchOptions:     fetchOpts,
		DownloadImages:   *dlImages,
		ImagesDir:        *imgDir,
		ImageLimits:      fetcher.DownloadLimits{MaxImages: *maxImages},
	})
	if err != nil {
		fatal("渲染失败: %v", err)
	}

	if *stats {
		fmt.Fprintf(os.Stderr, "[render] %s\n", renderer.EngineVersion)
		fmt.Fprintf(os.Stderr, "[render] 耗时 %.2fs | 脚本 %d/%d 执行成功 | 正文 %d 字\n",
			out.Elapsed.Seconds(), out.ScriptsRan, out.ScriptTags, out.TextLen)
		if *markdown {
			fmt.Fprintf(os.Stderr, "[render] Markdown %d 字\n", len([]rune(out.Markdown)))
		}
		if len(out.ScriptErrs) > 0 {
			fmt.Fprintf(os.Stderr, "[render] 脚本报错 %d 个,前 3 个:\n", len(out.ScriptErrs))
			for i, e := range out.ScriptErrs {
				if i >= 3 {
					break
				}
				fmt.Fprintf(os.Stderr, "         - %s\n", e)
			}
		}
		if len(out.ImageFiles) > 0 {
			okN := 0
			for _, im := range out.ImageFiles {
				if im.Path != "" {
					okN++
				}
			}
			fmt.Fprintf(os.Stderr, "[images] 下载成功 %d/%d 张\n", okN, len(out.ImageFiles))
			// 成功的只列前 5 条;失败的【全部】列出 ——
			// 失败信息才是排查时要看的,截断掉等于没说。
			shownOK := 0
			for _, im := range out.ImageFiles {
				if im.Path != "" {
					if shownOK < 5 {
						fmt.Fprintf(os.Stderr, "         ✅ %s  %dx%d  %d KB\n",
							filepath.Base(im.Path), im.Width, im.Height, im.Bytes>>10)
						shownOK++
					}
					continue
				}
				fmt.Fprintf(os.Stderr, "         ❌ %s  → %s\n", shorten(im.URL, 50), im.Error)
			}
			if okN > shownOK {
				fmt.Fprintf(os.Stderr, "         …另有 %d 张成功(未列出)\n", okN-shownOK)
			}
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(out); err != nil {
			fatal("JSON 输出失败: %v", err)
		}
		return
	}

	// 默认输出:标题 + 正文
	if t := strings.TrimSpace(out.Title); t != "" {
		fmt.Println("# " + t)
		fmt.Println()
	}
	if !*markdown {
		fmt.Println(out.Text)
		return
	}
	if strings.TrimSpace(out.Markdown) == "" {
		// 转换失败时不给一份空输出:退回纯文本并说明原因(renderer 会在
		// scriptErrs 里留下失败原因,-stats 能看到)。
		fmt.Fprintln(os.Stderr, "qjsdom: Markdown 转换没有产出内容,已退回纯文本")
		fmt.Println(out.Text)
		return
	}
	fmt.Println(out.Markdown)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "qjsdom: "+format+"\n", args...)
	os.Exit(1)
}

// shorten 截断字符串,用于日志输出。
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// reorderArgs 让 flags 可以写在位置参数之后。
//
// Go 标准库的 flag 包在遇到第一个非 flag 参数时停止解析,于是
// `qjsdom <url> -json` 里的 -json 会被静默丢弃 —— 这个坑很隐蔽。
// 这里把参数重排成「所有 flags 在前、位置参数在后」。
//
// 注意:需要维护 valueFlags 集合,否则无法判断某个 flag 后面跟的是
// 它的值还是位置参数。
func reorderArgs(args []string) []string {
	valueFlags := map[string]bool{
		"-mode":            true,
		"-selector":        true,
		"-max":             true,
		"-file":            true,
		"-timeout":         true,
		"-images-dir":      true,
		"-max-images":      true,
		"-allow-schemes":   true,
		"-cache-dir":       true,
		"-cache-ttl":       true,
		"-cache-max":       true,
		"-js-fetch-budget": true,
	}

	var flags, positional []string
	有分隔符 := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// 分隔符本身必须留在结果里:flag.Parse 靠它知道"后面全不是 flag"。
			// 丢掉它的话,`--` 之后那个像 flag 的位置参数会被 flag.Parse 当 flag
			// 处理(实测:`qjsdom -json -- -max` 等价于 `-json -max`,而后者报
			// `flag needs an argument: -max`)。
			有分隔符 = true
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// 形如 -max=100 自带值;否则看它是否需要下一个参数作为值
			if !strings.Contains(a, "=") && valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	if 有分隔符 {
		return append(append(flags, "--"), positional...)
	}
	return append(flags, positional...)
}
