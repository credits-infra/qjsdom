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
	data, out := fetchImageData(rawURL, opt, maxBytes)
	if out.Error != "" || dir == "" {
		return out
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		out.Error = "创建目录失败: " + err.Error()
		return out
	}
	// 文件名用 URL 的 sha1,避免路径穿越与重名
	sum := sha1.Sum([]byte(out.URL))
	name := hex.EncodeToString(sum[:])[:16] + extFromFormat(out.Format)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		out.Error = "写入失败: " + err.Error()
		return out
	}
	out.Path = path
	return out
}

// FetchImageBytes 下载单张图片并返回字节,不落盘。
//
// MCP 服务需要把图片作为内容块返回(base64),所以它用这个而不是
// DownloadImage。返回的 ImageFile 里 Path 为空,尺寸与格式照常填。
func FetchImageBytes(rawURL string, opt Options, maxBytes int64) ([]byte, ImageFile) {
	return fetchImageData(rawURL, opt, maxBytes)
}

// fetchImageData 是 DownloadImage 与 FetchImageBytes 的共同实现:
// 取回图片字节并解析尺寸,不碰文件系统。
func fetchImageData(rawURL string, opt Options, maxBytes int64) ([]byte, ImageFile) {
	out := ImageFile{URL: rawURL}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		out.Error = "URL 解析失败: " + err.Error()
		return nil, out
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "file" {
		out.Error = fmt.Sprintf("协议 %q 未放行(默认只允许 http/https)", u.Scheme)
		return nil, out
	}
	if u.Scheme == "file" {
		if !schemeAllowed(u.Scheme, opt.AllowSchemes) {
			out.Error = fmt.Sprintf("协议 %q 未放行(默认只允许 http/https)", u.Scheme)
			return nil, out
		}
		return readLocalImage(u, maxBytes)
	}
	if u.Host == "" {
		out.Error = "URL 缺少主机名"
		return nil, out
	}
	if !opt.AllowPrivate {
		if err := guardSSRF(u.Hostname()); err != nil {
			out.Error = err.Error()
			return nil, out
		}
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}

	client := 建客户端(opt, timeout)

	// 图片下载同样走重试:连续下载多张时最容易碰到 429。
	build := func() (*http.Request, error) {
		r, err := http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("User-Agent", opt.UserAgent)
		r.Header.Set("Accept", "image/*,*/*;q=0.8")
		return r, nil
	}

	resp, err := doWithRetry(client, build)
	if err != nil {
		out.Error = err.Error()
		return nil, out
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return nil, out
	}

	// 只接受图片类型 —— 有些站点会用脚本/HTML 伪装成图片地址
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "image/") {
		out.Error = fmt.Sprintf("非图片内容类型: %s", ct)
		return nil, out
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		out.Error = "读取失败: " + err.Error()
		return nil, out
	}
	if int64(len(data)) > maxBytes {
		out.Error = fmt.Sprintf("超过单张上限(%d MB)", maxBytes>>20)
		return nil, out
	}
	if len(data) == 0 {
		out.Error = "空响应"
		return nil, out
	}

	// 扩展名优先按内容嗅探,其次看 Content-Type
	ext := sniffImageExt(data, ct)
	out.Format = strings.TrimPrefix(ext, ".")
	out.Bytes = int64(len(data))

	// 尽力解析尺寸;SVG/WebP 等未注册格式解不出来也不影响保存
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		out.Width = cfg.Width
		out.Height = cfg.Height
	}

	return data, out
}

// readLocalImage 读本机图片文件(file:// 协议,仅在显式放行 file 时可达)。
//
// 与 HTTP 路径共用同一套嗅探与尺寸解析,保证 file:// 与 http:// 的
// ImageFile 字段语义一致。
func readLocalImage(u *url.URL, maxBytes int64) ([]byte, ImageFile) {
	out := ImageFile{URL: u.String()}
	path := u.Path
	if path == "" {
		path = u.Host + u.Path
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	f, err := os.Open(path)
	if err != nil {
		out.Error = "读取本机文件失败: " + err.Error()
		return nil, out
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		out.Error = "读取失败: " + err.Error()
		return nil, out
	}
	if int64(len(data)) > maxBytes {
		out.Error = fmt.Sprintf("超过单张上限(%d MB)", maxBytes>>20)
		return nil, out
	}
	if len(data) == 0 {
		out.Error = "空文件"
		return nil, out
	}
	out.Format = strings.TrimPrefix(sniffImageExt(data, ""), ".")
	out.Bytes = int64(len(data))
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		out.Width = cfg.Width
		out.Height = cfg.Height
	}
	return data, out
}

// extFromFormat 把 ImageFile.Format(不带点)转回扩展名。
func extFromFormat(format string) string {
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		return ".jpg"
	case "png":
		return ".png"
	case "gif":
		return ".gif"
	case "webp":
		return ".webp"
	case "svg":
		return ".svg"
	case "":
		return ".jpg"
	}
	return "." + strings.ToLower(format)
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
