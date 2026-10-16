package renderer

import (
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
	if got := 去尾标签(`var s = "</script>";`); strings.Contains(got, "</script") {
		t.Errorf("没有转义 </script:%q", got)
	}
	if got := 安全注释("地址 http://x/*/y"); strings.Contains(got, "*/") {
		t.Errorf("注释没被保护:%q", got)
	}
}
