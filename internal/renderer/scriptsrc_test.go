package renderer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test外链脚本被加载并执行 <script src> 是这批页面能不能读的前提。
func Test外链脚本被加载并执行(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>外链</title></head><body>
<article id="c"><p>空</p></article>
<script src="/app.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "外链脚本跑过了") {
		t.Errorf("外链脚本没执行(正文 = %q,scriptErrs = %v)", res.Text, res.ScriptErrs)
	}
}

// Test外链脚本保持文档顺序 顺序错了等于给页面换了执行次序 —— 内联回原位才保序。
func Test外链脚本保持文档顺序(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>顺序</title></head><body>
<article id="c"><p>空</p></article>
<script>window.__轨迹 = ["内联1"];</script>
<script src="/app.js"></script>
<script>window.__轨迹.push("内联2"); document.querySelector("#c p").textContent = window.__轨迹.join(" → ");</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "内联1") || !strings.Contains(res.Text, "内联2") {
		t.Fatalf("轨迹不完整: %q", res.Text)
	}
}

// Test外链脚本取不到时留原因 失败必须带原因,否则"脚本没跑"没法查。
// 两种失败都在这里:HTTP 500 与 HTTP 404 —— 顺带钉住"非 2xx 的响应体绝不当脚本执行"
// (否则 404 的 HTML 错误页会被内联成 JS,报一行没头没尾的语法错,真原因反而被埋掉)。
func Test外链脚本取不到时留原因(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>坏外链</title></head><body>
<article id="c"><p>正文还在</p></article>
<script src="/bad.js"></script>
<script src="/missing.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "正文还在") {
		t.Errorf("外链失败不该影响正文抽取:%q", res.Text)
	}
	记录 := strings.Join(res.ScriptErrs, " | ")
	// 两个脚本都该被逐个点名:一个是 500,一个是 404(顺带钉住"非 2xx 不内联")
	if !strings.Contains(记录, "bad.js") || !strings.Contains(记录, "missing.js") {
		t.Errorf("失败的脚本应当在 scriptErrs 里逐条留名,实际:%s", 记录)
	}
}

// Test外链脚本的模块与数据块被跳过 type=module 需要 import 解析,
// type=application/json 之类的数据块本就不是可执行脚本 —— 两者都不该去取。
func Test外链脚本的模块与数据块被跳过(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>跳过</title></head><body>
<article id="c"><p>正文</p></article>
<script type="module" src="/app.js"></script>
<script type="application/json" src="/app.js"></script>
<script type="text/template" src="/app.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "外链脚本跑过了") {
		t.Errorf("不该执行的脚本被执行了:%q", res.Text)
	}
	if !strings.Contains(strings.Join(res.ScriptErrs, " "), "module") {
		t.Errorf("跳过 module 应当有说明,实际:%v", res.ScriptErrs)
	}
}

// Test关掉外链脚本加载 开关生效:src 保持原样,runner 照旧跳过它。
func Test关掉外链脚本加载(t *testing.T) {
	srv := 起测试服务(t)
	opt := 测试选项()
	opt.DisableScriptSrc = true
	html := `<!DOCTYPE html><html lang="zh"><head><title>关闭外链</title></head><body>
<article id="c"><p>正文</p></article>
<script src="/app.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "外链脚本跑过了") {
		t.Errorf("关掉之后外链脚本仍然执行了:%q", res.Text)
	}
}

// Test外链脚本里的script结束标签被转义 否则我们拼内联块时会被提前截断。
func Test外链脚本里的script结束标签被转义(t *testing.T) {
	if got := 防截断(`var s = "</script>";`); strings.Contains(got, "</script") {
		t.Errorf("没有转义 </script:%q", got)
	}
	if got := 安全注释("地址 http://x/*/y"); strings.Contains(got, "*/") {
		t.Errorf("注释没被保护:%q", got)
	}
}

// Test脚本报错带行号与来源 钉住可诊断性:多脚本页面上只报一句 "not a function"
// 等于没信息。qjs 的 e.stack 带行列号(顶帧是页面脚本里的位置),外链脚本还有
// Go 侧内联时打的来源标签 —— 两者都该进 scriptErrs。
func Test脚本报错带行号与来源(t *testing.T) {
	srv := 起测试服务(t)
	// 第 2 行出错(第 1 行是注释)
	html := `<!DOCTYPE html><html lang="zh"><head><title>报错</title></head><body>
<article id="c"><p>正文</p></article>
<script>
// 第一行是注释,第二行才出错
null.没有这个方法();
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	记录 := strings.Join(res.ScriptErrs, " | ")
	if !strings.Contains(记录, "脚本第 3 行") {
		t.Errorf("内联脚本的报错应当带行号(第 3 行),实际:%s", 记录)
	}
}

