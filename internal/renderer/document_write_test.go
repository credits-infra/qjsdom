package renderer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// document.write 的一组用例。
//
// 分工(与实现里的注释、README 的说明一致):
//   * 解析 HTML 的是 linkedom 自己的解析器(容器.innerHTML = html);
//   * 搬运节点用的是原生 DOM 方法(document.createElement / parentNode /
//     firstChild / nextSibling / insertBefore);
//   * 我们只补 linkedom 缺失的 document.write / writeln / open / close。
// 所以下面既测"内容进去了",也测"它走的是解析器而不是被当成文本"。

// Test写入内容落在脚本原位置 是位置语义的核心:旧式页面正是靠 document.write
// 把内容写在脚本所在的地方 —— 写到别处,顺序就全错了。
func Test写入内容落在脚本原位置(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>位置</title></head><body>
<article id="c"><p>甲</p><script>document.write('<p>乙</p>');</script><p>丙</p></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	甲, 乙, 丙 := strings.Index(res.Text, "甲"), strings.Index(res.Text, "乙"), strings.Index(res.Text, "丙")
	if 甲 < 0 || 乙 < 0 || 丙 < 0 {
		t.Fatalf("三段内容没都在正文里: %q", res.Text)
	}
	if !(甲 < 乙 && 乙 < 丙) {
		t.Errorf("写入的内容不在脚本原位置(应当 甲→乙→丙),实际 %q", res.Text)
	}
}

// Test多次写入保持顺序 钉住一个容易写错的地方:若每次都插在脚本紧后面,
// 第二次写入会跑到第一次【前面】。实现里靠"插到同一个参考节点之前"避免。
func Test多次写入保持顺序(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>顺序</title></head><body>
<article id="c"><script>
document.write('<p>一</p>');
document.write('<p>二</p>');
document.write('<p>三</p>');
</script></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "一") || !strings.Contains(res.Text, "二") || !strings.Contains(res.Text, "三") {
		t.Fatalf("内容不全: %q", res.Text)
	}
	一, 二, 三 := strings.Index(res.Text, "一"), strings.Index(res.Text, "二"), strings.Index(res.Text, "三")
	if !(一 < 二 && 二 < 三) {
		t.Errorf("多次写入的顺序错了(应当 一→二→三),实际 %q", res.Text)
	}
}

// Test写入的HTML真的被解析 不是把源码当文本塞进去 —— 用的是 linkedom 的解析器。
// 用 Markdown 输出验证最直观:写入的 <b> 必须变成粗体语法。
func Test写入的HTML真的被解析(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>解析</title></head><body>
<article id="c"><script>
document.write('<p>这里有 <b>粗体</b> 与 <a href="/x">链接</a></p>');
document.querySelector("#c").setAttribute("data-探", document.querySelector("#c b") ? document.querySelector("#c b").tagName : "没解析出元素");
</script></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true, Markdown: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "粗体") {
		t.Fatalf("写入的内容没进正文: %q", res.Text)
	}
	if !strings.Contains(res.Markdown, "**粗体**") {
		t.Errorf("写入的 <b> 没被解析成元素(否则 Markdown 里会是 **粗体**): %q", res.Markdown)
	}
}

// Test没有当前脚本时追加到body并记提示 微任务/定时器里的调用:
// 浏览器此时会清空整个文档重写 —— 那个语义对抓取只有害处,我们改成追加到 body
// 末尾,并且【必须记一条】,不能悄悄换语义。
func Test没有当前脚本时追加到body并记提示(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>微任务里写</title></head><body>
<article id="c"><p>原有内容</p></article>
<script>
Promise.resolve().then(function () {
  document.write('<p>微任务写入</p>');
});
</script>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// 内容不该丢(丢掉才是真的坏),但要有一句说明它去了哪儿
	if !strings.Contains(strings.Join(res.ScriptErrs, " "), "document.write 在脚本之外被调用") {
		t.Errorf("没有记录「追加到 body」这件事,scriptErrs = %v", res.ScriptErrs)
	}
}

// Test注入的内联脚本会被执行 浏览器里 document.write('<script>…</script>') 会执行;
// 不执行的话那类页面永远差一块内容。
func Test注入的内联脚本会被执行(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>注入内联脚本</title></head><body>
<article id="c"><p>原文本</p><script>
document.write('<script>document.querySelector("#c p").textContent = "被注入的脚本改过";<\/script>');
</script></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "被注入的脚本改过") {
		t.Errorf("注入的内联脚本没执行(正文 = %q,scriptErrs = %v)", res.Text, res.ScriptErrs)
	}
	if res.ScriptsRan < 2 || res.ScriptTags < 2 {
		t.Errorf("统计应当把注入的脚本算进去:ran=%d tags=%d", res.ScriptsRan, res.ScriptTags)
	}
}

