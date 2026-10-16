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

