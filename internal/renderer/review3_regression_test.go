package renderer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 交叉评审(第三轮)报出的三条问题,先写成回归测试 —— 它们在修复前应当是【失败】的。
// 报告与实测证据:/tmp/qjsdom_review2/report.md(A2/B4/D2/D3 条目)。

// Test被跳过的外链脚本不被JS通路取回 —— 第三轮评审的头号发现(A2/B4,高)。
//
// 症状:Go 侧判定"不执行"的外链脚本(404 / text-html / 超上限 / 第 11 个),
// 标签原样留在 DOM 里;runner 的分轮执行看到它带 src,就通过 JS 侧 fetch 取回来
// 又执行了一遍。于是 scriptErrs 一边写着"取不到/未执行",一边那个脚本真的跑了。
// 判据:正文里绝不能出现 404 响应体写进去的标记。
func Test被跳过的外链脚本不被JS通路取回(t *testing.T) {
	mux := http.NewServeMux()
	// 404 但响应体是一段合法 JS(真实世界常见:错误页或反爬页)
	mux.HandleFunc("/e404.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.WriteHeader(404)
		w.Write([]byte(`document.querySelector("#c p").textContent = "404 响应体不该被执行";`))
	})
	// text/html 的"脚本"
	mux.HandleFunc("/ashtml.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<p>错误页</p>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>跳过的脚本</title></head><body>
<article id="c"><p>原文本</p></article>
<script src="/e404.js"></script>
<script src="/ashtml.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "404 响应体不该被执行") {
		t.Errorf("Go 侧已判定不执行的外链脚本,被 JS 通路取回执行了:%q", res.Text)
	}
	if res.ScriptsRan != 0 {
		t.Errorf("不该有脚本执行成功,实际 ran=%d(scriptTags=%d) errs=%v",
			res.ScriptsRan, res.ScriptTags, res.ScriptErrs)
	}
}

// Test超上限被跳过的脚本不被JS通路取回 —— 同一条通路的另一个分支(A2)。
// 上一轮刚补了"被截断的脚本不执行",但那条限制同样会被 JS 通路绕过。
func Test超上限被跳过的脚本不被JS通路取回(t *testing.T) {
	大块 := strings.Repeat("var pad = 1;\n", 60000) // ~660KB > 512KB
	mux := http.NewServeMux()
	mux.HandleFunc("/huge.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(大块 + `document.querySelector("#c p").textContent = "超大脚本不该跑";`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>超大</title></head><body>
<article id="c"><p>原文本</p></article>
<script src="/huge.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "超大脚本不该跑") {
		t.Errorf("被截断/超上限的脚本被 JS 通路取回执行了:%q", res.Text)
	}
}

// Test内联脚本块的结束标签大小写不敏感 —— D3(高)。
// 我们把外链脚本源码内联进 <script>…</script>:HTML 解析器在 `</script` 处结束
// 该元素,而且**大小写不敏感**。之前的转义只替换小写 `</script`,所以源码里出现
// `</SCRIPT>` 时内联块被提前截断:后面的代码当成文本漏进 DOM,脚本本身报语法错。
// 判据:①那段本该死掉的标记不能出现在正文里;②脚本要能正常跑完。
func Test内联脚本块的结束标签大小写不敏感(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/case.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		// 源码里带着大写结束标签(真实站点写 document.write('<SCRIPT>…') 时就会出现)
		w.Write([]byte(`var 标记 = "</SCRIPT>请勿泄漏到正文</SCRIPT>";
document.querySelector("#c p").textContent = "大写结束标签下的脚本跑完了";`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>大小写</title></head><body>
<article id="c"><p>原文本</p></article>
<script src="/case.js"></script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "请勿泄漏到正文") {
		t.Errorf("内联块被 </SCRIPT> 提前截断,源码泄漏成了正文:%q", res.Text)
	}
	if !strings.Contains(res.Text, "大写结束标签下的脚本跑完了") {
		t.Errorf("脚本没能跑完(正文=%q errs=%v)", res.Text, res.ScriptErrs)
	}
}

// Test标签名大小写不绕开数量上限 —— 第三轮【复核】新发现(不在评审报告里)。
//
// 症状:`展开外部脚本` 开头用 strings.Contains(html, "<script") 做早退,而它是
// 大小写敏感的 —— 页面把标签写成 <ScRiPt src=...> / <SCRIPT SRC=...> 时,
// 整个外链脚本能力会【静默失效】:既不内联,也没有一条 note。
// 后果实测:12 个 <ScRiPt src> 时,超过数量上限(maxScriptSrcCount=10)的第 11、12 个
// 照跑(正文 n=12);根因修掉之后,三种大小写都恰好 n=10。
func Test标签名大小写不绕开数量上限(t *testing.T) {
	mux := http.NewServeMux()
	for i := 0; i < 12; i++ {
		mux.HandleFunc("/s"+string(rune('a'+i))+".js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(`window.__n=(window.__n||0)+1;` +
				`document.querySelector("#c p").textContent="n="+window.__n;`))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	情形 := []struct{ 名, 开, 闭 string }{
		{"小写", `<script src="/s`, `.js"></script>`},
		{"标签大写", `<SCRIPT SRC="/s`, `.js"></SCRIPT>`},
		{"混合大小写", `<ScRiPt src="/s`, `.js"></ScRiPt>`},
	}
	for _, c := range 情形 {
		var b strings.Builder
		b.WriteString(`<!DOCTYPE html><html lang="zh"><head><title>上限</title></head><body>` +
			`<article id="c"><p>原文本</p></article>`)
		for i := 0; i < 12; i++ {
			b.WriteString(c.开 + string(rune('a'+i)) + c.闭)
		}
		b.WriteString(`</body></html>`)

		res, err := Render(b.String(), srv.URL+"/p", 测试选项())
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", c.名, err)
		}
		if !strings.Contains(res.Text, "n=10") {
			t.Errorf("%s:应当恰好执行 %d 个外链脚本,实际正文 %q(scriptErrs=%v)",
				c.名, maxScriptSrcCount, res.Text, res.ScriptErrs)
		}
	}
}

