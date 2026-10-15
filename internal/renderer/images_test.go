package renderer

import (
	"strings"
	"testing"
)

// 页面骨架:页眉 LOGO、正文容器里的插图、侧栏徽标。
// 页眉与侧栏的图与"这页在讲什么"无关,正是要靠容器范围排除掉的东西。
const 容器夹具 = `<!DOCTYPE html>
<html lang="zh"><head><title>容器范围测试</title></head><body>
  <header><img src="https://example.com/logo.png"></header>
  <article id="content">
    <h1>正文标题</h1>
    <p>%s</p>
    <img src="https://example.com/figure.png">
  </article>
  <aside class="sidebar"><img src="https://example.com/badge.png"></aside>
</body></html>`

// 长段落:article 模式要求候选容器正文 ≥ 200 字才认。
const 一段 = "这一段正文写得足够长,长到能让 article 模式认定这个容器就是正文所在," +
	"而不是页面上随便哪个包含文字的块。它不需要有实际意义,只需要占用足够的字符数。"

const 长段落 = 一段 + 一段 + 一段

func 渲染(t *testing.T, opt Options) *Result {
	t.Helper()
	opt.DisableImages = true // 不联网:这条用例只关心图片地址的收集范围
	res, err := Render(strings.Replace(容器夹具, "%s", 长段落, 1),
		"https://example.com/page", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	return res
}

func 判定图片(t *testing.T, 名 string, got []string, 想 ...string) {
	t.Helper()
	if len(got) != len(想) {
		t.Errorf("%s: 收集到 %d 张 %v,想要 %d 张 %v", 名, len(got), got, len(想), 想)
		return
	}
	for i := range 想 {
		if got[i] != 想[i] {
			t.Errorf("%s: 收集到 %v,想要 %v", 名, got, 想)
			return
		}
	}
}

// TestImagesFollowTheContentContainer 钉住「图片收集跟随正文容器」。
//
// 曾经是全文档 querySelectorAll("img"),于是页眉 LOGO、侧栏徽标都会被算成
// 这一页的图片 —— 实测那正是「LOGO 顶掉正文插图」事故里混进来的东西。
func TestImagesFollowTheContentContainer(t *testing.T) {
	// ① 显式 selector:只收容器内的图
	res := 渲染(t, Options{Mode: ModeArticle, Selector: "#content"})
	判定图片(t, "article + selector", res.Images, "https://example.com/figure.png")

	// ② all 模式给 selector 时同样跟随容器
	res = 渲染(t, Options{Mode: ModeAll, Selector: "#content"})
	判定图片(t, "all + selector", res.Images, "https://example.com/figure.png")

	// ③ article 模式自己选中容器(没给 selector)时也跟随容器
	res = 渲染(t, Options{Mode: ModeArticle})
	判定图片(t, "article 自选容器", res.Images, "https://example.com/figure.png")
}

// TestImagesFallBackToWholeDocument 钉住「没有正文容器时退回全文档」:
// links 模式要的是页面上所有链接,all 模式的默认语义是"什么都不丢",
// 这两种情况都必须照旧收全,不能因为上面的改动少收图。
func TestImagesFallBackToWholeDocument(t *testing.T) {
	全 := []string{
		"https://example.com/logo.png",
		"https://example.com/figure.png",
		"https://example.com/badge.png",
	}
	res := 渲染(t, Options{Mode: ModeLinks})
	判定图片(t, "links(无容器)", res.Images, 全...)

	res = 渲染(t, Options{Mode: ModeAll})
	判定图片(t, "all(无 selector,整页)", res.Images, 全...)
}
