package renderer

// 第三轮交叉评审 C5:出站矩阵里页面脚本这一侧只测过 fetch —— XMLHttpRequest
// 通路与 Image 探测的配额这两格一直没有测试。这里补上。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"qjsdom/internal/fetcher"
)

// 造小PNG 生一张真 PNG(fetcher 包里那份是包内私有,这里要自己的)。
func 造小PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("造 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// 探测选项 与 测试选项() 的区别:不禁用图片,这样 Image 网络探测那一层才会注册。
func 探测选项() Options {
	fo := fetcher.DefaultOptions()
	fo.AllowPrivate = true
	return Options{Mode: ModeArticle, Selector: "#c", FetchOptions: fo}
}

func TestXHR取数据进正文(t *testing.T) {
	srv := 起测试服务(t)
	html := `<!DOCTYPE html><html lang="zh"><head><title>XHR</title></head><body>
<article id="c"><p>加载中…</p>
<script>
var x = new XMLHttpRequest();
x.open("GET", "/api.json", true);
x.onload = function () {
  document.querySelector("#c p").textContent =
    "状态=" + x.status + " 头=" + x.getResponseHeader("X-Test-Header") + " 体=" + x.responseText;
};
x.onerror = function (e) {
  document.querySelector("#c p").textContent = "不该失败:" + String(e && e.message);
};
x.send();
</script>
</article></body></html>`

	res, err := Render(html, srv.URL+"/page", 探测选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	t.Logf("XHR 正文=%q errs=%v", res.Text, res.ScriptErrs)
	if !strings.Contains(res.Text, "状态=200") {
		t.Errorf("XHR 应当拿到 200(正文=%q)", res.Text)
	}
	if !strings.Contains(res.Text, `体={"v":"糖酵解","n":10}`) {
		t.Errorf("responseText 应当是响应体原文(正文=%q)", res.Text)
	}
	if !strings.Contains(res.Text, "头=有") {
		t.Errorf("getResponseHeader 应当读得到响应头(正文=%q)", res.Text)
	}
}

func TestXHR失败走onerror而不是假装成功(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>XHR 失败</title></head><body>
<article id="c"><p>原文本</p>
<script>
var x = new XMLHttpRequest();
x.open("GET", "file:///etc/hosts", true);
x.onload = function () {
  // 不该走到这里:file:// 没被放行,请求必须失败
  document.querySelector("#c p").textContent = "不该走到 onload:status=" + x.status;
};
x.onerror = function (e) {
  document.querySelector("#c p").textContent = "错误:status=" + x.status + " 说明=" +
    String(e && e.message ? e.message : e);
};
x.send();
</script>
</article></body></html>`

	res, err := Render(html, "https://example.com/page", 探测选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	t.Logf("XHR 失败正文=%q errs=%v", res.Text, res.ScriptErrs)
	if strings.Contains(res.Text, "不该走到 onload") {
		t.Errorf("未放行的协议必须走失败路径(正文=%q)", res.Text)
	}
	if !strings.Contains(res.Text, "错误:status=0") {
		t.Errorf("失败时 status 应当是 0(正文=%q)", res.Text)
	}
}

func Test图片探测有单页配额(t *testing.T) {
	var 探测次数 int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&探测次数, 1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(造小PNG(t, 8, 8))
	}))
	t.Cleanup(srv.Close)

	// 25 张互不相同的图 —— 超过单页 20 次配额
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh"><head><title>配额</title></head><body>` +
		`<article id="c"><p>原文本</p><script>`)
	b.WriteString(`for (var i = 0; i < 25; i++) { var im = new Image(); im.src = "/img" + i + ".png"; }`)
	b.WriteString(`document.querySelector("#c p").textContent = "探测已发起";`)
	b.WriteString(`</script></article></body></html>`)

	res, err := Render(b.String(), srv.URL+"/page", 探测选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "探测已发起") {
		t.Fatalf("页面脚本没跑完:正文=%q errs=%v", res.Text, res.ScriptErrs)
	}
	if got := atomic.LoadInt32(&探测次数); got != 20 {
		t.Errorf("单页图片探测应当恰好 20 次,实际 %d 次", got)
	}
}

func Test同URL的图片只探一次(t *testing.T) {
	var 探测次数 int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&探测次数, 1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(造小PNG(t, 8, 8))
	}))
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>同图</title></head><body>
<article id="c"><p>原文本</p>
<script>
var 宽 = 0;
for (var i = 0; i < 3; i++) {
  var im = new Image();
  im.src = "/同一张.png";
  宽 = im.naturalWidth; // 同步就绪(Go 侧探测是同步的)
}
document.querySelector("#c p").textContent = "宽=" + 宽;
</script>
</article></body></html>`

	res, err := Render(html, srv.URL+"/page", 探测选项())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if got := atomic.LoadInt32(&探测次数); got != 1 {
		t.Errorf("同页同一张图只该探一次,实际 %d 次", got)
	}
	// 尺寸在 setter 返回时就该就绪 —— 页面脚本不必等 onload
	if !strings.Contains(res.Text, "宽=8") {
		t.Errorf("naturalWidth 应当同步就绪(正文=%q)", res.Text)
	}
}