// Test大写属性名的外链脚本照常执行 —— 同一个早退 bug 的另一面。
// 属性名本来就大小写不敏感(属性值 用 EqualFold),<SCRIPT SRC="x.js"> 在浏览器里
// 会执行;而早退是大小写敏感的,于是它以前【既不执行、也不报错】—— 静默丢内容。
func Test大写属性名的外链脚本照常执行(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`document.querySelector("#c p").textContent = "大写标签的脚本跑过了";`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, 标签 := range []string{
		`<SCRIPT SRC="/ok.js"></SCRIPT>`,
		`<ScRiPt sRc="/ok.js"></ScRiPt>`,
		`<script SRC="/ok.js"></script>`,
	} {
		html := `<!DOCTYPE html><html lang="zh"><head><title>大写</title></head><body>` +
			`<article id="c"><p>原文本</p></article>` + 标签 + `</body></html>`
		res, err := Render(html, srv.URL+"/p", 测试选项())
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", 标签, err)
		}
		if !strings.Contains(res.Text, "大写标签的脚本跑过了") {
			t.Errorf("%s:标签名/属性名的大小写不该让脚本静默不执行,正文=%q(scriptErrs=%v)",
				标签, res.Text, res.ScriptErrs)
		}
	}
}

// Test页面脚本看到的UA与抓取一致 —— 第三轮评审 A5(低)的加严版。
//
// 症状:page_runner 用 `globalThis.navigator = {...}` 给页面脚本设 UA,而 linkedom
// 把 navigator 定义成只读访问器属性 —— 赋值静默失效(实测页面脚本读到的是引擎默认的
// quickjs-ng/0.10.1,language 是 undefined)。而且它与抓取用的 UA 是两套值。
// 这里把"一致性"本身钉住:页面脚本读到的 UA,必须等于我们真正发出去的 UA。
func Test页面脚本看到的UA与抓取一致(t *testing.T) {
	var 抓取UA string
	mux := http.NewServeMux()
	mux.HandleFunc("/whoami.json", func(w http.ResponseWriter, r *http.Request) {
		抓取UA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>UA</title></head><body>
<article id="c"><p>原文本</p>
<script>
fetch('/whoami.json').then(function (r) { return r.json(); }).then(function () {
  document.querySelector('#c p').textContent =
    '页面UA=' + String(navigator.userAgent) + ' 语言=' + String(navigator.language);
});
</script>
</article></body></html>`

	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if 抓取UA == "" {
		t.Fatalf("页面脚本没发出请求,这轮测不到一致性(正文=%q errs=%v)", res.Text, res.ScriptErrs)
	}
	if !strings.Contains(res.Text, "页面UA="+抓取UA) {
		t.Errorf("页面脚本看到的 UA 与抓取用的 UA 不一致:正文=%q 抓取UA=%q", res.Text, 抓取UA)
	}
	if strings.Contains(res.Text, "quickjs-ng") {
		t.Errorf("暴露了引擎身份(赋值没生效):%q", res.Text)
	}
	if !strings.Contains(res.Text, "语言=zh-CN") {
		t.Errorf("navigator.language 没设上:%q", res.Text)
	}
}
