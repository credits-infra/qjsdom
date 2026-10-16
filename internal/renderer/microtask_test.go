package renderer

import (
	"strings"
	"testing"
)

// Test微任务在抽取之前执行 钉住两阶段的意义:脚本里排进微任务队列的回调
// (Promise.then、以及后面 fetch().then)必须【在抽取之前】跑完,否则它们对
// DOM 的改动进不了结果。
//
// 改造前的实测:这个夹具的正文出 "原文本"(then 里写的字根本没进正文)。
// 原因是 quickjs 的 js_std_loop 只在 Eval 返回后抽微任务,而那时抽取早已结束。
func Test微任务在抽取之前执行(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>微任务夹具</title></head><body>
<article id="c"><p>原文本</p></article>
<script>
Promise.resolve().then(function () {
  document.querySelector('#c p').textContent = '微任务写入的文本';
});
</script>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "微任务写入的文本") {
		t.Errorf("微任务对 DOM 的改动没进正文(正文 = %q,scriptErrs = %v)", res.Text, res.ScriptErrs)
	}
	if strings.Contains(res.Text, "原文本") {
		t.Errorf("正文里还是旧内容 —— 抽取跑在了微任务前面:%q", res.Text)
	}
}

// Test微任务链式多级 钉住多级 then 也能跑完(链上有几个微任务就得抽几次)。
// 这条是给 fetch().then(r => r.json()).then(...) 那种写法探路的:
// 中间任何一级没被抽干,最后一级就永远不会执行。
func Test微任务链式多级(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>链式微任务</title></head><body>
<article id="c"><p>起点</p></article>
<script>
Promise.resolve("一段中间数据")
  .then(function (v) { return v + " → 第二级"; })
  .then(function (v) { return v + " → 第三级"; })
  .then(function (v) { document.querySelector('#c p').textContent = v; });
</script>
</body></html>`
	res, err := Render(html, "https://example.com/x",
		Options{Mode: ModeArticle, Selector: "#c", DisableImages: true})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(res.Text, "第三级") {
		t.Errorf("多级 then 没有跑完(正文 = %q)", res.Text)
	}
}
