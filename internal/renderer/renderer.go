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
	"strings"
	"time"

	"github.com/fastschema/qjs"
)

//go:embed js/host_shims.js
var hostShims string

//go:embed js/page_runner.js
var pageRunner string

//go:embed assets/linkedom.core.js
var linkedomBundle string

// Options 控制单次渲染行为。
type Options struct {
	// Selector 指定正文容器(留空则按常见文章容器自动探测)。
	Selector string
	// MaxTextLen 截断正文长度(0 = 不截断)。
	MaxTextLen int
	// RunScripts 是否执行页面内联脚本(默认 true)。
	RunScripts bool
}

// Result 是渲染结果。字段名对应 page_runner.js 的返回 JSON。
type Result struct {
	Title      string   `json:"title"`
	Text       string   `json:"text"`
	TextLen    int      `json:"textLen"`
	ScriptTags int      `json:"scriptTags"`
	ScriptsRan int      `json:"scriptsRan"`
	ScriptErrs []string `json:"scriptErrs"`
	Links      []string `json:"links"`
	Images     []string `json:"images"`
	HasBody    bool     `json:"hasBody"`
	// Degraded 为 true 表示 DOM 解析失败、退回了粗暴的文本提取。
	// 这种情况仍然会返回结果(而不是报错),调用方应按需降级处理。
	Degraded bool `json:"degraded"`

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

	if _, err := ctx.Eval("host_shims.js", qjs.Code(hostShims)); err != nil {
		return nil, fmt.Errorf("宿主 shim 注入失败: %w", err)
	}
	if _, err := ctx.Eval("linkedom.js", qjs.Code(linkedomBundle)); err != nil {
		return nil, fmt.Errorf("linkedom 注入失败: %w", err)
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
	return &out, nil
}

// buildRunner 把三个占位符替换为 JSON 字面量。
// 用 JSON 编码而不是裸字符串拼接 —— HTML 里任何引号/反斜杠/换行都会被正确转义。
func buildRunner(html, pageURL string, opt Options) string {
	htmlLit, _ := json.Marshal(html)
	urlLit, _ := json.Marshal(pageURL)
	selLit, _ := json.Marshal(opt.Selector)

	s := pageRunner
	s = strings.ReplaceAll(s, "__HTML__", string(htmlLit))
	s = strings.ReplaceAll(s, "__URL__", string(urlLit))
	s = strings.ReplaceAll(s, "__SELECTOR__", string(selLit))
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
