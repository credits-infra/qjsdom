package fetcher

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

const 中文句 = "糖酵解在细胞质中进行,磷酸果糖激酶-1 是限速酶。"

func GBK(t *testing.T, 文本 string) []byte {
	t.Helper()
	出, err := simplifiedchinese.GBK.NewEncoder().String(文本)
	if err != nil {
		t.Fatalf("GBK 编码失败: %v", err)
	}
	return []byte(出)
}

func Big5(t *testing.T, 文本 string) []byte {
	t.Helper()
	出, err := traditionalchinese.Big5.NewEncoder().String(文本)
	if err != nil {
		t.Fatalf("Big5 编码失败: %v", err)
	}
	return []byte(出)
}

// GBK 页面、编码写在响应头里 —— 这是最容易漏的一种:meta 可以没有,头骗不了人。
func Test响应头声明GBK(t *testing.T) {
	体 := GBK(t, "<html><head><title>糖酵解</title></head><body><p>"+中文句+"</p></body></html>")
	文本, 结论 := DecodeHTML(体, "text/html; charset=gbk")
	if !strings.Contains(文本, 中文句) {
		t.Errorf("正文没解对: %q", 文本[:min(80, len(文本))])
	}
	if 结论.Name != "gbk" || 结论.Source != "header" {
		t.Errorf("结论应为 gbk/响应头,实际 %+v", 结论)
	}
}

// 只靠 meta 声明。中文老站点大量这种写法。
func Test只靠meta声明也能解(t *testing.T) {
	体 := GBK(t, `<html><head><meta http-equiv="Content-Type" content="text/html; charset=gb2312">`+
		`<title>t</title></head><body><p>`+中文句+`</p></body></html>`)
	文本, 结论 := DecodeHTML(体, "text/html")
	if !strings.Contains(文本, 中文句) {
		t.Errorf("meta 里的 gb2312 没被采信: %q", 文本[:min(80, len(文本))])
	}
	if 结论.Source != "meta" {
		t.Errorf("依据应为 meta,实际 %+v", 结论)
	}
}

func TestMeta的简写形式(t *testing.T) {
	体 := GBK(t, `<html><head><meta charset="gbk"><title>t</title></head><body><p>`+中文句+`</p></body></html>`)
	if 文本, 结论 := DecodeHTML(体, ""); !strings.Contains(文本, 中文句) || 结论.Source != "meta" {
		t.Errorf("`<meta charset=gbk>` 这种写法没解对: %+v, 正文 %q", 结论, 文本[:min(60, len(文本))])
	}
}

// 不是只有简体中文:编码表由 htmlindex 提供,Big5 一样能解。
func TestBig5声明(t *testing.T) {
	常量 := "醣解作用在細胞質中進行"
	体 := Big5(t, "<html><head><meta charset=\"big5\"></head><body><p>"+常量+"</p></body></html>")
	文本, 结论 := DecodeHTML(体, "")
	if !strings.Contains(文本, 常量) {
		t.Errorf("Big5 没解对: %+v", 结论)
	}
}

// BOM 比声明硬:规范里 BOM 嗅探在传输层声明之前。
func TestBOM优先于声明(t *testing.T) {
	体 := append([]byte{0xEF, 0xBB, 0xBF}, []byte("<p>"+中文句+"</p>")...)
	文本, 结论 := DecodeHTML(体, "text/html; charset=gbk") // 头在撒谎
	if 结论.Source != "bom" || 结论.Name != "utf-8" {
		t.Errorf("BOM 应当优先,实际 %+v", 结论)
	}
	if strings.ContainsRune(文本, 0xFEFF) {
		t.Errorf("BOM 字符不该留在正文里: %q", 文本[:min(20, len(文本))])
	}
	if !strings.Contains(文本, 中文句) {
		t.Errorf("正文没解对: %q", 文本[:min(60, len(文本))])
	}
}