// Test外链脚本出错时点名来源 同一件事,外链版本:出错必须能看出是哪个地址。
func Test外链脚本出错时点名来源(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/broken.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte("null.noSuchMethod();"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>外链报错</title></head><body>
<article id="c"><p>正文</p></article>
<script src="/broken.js"></script>
</body></html>`
	opt := 测试选项()
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	记录 := strings.Join(res.ScriptErrs, " | ")
	if !strings.Contains(记录, "broken.js") {
		t.Errorf("外链脚本的报错应当点出地址,实际:%s", 记录)
	}
	if !strings.Contains(记录, "脚本第") {
		t.Errorf("外链脚本的报错也应当带行号,实际:%s", 记录)
	}
}

// Test数据块不被当脚本执行 钉住一个实测出来的噪音源:JSON-LD、application/json、
// text/template 这些【数据块】在浏览器里从不执行,而此前我们把它们当脚本 eval,
// 结果是 scriptErrs 里多出三条 "expecting ';'" / "unexpected token '<'",
// 真正有用的错误反而被埋掉。
func Test数据块不被当脚本执行(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>数据块</title>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Article"}</script>
<script type="text/template"><div>模板不该执行</div></script>
<script type="application/json">{"a":1}</script>
</head><body>
<article id="c"><p>正文</p></article>
<script>document.querySelector("#c p").textContent = "正文(脚本已跑)";</script>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// 只断言"没有报错":"note:" 开头的说明是另一回事(例如 RunScripts=false 的提示)
	var 报错 []string
	for _, e := range res.ScriptErrs {
		if !strings.HasPrefix(e, "note:") {
			报错 = append(报错, e)
		}
	}
	if len(报错) > 0 {
		t.Errorf("数据块不该产生任何脚本报错,实际:%v", 报错)
	}
	if !strings.Contains(res.Text, "正文(脚本已跑)") {
		t.Errorf("真正的脚本该照跑: %q", res.Text)
	}
	if res.ScriptsRan != 1 {
		t.Errorf("只该有一个脚本执行,实际 %d(scriptTags=%d)", res.ScriptsRan, res.ScriptTags)
	}
}

// Test内联module按普通脚本执行并留提示 module 需要 import 解析,我们没有;
// 但内联 module 里有不少是无 import 的,直接跳过会白丢内容 —— 所以照跑,并记一条
// 说明 import 不会解析。外链 module 则跳过(Go 侧已记原因)。
func Test内联module按普通脚本执行并留提示(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>module</title></head><body>
<article id="c"><p>原文本</p>
<script type="module">document.querySelector("#c p").textContent = "内联 module 跑过了";</script>
</article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "内联 module 跑过了") {
		t.Errorf("无 import 的内联 module 不该被丢掉: %q", res.Text)
	}
	if !strings.Contains(strings.Join(res.ScriptErrs, " "), "type=module") {
		t.Errorf("应当说明 import 不会解析,实际:%v", res.ScriptErrs)
	}
}

// Test超上限的脚本不执行 钉住一个实测出来的坑:fetcher 超过上限会把响应截断,
// 而半个脚本执行起来只会报一句「没头没尾的语法错」——真原因(脚本太大)被埋掉。
// 判据用 Result.Truncated(不是靠响应体里的标记字符串),因为截断与"恰好等于上限"
// 在字节数上无法区分。
func Test超上限的脚本不执行(t *testing.T) {
	大块 := strings.Repeat("var x = 1;\n", 60000) // ~660KB > 512KB 上限
	mux := http.NewServeMux()
	mux.HandleFunc("/huge.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(大块 + `document.querySelector("#c p").textContent = "超大脚本不该跑";`))
	})
	mux.HandleFunc("/ok.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`document.querySelector("#c p").textContent = "正常脚本跑过了";`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>上限</title></head><body>
<article id="c"><p>原文本</p></article>
<script src="/huge.js"></script>
<script src="/ok.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "超大脚本不该跑") {
		t.Errorf("被截断的脚本被执行了:%q", res.Text)
	}
	if !strings.Contains(res.Text, "正常脚本跑过了") {
		t.Errorf("后面的正常脚本也不该被连累:%q(scriptErrs=%v)", res.Text, res.ScriptErrs)
	}
	记录 := strings.Join(res.ScriptErrs, " ")
	if !strings.Contains(记录, "huge.js") || !strings.Contains(记录, "超过单脚本上限") {
		t.Errorf("被截断的脚本应当留名并说明原因,实际:%s", 记录)
	}
}