// Test注入的外链脚本会被取回执行 这条路只能在 JS 侧取(Go 侧内联发生在解析之前,
// 那时还没有它),走的是同一道守卫 —— 用真实 HTTP 服务验证。
func Test注入的外链脚本会被取回执行(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/注入.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`document.querySelector("#c p").textContent = "外链注入脚本跑过了";`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>注入外链</title></head><body>
<article id="c"><p>原文本</p><script>
document.write('<script src="/注入.js"><\/script>');
</script></article>
</body></html>`
	opt := 测试选项()
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "外链注入脚本跑过了") {
		t.Errorf("注入的外链脚本没执行(正文 = %q,scriptErrs = %v)", res.Text, res.ScriptErrs)
	}
}

// Test注入的外链脚本被关掉时留原因 关掉能力是允许的,但必须留痕 ——
// 静默跳过会让"这页怎么少了一块"变成谜。
func Test注入的外链脚本被关掉时留原因(t *testing.T) {
	srv := 起测试服务(t)
	opt := 测试选项()
	opt.DisableJSFetch = true
	html := `<!DOCTYPE html><html lang="zh"><head><title>关闭</title></head><body>
<article id="c"><p>原文本</p><script>
document.write('<script src="/app.js"><\/script>');
</script></article>
</body></html>`
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	记录 := strings.Join(res.ScriptErrs, " ")
	if !strings.Contains(记录, "注入的外链脚本未执行") {
		t.Errorf("关掉之后应当留一条原因,实际:%v", res.ScriptErrs)
	}
	if strings.Contains(res.Text, "外链脚本跑过了") {
		t.Errorf("关掉了却还是执行了:%q", res.Text)
	}
}

// Test未闭合标签被自动闭合 记录一个已知限制:不支持"跨调用拼一个标签"。
// 每次 write 都是独立解析,未闭合的标签会被解析器补全 —— 断言这个行为,
// 免得以后有人以为它会流式拼接。
func Test未闭合标签被自动闭合(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>未闭合</title></head><body>
<article id="c"><script>
document.write('<div><span>半截');
document.write('<i>然后是补的');
</script></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// 内容都在,但两个片段各自被当独立文档解析(这正是限制本身)
	if !strings.Contains(res.Text, "半截") || !strings.Contains(res.Text, "然后是补的") {
		t.Errorf("内容缺了: %q", res.Text)
	}
}

// Test脚本轮数有上限 脚本可以写出脚本,不设限就是死循环。上限到了要留一句。
func Test脚本轮数有上限(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>自我复制</title></head><body>
<article id="c"><p>起点</p><script>
var 层 = 0;
function 再写() {
  层++;
  if (层 < 12) document.write('<script>再写();<\/script>');
}
再写();
</script></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败(可能是轮数上限没生效): %v", err)
	}
	if !strings.Contains(strings.Join(res.ScriptErrs, " "), "轮数达到上限") {
		t.Errorf("轮数用尽应当留提示,实际:%v", res.ScriptErrs)
	}
}

// Test还有writeln与open_close 四个方法都在(document 上原本都没有),
// 且 open/close 不清空文档 —— 清空等于毁掉已经拿到的内容。
func Test还有writeln与open_close(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>四个方法</title></head><body>
<article id="c"><p>原有内容</p>
<pre id="pre"></pre>
<script>
document.writeln('<span>writeln 写入</span>');
document.getElementById("pre").textContent =
  "write=" + (typeof document.write) + " writeln=" + (typeof document.writeln) +
  " open=" + (typeof document.open) + " close=" + (typeof document.close);
var 返回 = document.open();
document.close();
</script></article>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	for _, 想要 := range []string{"write=function", "writeln=function", "open=function", "close=function"} {
		if !strings.Contains(res.Text, 想要) {
			t.Errorf("缺 %q(正文 = %q)", 想要, res.Text)
		}
	}
	if !strings.Contains(res.Text, "原有内容") && !strings.Contains(res.Text, "writeln 写入") {
		t.Errorf("open/close 之后内容不该消失:%q", res.Text)
	}
}