// 声明了 UTF-8 却真有坏字节:换成替换符,别把非法序列喂给 JS 引擎 ——
// linkedom 与 QuickJS 拿到非法 UTF-8 的行为是未定义的。
func Test声明UTF8但有坏字节(t *testing.T) {
	体 := []byte("<p>好的</p>\xff\xfe坏字节")
	文本, 结论 := DecodeHTML(体, "text/html; charset=utf-8")
	if 结论.Name != "utf-8" {
		t.Errorf("结论 %+v", 结论)
	}
	if !strings.Contains(文本, "好的") {
		t.Errorf("好字节被连坐了: %q", 文本)
	}
	if !strings.ContainsRune(文本, '\uFFFD') {
		t.Errorf("坏字节应该变成替换符: %q", 文本)
	}
}

// 端到端:抓取这一层就把编码解掉,渲染层拿到的永远是 UTF-8。
func Test抓取时按声明解码(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		w.Write(GBK(t, "<html><head><title>糖酵解</title></head><body><p>"+中文句+"</p></body></html>"))
	}))
	defer srv.Close()

	opt := DefaultOptions()
	opt.AllowPrivate = true // 测试服务器在 127.0.0.1,SSRF 守卫默认拒绝(这是对的)
	res, err := Fetch(srv.URL, opt)
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if !strings.Contains(res.HTML, 中文句) {
		t.Errorf("抓取结果没解对编码: %q", res.HTML[:min(80, len(res.HTML))])
	}
	if res.Charset.Name != "gbk" || res.Charset.Source != "header" {
		t.Errorf("结论应随结果一起给出,实际 %+v", res.Charset)
	}
}

func to1252(t *testing.T, 文本 string) []byte {
	t.Helper()
	出, err := charmap.Windows1252.NewEncoder().String(文本)
	if err != nil {
		t.Fatalf("windows-1252 编码失败: %v", err)
	}
	return []byte(出)
}

// 未声明编码的 GBK 页面 —— 中文老站点的常态。改前整页乱码(chromium 能猜对,
// 它有自己的编码探测;我们靠字节结构猜)。
func Test未声明时推断简体中文(t *testing.T) {
	体 := GBK(t, "<html><body><p>"+中文句+"</p></body></html>")
	文本, 结论 := DecodeHTML(体, "")
	if !strings.Contains(文本, 中文句) {
		t.Errorf("未声明的 GBK 页没解对: %+v", 结论)
	}
	if 结论.Name != "gb18030" || 结论.Source != "guessed" {
		t.Errorf("结论应为推断简体中文,实际 %+v", 结论)
	}
}

// 同样未声明的西欧页面:必须走 windows-1252,不能被 GB18030 解成一片假汉字。
// 这条是上面那个结构判据的判别力所在 —— 只看"解出来是不是汉字"会在这里翻车。
func Test未声明时推断西欧(t *testing.T) {
	原文 := "<html><body><p>Le métabolisme dégrade le glucose; la glycolyse est très efficace.</p></body></html>"
	体 := to1252(t, 原文)
	文本, 结论 := DecodeHTML(体, "")
	if !strings.Contains(文本, "métabolisme dégrade le glucose") {
		t.Errorf("西欧页被解坏了: %+v, 正文 %q", 结论, 文本)
	}
	if 结论.Name != "windows-1252" {
		t.Errorf("结论应为 windows-1252,实际 %+v", 结论)
	}
	if strings.ContainsFunc(文本, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		t.Errorf("西欧页面里不该出现汉字: %q", 文本)
	}
}

// 样本太短就不该乱判:一两个孤立高位字节不足以断言"这是简体中文"。
func Test短样本不乱判(t *testing.T) {
	if 像GBK汉字([]byte("caf\xe9")) {
		t.Errorf("三个字节的样本不该被判成 GBK 汉字")
	}
	if !像GBK汉字(GBK(t, 中文句)) {
		t.Errorf("真正的 GBK 中文句应当被判成 GBK 汉字")
	}
}
