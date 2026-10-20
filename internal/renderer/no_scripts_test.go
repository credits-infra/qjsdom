package renderer

import (
	"strings"
	"testing"
)

// TestNoScriptsSkipsInlineExecution 钉住 -no-scripts 的承诺:调用方关掉脚本时,
// 页面里的 <script> 不得执行,正文必须是 HTML 里原本的内容。
//
// 旧行为:脚本开关字段没有被任何代码读取,页面脚本照跑,只在 scriptErrs 里留一句
// "尚未实现"。现在改成 DisableScripts,零值仍然表示执行脚本。
func TestNoScriptsSkipsInlineExecution(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>no-scripts</title></head><body>
<article id="c"><p id="p">原文本</p></article>
<script>document.querySelector("#p").textContent = "脚本跑过了";</script>
</body></html>`

	opt := 测试选项()
	opt.DisableScripts = true
	res, err := Render(html, "https://example.com/x", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(res.Text, "脚本跑过了") {
		t.Errorf("关掉脚本后页面脚本仍然执行了,正文=%q", res.Text)
	}
	if !strings.Contains(res.Text, "原文本") {
		t.Errorf("正文丢了原本的静态文本,正文=%q", res.Text)
	}
	if res.ScriptsRan != 0 {
		t.Errorf("关掉脚本后 scriptsRan 应为 0,实际 %d(scriptTags=%d)", res.ScriptsRan, res.ScriptTags)
	}
	if res.ScriptTags != 1 {
		t.Errorf("scriptTags 应照实统计页面里的脚本数,实际 %d", res.ScriptTags)
	}

	// 对照组:同一份 HTML 在默认选项下必须真的能跑,否则上面的断言可能只是
	// 因为脚本本身没写对。
	control, err := Render(html, "https://example.com/x", 测试选项())
	if err != nil {
		t.Fatalf("对照组渲染失败: %v", err)
	}
	if !strings.Contains(control.Text, "脚本跑过了") || control.ScriptsRan != 1 {
		t.Errorf("对照组没有执行脚本,测试用例本身失效:正文=%q ran=%d", control.Text, control.ScriptsRan)
	}
}

// TestNoScriptsDoesNotFetchExternalScript 钉住关脚本时的另一条网络路径:
// 外链脚本由 Go 侧在渲染前取回并内联,如果只跳过执行而不跳过取回,关掉脚本
// 仍然会向页面声明的地址发请求。关脚本应当连取回一起停掉。
func TestNoScriptsDoesNotFetchExternalScript(t *testing.T) {
	srv, 见过 := 记录服务(t, map[string]string{
		"/app.js": `document.querySelector("#p").textContent = "外链脚本跑过了";`,
	})

	html := `<!DOCTYPE html><html lang="zh"><head><title>no-scripts</title></head><body>
<article id="c"><p id="p">原文本</p></article>
<script src="/app.js"></script>
</body></html>`

	opt := 测试选项()
	opt.DisableScripts = true
	res, err := Render(html, srv.URL+"/page", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if got := 见过(); len(got) != 0 {
		t.Errorf("关掉脚本后仍然请求了外链脚本: %v", got)
	}
	if strings.Contains(res.Text, "外链脚本跑过了") {
		t.Errorf("关掉脚本后外链脚本仍然执行了,正文=%q", res.Text)
	}
	if res.ScriptsRan != 0 {
		t.Errorf("关掉脚本后 scriptsRan 应为 0,实际 %d", res.ScriptsRan)
	}

	// 对照组:默认选项会取回并执行同一个外链脚本。
	control, err := Render(html, srv.URL+"/page", 测试选项())
	if err != nil {
		t.Fatalf("对照组渲染失败: %v", err)
	}
	if !strings.Contains(control.Text, "外链脚本跑过了") {
		t.Errorf("对照组没有执行外链脚本,测试用例本身失效:正文=%q errs=%v", control.Text, control.ScriptErrs)
	}
}
