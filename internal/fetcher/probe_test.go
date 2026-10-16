package fetcher

// internal/fetcher/probe.go 此前 0% 覆盖(第三轮交叉评审 C5)。
// 图片探测的失败语义是「OK=false + 原因」而不是异常 —— 页面脚本靠它决定布局,
// 给假尺寸比给失败更糟。这里锁住:尺寸/格式/Bytes 的取值、只读头部、
// 未注册格式要明确报错、以及它同样走 SSRF 守卫。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 噪点PNG 造一张压不动的大 PNG —— 纯色图会被压到几十字节,测不出"只读头部"。
func 噪点PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rnd.Intn(256)), G: uint8(rnd.Intn(256)),
				B: uint8(rnd.Intn(256)), A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("造 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

func 探测服务(t *testing.T, 处理 http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(处理))
	t.Cleanup(srv.Close)
	return srv
}

func Test探测尺寸格式与字节数(t *testing.T) {
	data := 造PNG(t, 250, 134)
	srv := 探测服务(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(data)
	})

	info := ProbeImage(srv.URL+"/插图.png", 图片选项())
	if !info.OK || info.Error != "" {
		t.Fatalf("探测应当成功:%+v", info)
	}
	if info.Width != 250 || info.Height != 134 {
		t.Errorf("尺寸不对:%+v", info)
	}
	if info.Format != "png" {
		t.Errorf("格式应当是 png,实际 %q", info.Format)
	}
	// 有 Content-Length 时用它,而不是按实际读到的头部长度
	if info.Bytes != int64(len(data)) {
		t.Errorf("字节数应当取 Content-Length(%d),实际 %d", len(data), info.Bytes)
	}
}

func Test探测只读头部不取整张图(t *testing.T) {
	data := 噪点PNG(t, 400, 400)
	if int64(len(data)) <= maxProbeBytes {
		t.Fatalf("测试素材不够大(%d 字节),测不出只读头部", len(data))
	}
	srv := 探测服务(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(200)
		// 先写一小段并 flush,强制走 chunked(没有 Content-Length)
		w.Write(data[:1024])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		w.Write(data[1024:])
	})

	info := ProbeImage(srv.URL+"/大图.png", 图片选项())
	if !info.OK {
		t.Fatalf("大图探测应当成功:%+v", info)
	}
	// 也不能是负数:chunked 响应的 Content-Length 是 -1,直接用就会报出 -1 字节
	if info.Bytes <= 0 || info.Bytes > maxProbeBytes {
		t.Errorf("没有 Content-Length 时,字节数应当落在 (0, %d] 内,实际 %d", maxProbeBytes, info.Bytes)
	}
	if info.Width != 400 || info.Height != 400 {
		t.Errorf("只读头部也该解出尺寸:%+v", info)
	}
}

func Test探测未注册格式要明说原因(t *testing.T) {
	srv := 探测服务(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Write([]byte("RIFF\x1a\x00\x00\x00WEBPVP8 lost junk")) // 既不是可解码格式,也不该崩
	})

	info := ProbeImage(srv.URL+"/x.webp", 图片选项())
	if info.OK {
		t.Fatalf("解不出来就不该报成功:%+v", info)
	}
	if !strings.Contains(info.Error, "未注册格式") {
		t.Errorf("失败理由应当点出可能是未注册格式(webp/avif/svg),实际 %q", info.Error)
	}
}

func Test探测失败给状态码(t *testing.T) {
	srv := 探测服务(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	info := ProbeImage(srv.URL+"/missing.png", 图片选项())
	if info.OK || !strings.Contains(info.Error, "404") {
		t.Errorf("404 应当给出带状态码的失败理由:%+v", info)
	}
}

func Test探测守SSRF守卫(t *testing.T) {
	opt := DefaultOptions() // AllowPrivate = false
	info := ProbeImage("http://127.0.0.1:9/x.png", opt)
	if info.OK || !strings.Contains(info.Error, "SSRF") {
		t.Errorf("探测同样要走守卫:%+v", info)
	}
	// 不支持的协议直接拒(探测只支持 http/https)
	info2 := ProbeImage("file:///etc/hosts", opt)
	if info2.OK || info2.Error == "" {
		t.Errorf("file:// 不该被探测:%+v", info2)
	}
}
