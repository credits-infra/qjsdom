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
	"strings"
	"time"

	"qjsdom/internal/fetcher"
	"qjsdom/internal/renderer"
)

func main() {
	var (
		selector   = flag.String("selector", "", "正文容器 CSS 选择器(留空则自动探测)")
		maxLen     = flag.Int("max", 0, "正文截断长度,0=不截断")
		asJSON     = flag.Bool("json", false, "以 JSON 输出(含脚本执行统计、链接、图片)")
		filePath   = flag.String("file", "", "渲染本地 HTML 文件,不联网")
		noScripts  = flag.Bool("no-scripts", false, "不执行页面内联脚本(仅静态解析)")
		stats      = flag.Bool("stats", false, "把诊断信息打印到 stderr")
		allowPriv  = flag.Bool("allow-private", false, "允许访问内网地址(仅本地测试)")
		timeout    = flag.Duration("timeout", 20*time.Second, "抓取超时")
		showVer    = flag.Bool("version", false, "打印版本")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(renderer.EngineVersion)
		return
	}

	var (
		html    string
		pageURL string
	)

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
		pageURL = flag.Arg(0)

		fo := fetcher.DefaultOptions()
		fo.Timeout = *timeout
		fo.AllowPrivate = *allowPriv

		res, err := fetcher.Fetch(pageURL, fo)
		if err != nil {
			fatal("%v", err)
		}
		html = res.HTML
		pageURL = res.URL
		if *stats {
			fmt.Fprintf(os.Stderr, "[fetch]  HTTP %d  %.1f KB  %.2fs  %s\n",
				res.StatusCode, float64(res.Bytes)/1024, res.Elapsed.Seconds(), res.URL)
		}
	}

	out, err := renderer.Render(html, pageURL, renderer.Options{
		Selector:   *selector,
		MaxTextLen: *maxLen,
		RunScripts: !*noScripts,
	})
	if err != nil {
		fatal("渲染失败: %v", err)
	}

	if *stats {
		fmt.Fprintf(os.Stderr, "[render] %s\n", renderer.EngineVersion)
		fmt.Fprintf(os.Stderr, "[render] 耗时 %.2fs | 脚本 %d/%d 执行成功 | 正文 %d 字\n",
			out.Elapsed.Seconds(), out.ScriptsRan, out.ScriptTags, out.TextLen)
		if len(out.ScriptErrs) > 0 {
			fmt.Fprintf(os.Stderr, "[render] 脚本报错 %d 个,前 3 个:\n", len(out.ScriptErrs))
			for i, e := range out.ScriptErrs {
				if i >= 3 {
					break
				}
				fmt.Fprintf(os.Stderr, "         - %s\n", e)
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
	fmt.Println(out.Text)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "qjsdom: "+format+"\n", args...)
	os.Exit(1)
}
