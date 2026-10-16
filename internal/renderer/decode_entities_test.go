package renderer

// 第四轮交叉评审 A4(低,非本批引入):页内 JS 的 decodeEntities 是顺序替换,
// `&amp;lt;` 会被连解两层。链接与图片地址都过这一道,所以多解一层等于改写了地址。

import (
	"strings"
	"testing"
)

func Test双重转义只解一层(t *testing.T) {
	html := `<!DOCTYPE html><html lang="zh"><head><title>实体</title></head><body>
<article id="c"><p>原文本</p>
<a href="/l?a=1&amp;lt;2">链接</a>
<img src="/i.png?x=1&amp;lt;2" alt="图">
</article></body></html>`

	opt := 探测选项()
	opt.DisableImages = true // 只关心地址怎么解出来的,不真下载
	res, err := Render(html, "https://real.example/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	t.Logf("图片地址: %v", res.Images)

	要找 := false
	for _, u := range res.Images {
		if strings.Contains(u, "&lt;2") {
			要找 = true
		}
		if strings.Contains(u, "%3C2") || strings.Contains(u, "<2") {
			t.Errorf("解了两层:`&amp;lt;` 应当只解成字面量 `&lt;`,实际地址=%q", u)
		}
	}
	if !要找 {
		t.Errorf("图片地址里应当保留字面量 `&lt;2`(只解一层),实际=%v", res.Images)
	}

	链接里有 := false
	for _, l := range res.Links {
		if strings.Contains(l.URL, "&lt;2") {
			链接里有 = true
		}
		if strings.Contains(l.URL, "%3C2") {
			t.Errorf("链接地址也被解了两层:%q", l.URL)
		}
	}
	if !链接里有 {
		t.Errorf("链接地址里应当保留字面量 `&lt;2`,实际=%v", res.Links)
	}
}
