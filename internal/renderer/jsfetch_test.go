package renderer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qjsdom/internal/fetcher"
)

// 起一个本地测试服务器:页面脚本要取的数据、以及外链脚本都从这里来。
// 测试用 AllowPrivate 放行 127.0.0.1 —— 守卫本身另有专门的用例去钉。
func 起测试服务(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Test-Header", "有")
		w.Write([]byte(`{"v":"糖酵解","n":10}`))
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`document.querySelector("#c p").textContent = "外链脚本跑过了";`))
	})
	mux.HandleFunc("/bad.js", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func 测试选项() Options {
	fo := fetcher.DefaultOptions()
	fo.AllowPrivate = true // 测试服务器在 127.0.0.1
	return Options{Mode: ModeArticle, Selector: "#c", DisableImages: true, FetchOptions: fo}
}

// Test页面脚本取数进正文 是这套能力的核心用例:
// 正文由脚本取回来的数据渲染 —— 而且它必须在【抽取之前】完成
// (靠的是 renderer 的两阶段 eval,见 microtask_test.go)。
func Test页面脚本取数进正文(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>取数</title></head><body>
<article id="c"><p>加载中…</p></article>
<script>
fetch('/api.json')
  .then(function (r) {
    if (!r.ok) throw new Error('状态 ' + r.status);
    return r.json();
  })
  .then(function (d) { document.querySelector('#c p').textContent = '接口返回:' + d.v; })
  .catch(function (e) { document.querySelector('#c p').textContent = '失败:' + e.message; });
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/page", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "接口返回:糖酵解") {
		t.Errorf("脚本取回的数据没进正文(正文 = %q,scriptErrs = %v)", res.Text, res.ScriptErrs)
	}
}

// Test页面脚本看得到响应头与状态 Response 上该有的东西得真有:
// 状态、ok、url、headers.get() —— 页面脚本按 content-type 分支是常见写法。
func Test页面脚本看得到响应头与状态(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>响应对象</title></head><body>
<article id="c"><p>…</p></article>
<script>
fetch('api.json').then(function (r) {
  document.querySelector('#c p').textContent =
    'status=' + r.status + '|ok=' + r.ok + '|ct=' + r.headers.get('content-type') +
    '|x=' + r.headers.get('x-test-header') + '|url=' + r.url;
});
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	for _, 想要 := range []string{"status=200", "ok=true", "application/json", "有", "/api.json"} {
		if !strings.Contains(res.Text, 想要) {
			t.Errorf("响应对象缺 %q(正文 = %q)", 想要, res.Text)
		}
	}
}

// Test页面脚本不能绕过SSRF守卫 是安全用例:页面能读什么 = 攻击者的 JS 能扫什么。
// 这里 HostAllowPrivate=false,脚本去 fetch 127.0.0.1 —— 必须被守卫挡住,
// 而且拒绝原因要能传到脚本的 catch 里。
func Test页面脚本不能绕过SSRF守卫(t *testing.T) {
	srv := 起测试服务(t)
	opt := 测试选项()
	opt.FetchOptions.AllowPrivate = false // 守卫生效
	html := `<!DOCTYPE html><html lang="zh"><head><title>打内网</title></head><body>
<article id="c"><p>…</p></article>
<script>
fetch('http://127.0.0.1:9/secret')
  .then(function (r) { return r.text(); })
  .then(function () { document.querySelector('#c p').textContent = '不该发生:拿到内网数据'; })
  .catch(function (e) { document.querySelector('#c p').textContent = '被拒:' + e.message; });
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "不该发生") {
		t.Fatalf("页面脚本绕过了 SSRF 守卫:%q", res.Text)
	}
	if !strings.Contains(res.Text, "被拒:") || !strings.Contains(res.Text, "SSRF") {
		t.Errorf("应当看到守卫的拒绝原因(正文 = %q)", res.Text)
	}
}

// Test页面脚本不能改协议白名单 同理:file:// 默认不放行,脚本也拿不到。
func Test页面脚本不能改协议白名单(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>读文件</title></head><body>
<article id="c"><p>…</p></article>
<script>
fetch('file:///etc/passwd')
  .then(function (r) { return r.text(); })
  .then(function (t) { document.querySelector('#c p').textContent = '不该发生:' + t.slice(0, 20); })
  .catch(function (e) { document.querySelector('#c p').textContent = '被拒:' + e.message; });
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "不该发生") {
		t.Fatalf("页面脚本读到了本机文件:%q", res.Text)
	}
	if !strings.Contains(res.Text, "未放行") {
		t.Errorf("应当看到协议白名单的拒绝原因(正文 = %q)", res.Text)
	}
}

// Test页面脚本只能GET POST 等于用我们的服务器向第三方发写请求,默认不给。
func Test页面脚本只能GET(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>POST</title></head><body>
<article id="c"><p>…</p></article>
<script>
fetch('/api.json', { method: 'POST', body: 'x=1' })
  .then(function () { document.querySelector('#c p').textContent = '不该发生:POST 成功了'; })
  .catch(function (e) { document.querySelector('#c p').textContent = '被拒:' + e.message; });
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", 测试选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "不该发生") {
		t.Fatalf("POST 被放行了:%q", res.Text)
	}
	if !strings.Contains(res.Text, "只支持 GET") {
		t.Errorf("应当明确说不支持 GET 以外的请求(正文 = %q)", res.Text)
	}
}

// Test关掉JS网络能力后页面脚本拿不到fetch 开关得真的生效:
// 关掉之后 fetch 不存在,页面脚本应当走它的 catch 分支(或抛 ReferenceError)。
func Test关掉JS网络能力后页面脚本拿不到fetch(t *testing.T) {
	srv := 起测试服务(t)
	opt := 测试选项()
	opt.DisableJSFetch = true
	html := `<!DOCTYPE html><html lang="zh"><head><title>关闭</title></head><body>
<article id="c"><p>…</p></article>
<script>
try {
  fetch('/api.json');
  document.querySelector('#c p').textContent = 'fetch 仍然存在';
} catch (e) {
  document.querySelector('#c p').textContent = 'fetch 不可用:' + e.name;
}
</script>
</body></html>`
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "fetch 不可用") {
		t.Errorf("关掉之后 fetch 仍然可用?正文 = %q", res.Text)
	}
}
