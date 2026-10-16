package fetcher

// internal/fetcher/image_download.go 此前 0% 覆盖(第三轮交叉评审 C5)。
// 这里锁住的是踩坑换来的行为:
//   · 429 是暂时性的 —— 要退避重试;404/403 重试多少次都一样 —— 不该拖慢失败路径
//   · 重试必须重建请求(已发出的请求体已被消费)
//   · 不是图片类型就按类型拒绝(MediaWiki 用 <img> 做 CentralAutoLogin 探测,
//     拒它是【正确行为】,不是 bug)
//   · 单张上限:超了就说清是超上限,而不是给一段被截断的破图
//   · 图片下载同样走 SSRF 守卫(它能被页面内容触发,是出站面)

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func 造PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("造 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

func 图片选项() Options {
	o := DefaultOptions()
	o.AllowPrivate = true // 测试服务器在 127.0.0.1
	o.Timeout = 5 * time.Second
	o.UserAgent = "qjsdom-test"
	return o
}

func 图服务(t *testing.T, 处理 http.HandlerFunc) (*httptest.Server, func() int32) {
	t.Helper()
	var 次 int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&次, 1)
		处理(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int32 { return atomic.LoadInt32(&次) }
}

func 回PNG(t *testing.T, w http.ResponseWriter, data []byte, contentType string) {
	t.Helper()
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Write(data)
}

func Test下载图片落盘并解析尺寸(t *testing.T) {
	png数据 := 造PNG(t, 250, 134)
	srv, _ := 图服务(t, func(w http.ResponseWriter, r *http.Request) {
		回PNG(t, w, png数据, "image/png")
	})
	dir := t.TempDir()

	f := DownloadImage(srv.URL+"/插图.png", dir, 图片选项(), 1<<20)
	if f.Error != "" {
		t.Fatalf("下载失败: %s", f.Error)
	}
	if f.Format != "png" || f.Width != 250 || f.Height != 134 {
		t.Errorf("格式/尺寸不对:%+v", f)
	}
	if f.Bytes != int64(len(png数据)) {
		t.Errorf("字节数不对:报告 %d,实际 %d", f.Bytes, len(png数据))
	}
	if f.Path == "" {
		t.Fatal("没有落盘路径")
	}
	盘上, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatalf("读落盘文件失败: %v", err)
	}
	if !bytes.Equal(盘上, png数据) {
		t.Error("落盘的字节与响应不一致")
	}
	// 文件名 = URL 的 sha1 前缀 + 嗅探出的扩展名
	if filepath.Ext(f.Path) != ".png" {
		t.Errorf("扩展名应当按内容嗅探为 .png,实际 %q", filepath.Ext(f.Path))
	}
}

func Test图片429会退避重试(t *testing.T) {
	png数据 := 造PNG(t, 8, 8)
	var 次 int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&次, 1) == 1 {
			// Retry-After: 0 —— 让测试不必真等 500ms 的退避
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		回PNG(t, w, png数据, "image/png")
	}))
	t.Cleanup(srv.Close)

	数据, f := FetchImageBytes(srv.URL+"/a.png", 图片选项(), 1<<20)
	if f.Error != "" {
		t.Fatalf("429 之后应当重试成功: %s", f.Error)
	}
	if len(数据) != len(png数据) {
		t.Errorf("重试后拿到的字节不对: %d", len(数据))
	}
	if 请求次数 := atomic.LoadInt32(&次); 请求次数 < 2 {
		t.Errorf("应当对 429 重试,实际只请求了 %d 次", 请求次数)
	}
	if f.Path != "" {
		t.Errorf("FetchImageBytes 不该落盘,实际 Path=%q", f.Path)
	}
}

