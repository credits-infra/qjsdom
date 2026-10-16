package renderer

// 第四轮交叉评审 A1(中):`抓baseHref` 扫的是原始文本,所以注释里、或 JS 字符串里的
// `<base>` 也会被当真标签 —— 实测一句 `<!-- <base href="https://假.example/"> -->`
// 就能把整页的相对地址(外链脚本 / 页面脚本 fetch / 图片)劫持到那个主机上。
// 修法是扫描前先摘掉注释与脚本体;这一组用例把它钉住。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func Test假base不被当真标签(t *testing.T) {
	文档地址 := "https://real.example/dir/page"

	情形 := []struct {
		名    string
		体    string
		期望基准 string
	}{
		{
			"注释里的假base",
			`<!-- <base href="https://fake.example/"> --><article id="c"><p>x</p></article>`,
			文档地址,
		},
		{
			"脚本体字符串里的假base",
			`<article id="c"><p>x</p></article><script>var s = "<base href='https://fake.example/'>";</script>`,
			文档地址,
		},
		{
			"注释里的假base + 后面有真base",
			`<!-- <base href="https://fake.example/"> --><base href="/realdir/"><article id="c"><p>x</p></article>`,
			"https://real.example/realdir/",
		},
		{
			"没有base",
			`<article id="c"><p>x</p></article>`,
			文档地址,
		},
	}

	for _, c := range 情形 {
		html := `<!DOCTYPE html><html lang="zh"><head><title>t</title></head><body>` + c.体 + `</body></html>`
		得到 := 有效基准(html, 文档地址)
		if 得到 != c.期望基准 {
			t.Errorf("%s:基准应当是 %q,实际 %q", c.名, c.期望基准, 得到)
		}
		if strings.Contains(得到, "fake.example") {
			t.Errorf("%s:注释/脚本里的 fake base 劫持了基准:%q", c.名, 得到)
		}
	}
}

// Test假base不劫持外链脚本 端到端再钉一遍:注释里的假主机绝不能收到请求。
func Test假base不劫持外链脚本(t *testing.T) {
	var 假主机被请求 bool
	假 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		假主机被请求 = true
		w.Write([]byte(`document.querySelector("#c p").textContent = "假主机被执行了";`))
	}))
	t.Cleanup(假.Close)

	真, 见过 := 记录服务(t, map[string]string{
		"/realdir/app.js": `document.querySelector("#c p").textContent = "真目录的脚本跑过了";`,
	})

	html := `<!DOCTYPE html><html lang="zh"><head><title>t</title>
<!-- <base href="` + 假.URL + `/"> -->
<base href="/realdir/">
</head><body><article id="c"><p>原文本</p></article>
<script src="app.js"></script>
</body></html>`

	res, err := Render(html, 真.URL+"/页", 测试选项带基准())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if 假主机被请求 {
		t.Error("注释里的 fake base 生效了:假主机收到了请求")
	}
	if !strings.Contains(res.Text, "真目录的脚本跑过了") {
		t.Errorf("真 base 应当生效:正文=%q 请求过=%v errs=%v", res.Text, 见过(), res.ScriptErrs)
	}
}
