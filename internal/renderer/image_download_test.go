package renderer

// 渲染层的图片下载(downloadPageImages)此前没有测试:去重与上限都在这里。
// 评审那轮实测过"维基 87 张去重后 65 张" —— 重复的图会成倍增加请求数,
// 而请求数正是触发限流的原因。

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"qjsdom/internal/fetcher"
)

func 下载选项(t *testing.T, 上限 int) Options {
	t.Helper()
	opt := 探测选项()
	opt.DownloadImages = true
	opt.ImagesDir = t.TempDir()
	opt.ImageLimits = fetcher.DownloadLimits{MaxImages: 上限}
	return opt
}

func Test图片下载按URL去重(t *testing.T) {
	var mu sync.Mutex
	命中 := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		命中[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		w.Write(造小PNG(t, 120, 120))
	}))
	t.Cleanup(srv.Close)

	// 同一张图出现两次 —— 去重后只该取一次
	html := `<!DOCTYPE html><html lang="zh"><head><title>图</title></head><body><article id="c">
<p>正文</p>
<img src="/甲.png"><img src="/甲.png"><img src="/乙.png">
</article></body></html>`

	res, err := Render(html, srv.URL+"/page", 下载选项(t, 10))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("ImageFiles=%d 命中=%v", len(res.ImageFiles), 命中)
	if 命中["/甲.png"] != 1 {
		t.Errorf("同一张图只该取一次,实际取了 %d 次", 命中["/甲.png"])
	}
	if len(res.ImageFiles) != 2 {
		t.Errorf("去重后应当有 2 个图片文件,实际 %d 个", len(res.ImageFiles))
	}
	for _, f := range res.ImageFiles {
		if f.Error != "" || f.Path == "" {
			t.Errorf("下载应当成功且落盘:%+v", f)
		}
	}
}

func Test图片下载有数量上限(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(造小PNG(t, 120, 120))
	}))
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>图</title></head><body><article id="c">
<p>正文</p>
<img src="/1.png"><img src="/2.png"><img src="/3.png"><img src="/4.png">
</article></body></html>`

	res, err := Render(html, srv.URL+"/page", 下载选项(t, 2))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if len(res.ImageFiles) != 2 {
		t.Errorf("上限 2 时应当只下载 2 张,实际 %d 张", len(res.ImageFiles))
	}
}
