package fetcher

// 图片探测:只取「尺寸 + 格式 + 字节数」,不保留像素数据。
//
// 为什么需要它:页面脚本常用 `new Image()` 预加载或读取 naturalWidth/Height
// 来决定布局/懒加载。不给真实尺寸,很多页面会走进「图片加载失败」的分支,
// 甚至无限等待。而图片内容本身我们不需要 —— 模型也看不了。

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	// 注册常见图片格式的解码器(只用于 DecodeConfig,读取头部)
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ImageInfo 是一次图片探测的结果。
type ImageInfo struct {
	URL    string `json:"url"`
	OK     bool   `json:"ok"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int64  `json:"bytes"`
	Format string `json:"format"`
	Error  string `json:"error,omitempty"`
}

// maxProbeBytes 是解码头部所需的最大读取量。
// 各种格式的尺寸信息都在文件头部,256KB 足够覆盖极端情况(超大 EXIF 等)。
const maxProbeBytes = 256 << 10

// ProbeImage 取图片的尺寸信息。任何失败都返回 OK=false 的 ImageInfo,
// 而不是 error —— 调用方(页面脚本)需要的是「加载失败」这个语义,不是异常。
func ProbeImage(rawURL string, opt Options) ImageInfo {
	info := ImageInfo{URL: rawURL}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		info.Error = "URL 解析失败: " + err.Error()
		return info
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		info.Error = fmt.Sprintf("不支持的协议: %q", u.Scheme)
		return info
	}
	if u.Host == "" {
		info.Error = "URL 缺少主机名"
		return info
	}
	if !opt.AllowPrivate {
		if err := guardSSRF(u.Hostname()); err != nil {
			info.Error = err.Error()
			return info
		}
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	req.Header.Set("User-Agent", opt.UserAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		info.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return info
	}

	// 只读头部:尺寸信息都在前面,不必下载整张图
	head, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBytes))
	if err != nil {
		info.Error = "读取失败: " + err.Error()
		return info
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil {
		info.Error = "解码头部失败(可能是 webp/avif/svg 等未注册格式): " + err.Error()
		return info
	}

	info.OK = true
	info.Width = cfg.Width
	info.Height = cfg.Height
	info.Format = format
	if resp.ContentLength > 0 {
		info.Bytes = resp.ContentLength
	} else {
		info.Bytes = int64(len(head))
	}
	return info
}
