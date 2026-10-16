package renderer

// <base href> 的浏览器语义测试。
//
// 背景:改造前 qjsdom 一律拿文档地址补全相对地址,而浏览器会用页面里第一个带 href 的
// <base> 顶掉它。忽略它的后果不是"少补一层",而是把相对地址补到站点根上 → 404 →
// 正文空壳。这一组用例把四条语义各钉一条:
//   ① 外链脚本按 <base> 补全(Go 侧)
//   ② 页面脚本的 fetch 按 <base> 补全(宿主桥)
//   ③ location.href 仍是文档地址,不被 <base> 影响
//   ④ 只有第一个带 href 的 <base> 生效;<base href> 自身是相对地址时按文档地址补全

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"qjsdom/internal/fetcher"
)

// 记录服务 起一个会把"被请求过的路径"记下来的测试服务器。
func 记录服务(t *testing.T, 路由 map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var 见过 []string
	var 顺序 []string
	_ = 顺序
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		见过 = append(见过, r.URL.Path)
		mu.Unlock()
		if 体, ok := 路由[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(体))
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), 见过...)
	}
}

func 测试选项带基准() Options {
	fo := fetcher.DefaultOptions()
	fo.AllowPrivate = true
	return Options{Mode: ModeArticle, Selector: "#c", DisableImages: true, FetchOptions: fo}
}

func Test外链脚本按baseHref补全(t *testing.T) {
	srv, 见过 := 记录服务(t, map[string]string{
		"/zh/wiki/app.js": `document.querySelector("#c p").textContent = "底座上的脚本跑过了";`,
	})

	// 页面地址在 /zh/wiki/page,而脚本写的是相对地址 —— 只有把 <base href="/zh/wiki/">
	// 算进去才会打到 /zh/wiki/app.js
	html := `<!DOCTYPE html><html lang="zh"><head><title>base</title>
<base href="/zh/wiki/">
</head><body>
<article id="c"><p>原文本</p></article>
<script src="app.js"></script>
</body></html>`

	res, err := Render(html, srv.URL+"/zh/wiki/page", 测试选项带基准())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "底座上的脚本跑过了") {
		t.Errorf("外链脚本没按 <base href> 补全(正文=%q 请求过=%v errs=%v)",
			res.Text, 见过(), res.ScriptErrs)
	}
	for _, p := range 见过() {
		if p == "/app.js" {
			t.Errorf("脚本被补到了站点根目录(/app.js),<base href> 没生效")
		}
	}
}

func Test页面脚本的fetch按baseHref补全(t *testing.T) {
	srv, 见过 := 记录服务(t, map[string]string{
		"/zh/wiki/data.json": `获取到不该出现的东西`,
	})
	// 用 JSON 端点会走 fetcher 的 HTML 通道,所以这里直接让服务器回一段文本,
	// 页面脚本只断言"请求打到了 /zh/wiki/data.json"。
	html := `<!DOCTYPE html><html lang="zh"><head><title>base</title>
<base href="/zh/wiki/">
</head><body>
<article id="c"><p>原文本</p>
<script>
fetch('data.json').then(function () {
  document.querySelector('#c p').textContent = '请求发出去了';
});
</script>
</article></body></html>`

	if _, err := Render(html, srv.URL+"/zh/wiki/page", 测试选项带基准()); err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	打了 := false
	for _, p := range 见过() {
		if p == "/zh/wiki/data.json" {
			打了 = true
		}
		if p == "/data.json" {
			t.Errorf("页面脚本的 fetch 被补到了站点根目录(/data.json)")
		}
	}
	if !打了 {
		t.Errorf("页面脚本的 fetch 没有按 <base href> 补全,实际请求过 %v", 见过())
	}
}

func Test基准不影响location(t *testing.T) {
	srv, _ := 记录服务(t, nil)
	html := `<!DOCTYPE html><html lang="zh"><head><title>base</title>
<base href="https://cdn.example.com/assets/">
</head><body>
<article id="c"><p>原文本</p>
<script>
document.querySelector('#c p').textContent = 'location=' + String(location.href);
</script>
</article></body></html>`

	文档地址 := srv.URL + "/zh/wiki/page"
	res, err := Render(html, 文档地址, 测试选项带基准())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// 浏览器的分工:<base> 只管"相对地址相对谁",location.href 仍是文档地址
	if !strings.Contains(res.Text, "location="+文档地址) {
		t.Errorf("location.href 不该被 <base href> 影响:正文=%q 期望含 %q", res.Text, 文档地址)
	}
}

func Test只有第一个base生效(t *testing.T) {
	srv, 见过 := 记录服务(t, map[string]string{
		"/第二/":       "",
		"/第一/app.js": `document.querySelector("#c p").textContent = "第一个 base 生效";`,
		"/第二/app.js": `document.querySelector("#c p").textContent = "第二个 base 生效";`,
	})
	html := `<!DOCTYPE html><html lang="zh"><head><title>base</title>
<base href="/第一/">
<base href="/第二/">
</head><body>
<article id="c"><p>原文本</p></article>
<script src="app.js"></script>
</body></html>`

	res, err := Render(html, srv.URL+"/p", 测试选项带基准())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "第一个 base 生效") {
		t.Errorf("应当只有树序中第一个带 href 的 <base> 生效:正文=%q 请求过=%v", res.Text, 见过())
	}
}

func Test相对base按文档地址补全(t *testing.T) {
	srv, 见过 := 记录服务(t, map[string]string{
		"/zh/wiki/子目录/app.js": `document.querySelector("#c p").textContent = "相对 base 生效";`,
	})
	html := `<!DOCTYPE html><html lang="zh"><head><title>base</title>
<base href="子目录/">
</head><body>
<article id="c"><p>原文本</p></article>
<script src="app.js"></script>
</body></html>`

	res, err := Render(html, srv.URL+"/zh/wiki/page", 测试选项带基准())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "相对 base 生效") {
		t.Errorf("<base href> 自身是相对地址时应当按文档地址补全:正文=%q 请求过=%v", res.Text, 见过())
	}
}
