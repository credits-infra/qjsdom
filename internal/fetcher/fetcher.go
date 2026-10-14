// Package fetcher 负责把网页源码取回来。
//
// 刻意保持极简:不执行 JS、不带 cookie、不上报任何来源信息。
// 带 SSRF 防护 —— 这个组件将来会暴露给 QQ 群里的任意人调用,
// 不能让它去探内网(127.0.0.1 / 10.x / 192.168.x / 169.254.x …)。
package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options 控制抓取行为。
type Options struct {
	UserAgent string
	Timeout   time.Duration
	MaxBytes  int64
	// AllowPrivate 为 true 时放行内网地址(默认 false,仅供本机测试用)
	AllowPrivate bool
}

// DefaultOptions 返回给爬公网用的默认值。
func DefaultOptions() Options {
	return Options{
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/126.0 Safari/537.36 qjsdom/0.1",
		Timeout: 20 * time.Second,
		MaxBytes: 4 << 20, // 4MB
	}
}

// Result 是一次抓取的结果。
type Result struct {
	URL       string // 最终 URL(跟随重定向后)
	HTML      string
	StatusCode int
	Bytes     int64
	Elapsed   time.Duration
}

// Fetch 抓取 url 并返回 HTML 源码。
func Fetch(rawURL string, opt Options) (*Result, error) {
	if opt.Timeout <= 0 {
		opt.Timeout = 20 * time.Second
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = 4 << 20
	}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("URL 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只支持 http/https,收到: %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("URL 缺少主机名")
	}
	if !opt.AllowPrivate {
		if err := guardSSRF(u.Hostname()); err != nil {
			return nil, err
		}
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), opt.Timeout)
	defer cancel()

	client := &http.Client{
		Timeout: opt.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多(>5)")
			}
			if !opt.AllowPrivate {
				if err := guardSSRF(req.URL.Hostname()); err != nil {
					return err
				}
			}
			return nil
		},
	}

	// 重试时需要重新构造请求(已发出的请求 Body 已被消费),
	// 所以把「造请求」这件事包成闭包交给 doWithRetry。
	build := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("User-Agent", opt.UserAgent)
		r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		r.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		return r, nil
	}

	resp, err := doWithRetry(client, build)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, opt.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	truncated := int64(len(body)) > opt.MaxBytes
	if truncated {
		body = body[:opt.MaxBytes]
	}

	html, err := decodeBody(body, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if truncated {
		html += "\n<!-- qjsdom: 响应超过上限已截断 -->"
	}

	finalURL := u.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &Result{
		URL:        finalURL,
		HTML:       html,
		StatusCode: resp.StatusCode,
		Bytes:      int64(len(body)),
		Elapsed:    time.Since(start),
	}, nil
}

// guardSSRF 拒绝解析到内网/回环/链路本地地址的目标。
func guardSSRF(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("域名解析失败: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
			ip.IsInterfaceLocalMulticast() {
			return fmt.Errorf("拒绝访问内网/保留地址(SSRF 防护): %s → %s", host, ip)
		}
		// 云环境元数据服务
		if ip.String() == "169.254.169.254" {
			return fmt.Errorf("拒绝访问元数据服务: %s", ip)
		}
	}
	return nil
}

// decodeBody 尽力把字节流解成 UTF-8 字符串。
func decodeBody(b []byte, contentType string) (string, error) {
	ct := strings.ToLower(contentType)
	// Go 的 html 包只吃 UTF-8;这里做最小处理:
	// 若声明了 charset=gbk/gb2312 等,提示调用方(暂不引入 x/text 依赖)。
	if strings.Contains(ct, "gbk") || strings.Contains(ct, "gb2312") {
		return string(b), nil // 保底:原样返回,由调用方决定
	}
	return string(b), nil
}
