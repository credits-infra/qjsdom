package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qjsdom/internal/renderer"
)

// TestImageContentDataIsRawBytes 钉住「ImageContent.Data 必须是原始字节」。
//
// go-sdk 给这个字段的注释写着 "base64-encoded",与它的序列化实现不符:
// Data 被塞进 imageAudioWire.Data []byte,由 encoding/json 自己 base64 上 wire。
// 曾经照着注释预先编码一次,wire 上就成了 base64(base64(png)) —— 接收方解一次
// 只得到一段 ASCII,按「非图片数据」丢弃,图片内容块等于从未发出,而工具结果
// 看起来还是成功的。这条测试就是那个 bug 的回归锁。
func TestImageContentDataIsRawBytes(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
		bytes.Repeat([]byte{0x00}, 24)...)

	encoded, err := json.Marshal(&mcp.ImageContent{MIMEType: "image/png", Data: png})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var wire struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("wire 数据不可解析: %v", err)
	}

	// 关键:客户端只解一次。解一次就必须拿到图像本身的魔数。
	once, err := base64.StdEncoding.DecodeString(wire.Data)
	if err != nil {
		t.Fatalf("wire 的 data 不是 base64: %v", err)
	}
	if !bytes.Equal(once, png) {
		head := once
		if len(head) > 24 {
			head = head[:24]
		}
		t.Fatalf("解一次应当就是原始字节,实际是 %q —— Data 被预先编码了?", head)
	}
	if strings.HasPrefix(string(once), "iVBOR") {
		t.Fatal("解一次得到的是 base64 文本,说明 wire 上是 base64(base64(…))")
	}
}

// TestImageManifestListsEveryImageInOrder 钉住清单的契约:
// 按内容块顺序编号、逐条给出 URL 与路径、没落盘时明说。
//
// 清单是「路径 ↔ URL」的唯一对照表。模型手里有图却没有身份时,只能靠「第几块」
// 对齐;任何一个环节丢弃或跳过一张,编号就全错(实测:维基条目返回 3 张,客户端
// 拒收 SVG 后只附上 2 张,模型把站点 LOGO 当成正文插图发了出去)。
func TestImageManifestListsEveryImageInOrder(t *testing.T) {
	imgs := []pickedImage{
		{url: "https://a.example/logo.png", format: "png", width: 100, height: 100,
			path: "/tmp/img/one.png"},
		{url: "https://b.example/figure.png", format: "png", width: 250, height: 581,
			path: "/tmp/img/two.png"},
		{url: "https://c.example/badge.svg", format: "svg"},
	}

	got := imageManifest(imgs)

	for i, want := range []string{
		"1. https://a.example/logo.png [PNG 100×100]\n   文件:/tmp/img/one.png",
		"2. https://b.example/figure.png [PNG 250×581]\n   文件:/tmp/img/two.png",
		"3. https://c.example/badge.svg [SVG]\n   未落盘",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("清单缺少第 %d 条:\n想要包含: %q\n实际:\n%s", i+1, want, got)
		}
	}

	// 顺序必须与内容块一致,不能按 URL 或路径重排。
	if strings.Index(got, "a.example") > strings.Index(got, "b.example") {
		t.Error("清单顺序与内容块顺序不一致")
	}
	if n := strings.Count(got, "\n"); n < 6 {
		t.Errorf("清单条目不完整,只有 %d 行", n)
	}

	if empty := imageManifest(nil); empty != "" {
		t.Errorf("没有图片时不该产出清单,实际 %q", empty)
	}
}

// TestDescribeImageOmitsUnknownDimensions 钉住尺寸缺失时的降级:
// SVG 之类解不出像素,只给格式,不能填 0×0 误导调用方。
func TestDescribeImageOmitsUnknownDimensions(t *testing.T) {
	if got := describeImage(pickedImage{format: "svg"}); got != "SVG" {
		t.Errorf("解不出尺寸时应当只给格式,实际 %q", got)
	}
	if got := describeImage(pickedImage{}); got != "" {
		t.Errorf("什么都不知道时应当留空,实际 %q", got)
	}
}

// TestSaveImageFileNamesByURLHash 钉住文件名规则:URL 的 sha1 前 16 位。
// 调用方靠它把路径反推回来源;URL 变了文件名必须跟着变。
func TestSaveImageFileNamesByURLHash(t *testing.T) {
	dir := t.TempDir()
	body := []byte{0x89, 'P', 'N', 'G'}

	p1, err := saveImageFile(dir, "https://example.com/a.png", "png", body)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if filepath.Base(p1) != "b86dafa63bceefac.png" {
		t.Errorf("文件名不是 URL 的 sha1 前 16 位: %s", filepath.Base(p1))
	}
	if raw, err := os.ReadFile(p1); err != nil || !bytes.Equal(raw, body) {
		t.Errorf("落盘内容与给定字节不一致: %v", err)
	}

	p2, _ := saveImageFile(dir, "https://example.com/b.png", "png", body)
	if p1 == p2 {
		t.Error("不同 URL 落到了同一路径,会互相覆盖")
	}
	// 同名 URL 覆盖是预期行为(同一张图重复抓取)
	p3, _ := saveImageFile(dir, "https://example.com/a.png", "png", body)
	if p3 != p1 {
		t.Errorf("同一 URL 应当稳定落到同一路径: %s vs %s", p1, p3)
	}
}

// TestComposeTextSwitchesBodyByFormat 钉住 format 参数的效果:
// markdown 用 Markdown 正文,text 用纯文本正文,两者都不影响标题/清单。
func TestComposeTextSwitchesBodyByFormat(t *testing.T) {
	res := &renderer.Result{
		Title:    "页面标题",
		Text:     "纯文本正文",
		Markdown: "# 一级标题\n\n- 列表项",
		Mode:     "article",
		Links:    []renderer.Link{{Title: "示例", URL: "https://example.com"}},
	}
	out := fetchOut{}

	纯 := composeText(res, out, nil, false)
	if !strings.Contains(纯, "纯文本正文") || strings.Contains(纯, "# 一级标题") {
		t.Errorf("text 格式应当用纯文本正文:\n%s", 纯)
	}
	md := composeText(res, out, nil, true)
	if !strings.Contains(md, "# 一级标题") || strings.Contains(md, "纯文本正文") {
		t.Errorf("markdown 格式应当用 Markdown 正文:\n%s", md)
	}
	// 两种格式下标题行与链接清单都在
	for _, 输出 := range []string{纯, md} {
		if !strings.Contains(输出, "# 页面标题") {
			t.Errorf("标题行丢了:\n%s", 输出)
		}
		if !strings.Contains(输出, "[示例](https://example.com)") {
			t.Errorf("链接清单丢了:\n%s", 输出)
		}
	}
}

// TestComposeTextSaysWhenMarkdownMissing 钉住降级时明说:
// 要了 Markdown 却拿不到(转换失败),不能让它看起来像转换成功。
func TestComposeTextSaysWhenMarkdownMissing(t *testing.T) {
	res := &renderer.Result{Title: "T", Text: "纯文本正文", Mode: "article"}
	got := composeText(res, fetchOut{}, nil, true)
	if !strings.Contains(got, "Markdown 转换未产出内容") {
		t.Errorf("没有说明已降级:\n%s", got)
	}
	if !strings.Contains(got, "纯文本正文") {
		t.Errorf("降级后应当仍给纯文本:\n%s", got)
	}
}
