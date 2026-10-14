package fetcher

// 页面图片下载 —— 把页面里的 <img> 真正取下来存成文件。
//
// 与 ProbeImage 的区别:
//   * ProbeImage 只读头部拿尺寸(给页面脚本的 `new Image()` 用,轻量)
//   * DownloadImage 保存完整文件(给模型看,需要真实像素数据)
//
// 模型(Gemini 等)完全支持图像输入,所以图片内容是有价值的产出,
// 而不是「反正看不了」的噪声。视频/音频则不同 —— 体积大、处理慢,
// 这里刻意不实现。

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ImageFile 是一张已下载(或尝试下载)的图片。
type ImageFile struct {
	URL    string `json:"url"`
	Path   string `json:"path,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Format string `json:"format,omitempty"`
	Error  string `json:"error,omitempty"`
}

// DownloadLimits 控制单页图片下载的资源占用。
type DownloadLimits struct {
	MaxImages   int   // 最多下载几张
	MaxBytesPer int64 // 单张上限
}

// DefaultDownloadLimits 给出保守的默认值。
func DefaultDownloadLimits() DownloadLimits {
	return DownloadLimits{
		MaxImages:   20,
		MaxBytesPer: 5 << 20, // 5MB/张
	}
}

// DownloadImage 下载单张图片到 dir。
//
// 任何失败都返回带 Error 的 ImageFile(而不是 Go error)—— 单张图失败
// 不应该影响整页处理。
func DownloadImage(rawURL, dir string, opt Options, maxBytes int64) ImageFile {
	out := ImageFile{URL: rawURL}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		out.Error = "URL 解析失败: " + err.Error()
		return out
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		out.Error = fmt.Sprintf("不支持的协议: %q", u.Scheme)
		return out
	}
	if u.Host == "" {
		out.Error = "URL 缺少主机名"
		return out
	}
	if !opt.AllowPrivate {
		if err := guardSSRF(u.Hostname()); err != nil {
			out.Error = err.Error()
			return out
		}
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			if !opt.AllowPrivate {
				return guardSSRF(req.URL.Hostname())
			}
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	req.Header.Set("User-Agent", opt.UserAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return out
	}

	// 只接受图片类型 —— 有些站点会用脚本/HTML 伪装成图片地址
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "image/") {
		out.Error = fmt.Sprintf("非图片内容类型: %s", ct)
		return out
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		out.Error = "读取失败: " + err.Error()
		return out
	}
	if int64(len(data)) > maxBytes {
		out.Error = fmt.Sprintf("超过单张上限(%d MB)", maxBytes>>20)
		return out
	}
	if len(data) == 0 {
		out.Error = "空响应"
		return out
	}

	// 扩展名优先按内容嗅探,其次看 Content-Type
	ext := sniffImageExt(data, ct)
	out.Format = strings.TrimPrefix(ext, ".")

	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			out.Error = "创建目录失败: " + err.Error()
			return out
		}
		// 文件名用 URL 的 sha1,避免路径穿越与重名
		sum := sha1.Sum([]byte(u.String()))
		name := hex.EncodeToString(sum[:])[:16] + ext
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			out.Error = "写入失败: " + err.Error()
			return out
		}
		out.Path = path
	}

	out.Bytes = int64(len(data))

	// 尽力解析尺寸;SVG/WebP 等未注册格式解不出来也不影响保存
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		out.Width = cfg.Width
		out.Height = cfg.Height
	}

	return out
}

// sniffImageExt 判断图片扩展名。
func sniffImageExt(data []byte, contentType string) string {
	// 魔数优先(比 Content-Type 可靠)
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return ".png"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return ".gif"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp"
	}
	// SVG 是文本
	head := strings.ToLower(strings.TrimSpace(string(data[:min(len(data), 512)])))
	if strings.HasPrefix(head, "<svg") || strings.HasPrefix(head, "<?xml") && strings.Contains(head, "<svg") {
		return ".svg"
	}
	// 退回 Content-Type
	switch {
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "svg"):
		return ".svg"
	}
	return ".jpg"
}