func Test图片404不重试(t *testing.T) {
	srv, 次 := 图服务(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, f := FetchImageBytes(srv.URL+"/missing.png", 图片选项(), 1<<20)
	if !strings.Contains(f.Error, "404") {
		t.Errorf("失败原因应当带上状态码,实际 %q", f.Error)
	}
	// 404 重试多少次都一样,重试只会拖慢失败路径
	if 次() != 1 {
		t.Errorf("404 不该重试,实际请求了 %d 次", 次())
	}
}

func Test非图片类型按类型拒绝(t *testing.T) {
	png数据 := 造PNG(t, 8, 8)
	情形 := map[string]string{
		"错误页":  "text/html; charset=utf-8",
		"探测脚本": "text/javascript",
	}
	for 名, ct := range 情形 {
		ct := ct
		t.Run(名, func(t *testing.T) {
			srv, _ := 图服务(t, func(w http.ResponseWriter, r *http.Request) {
				回PNG(t, w, png数据, ct)
			})
			_, f := FetchImageBytes(srv.URL+"/not-image", 图片选项(), 1<<20)
			if !strings.Contains(f.Error, "非图片内容类型") {
				t.Errorf("应当按类型拒绝(收到 %s),实际 %q", ct, f.Error)
			}
		})
	}
}

func Test超过单张上限则拒绝(t *testing.T) {
	大图 := 造PNG(t, 200, 200)
	srv, _ := 图服务(t, func(w http.ResponseWriter, r *http.Request) {
		回PNG(t, w, 大图, "image/png")
	})

	_, f := FetchImageBytes(srv.URL+"/big.png", 图片选项(), 64) // 上限 64 字节
	if !strings.Contains(f.Error, "超过单张上限") {
		t.Errorf("超上限要明说是超上限(而不是给半张破图),实际 %q", f.Error)
	}
}

func Test扩展名按内容嗅探而不是信ContentType(t *testing.T) {
	png数据 := 造PNG(t, 8, 8)
	srv, _ := 图服务(t, func(w http.ResponseWriter, r *http.Request) {
		// 站点把 PNG 声明成 image/jpeg(真实站点上见过声明错的)
		回PNG(t, w, png数据, "image/jpeg")
	})

	_, f := FetchImageBytes(srv.URL+"/mislabelled", 图片选项(), 1<<20)
	if f.Format != "png" {
		t.Errorf("扩展名应当按内容嗅探为 png,实际 %q", f.Format)
	}
}

func Test图片下载守SSRF守卫(t *testing.T) {
	opt := DefaultOptions() // AllowPrivate = false
	_, f := FetchImageBytes("http://127.0.0.1:9/x.png", opt, 1<<20)
	if !strings.Contains(f.Error, "SSRF") {
		t.Errorf("图片下载同样要走 SSRF 守卫,实际 %q", f.Error)
	}
	// 云元数据地址也在守卫范围内(第三轮评审 D2 的同一套规则)。
	// 注意:它命中的是运营级 NAT 保留段那条规则,拒绝理由里没有 "SSRF" 字样 ——
	// 断言盯"被拦下且给得出理由",不要盯某一句文案。
	_, f2 := FetchImageBytes("http://100.100.100.200/latest/meta-data/", opt, 1<<20)
	if f2.Error == "" {
		t.Error("云元数据地址必须被拦下")
	} else if !strings.Contains(f2.Error, "SSRF") && !strings.Contains(f2.Error, "保留段") {
		t.Errorf("拒绝理由应当说清是保留地址,实际 %q", f2.Error)
	}
}

func Test本机图片文件要显式放行file(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "本机.png")
	if err := os.WriteFile(p, 造PNG(t, 12, 34), 0o644); err != nil {
		t.Fatalf("写临时图片失败: %v", err)
	}
	url := "file://" + p

	// 默认不放行 file
	opt := DefaultOptions()
	if _, f := FetchImageBytes(url, opt, 1<<20); !strings.Contains(f.Error, "未放行") {
		t.Errorf("默认不该放行 file://,实际 %q", f.Error)
	}
	// 显式放行后能读,且字段语义与 http 一致
	opt.AllowSchemes = []string{"file"}
	_, f := FetchImageBytes(url, opt, 1<<20)
	if f.Error != "" {
		t.Fatalf("放行 file 后应当读得到: %s", f.Error)
	}
	if f.Format != "png" || f.Width != 12 || f.Height != 34 {
		t.Errorf("file:// 与 http:// 的字段语义应当一致:%+v", f)
	}
}
