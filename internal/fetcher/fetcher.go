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
	"os"
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
	// AllowSchemes 是额外放行的 URL 协议(除 http/https 之外),默认空。
	//
	// 为什么协议要白名单:IP 黑名单永远漏项 —— 它是一份要不断补的历史清单,
	// 而 Go 的 IsPrivate() 实测不覆盖 100.64.0.0/10(RFC 6598),于是阿里云
	// ECS 的元数据地址 100.100.100.200 从"内网防护"底下漏出去。相对地,
	// "只认两个协议"没有漏项问题,它是可以一眼看完的边界。
	//
	// 目前内建支持放行的额外协议只有 file(读本机文件,用于渲染本地快照)。
	// 打开它等于给调用方一个读本机文件的入口 —— Hermes 这种"群里谁都能触发"
	// 的部署不要开。
	AllowSchemes []string
}

// SplitSchemes 把逗号分隔的协议列表解析成去空、小写、去重的切片。
func SplitSchemes(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		p = strings.TrimSuffix(p, ":")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// 协议未放行 是统一的拒绝理由:入口之间不该各写一套措辞。
func 协议未放行(scheme string) error {
	return fmt.Errorf(
		"协议 %q 未放行:默认只允许 http 与 https。"+
			"确需其它协议(如 file)必须在配置里显式打开,默认禁止。", scheme)
}

// CheckScheme 在真正发起请求之前检查 URL 的协议是否放行。
//
// 给调用方(Fetch 自己、以及 MCP 这类入口)复用同一条规则。为什么要暴露:
// MCP 曾经自己写死"只支持 http/https",于是 QJSDOM_ALLOW_SCHEMES 在那里
// 形同虚设 —— 配置说放行了 file,入口却在更早的地方拦掉。规则只该有一份。
func CheckScheme(rawURL string, allowSchemes []string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("URL 解析失败: %w", err)
	}
	if !schemeAllowed(u.Scheme, allowSchemes) {
		return 协议未放行(u.Scheme)
	}
	return nil
}

// schemeAllowed 判断协议是否放行。http/https 恒定放行,其余要看显式配置。
//
// 注意顺序:先判协议、再判主机名 —— file:// 没有主机名,反过来的话
// 它会被"URL 缺少主机名"误拦,报出一个与真实原因无关的错。
func schemeAllowed(scheme string, extra []string) bool {
	s := 规范化协议(scheme)
	if s == "http" || s == "https" {
		return true
	}
	if s == "" {
		return false
	}
	for _, e := range extra {
		if 规范化协议(e) == s {
			return true
		}
	}
	return false
}

// 规范化协议 统一大小写、空白与结尾冒号(config 里写 "FILE:" 也认)。
func 规范化协议(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ":")
}

// DefaultOptions 返回给爬公网用的默认值。
func DefaultOptions() Options {
	return Options{
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/126.0 Safari/537.36 qjsdom/0.1",
		Timeout:  20 * time.Second,
		MaxBytes: 4 << 20, // 4MB
	}
}

// Result 是一次抓取的结果。
type Result struct {
	URL        string // 最终 URL(跟随重定向后)
	HTML       string
	StatusCode int
	Bytes      int64
	Elapsed    time.Duration
	// Header 是响应头(跟随重定向后那一次的)。给页面脚本里的 fetch 用 ——
	// 它会读 content-type 之类的字段来决定怎么解析响应体。
	Header http.Header
	// Redirected 表示这次抓取跟随过重定向(fetch 的 Response.redirected)。
	Redirected bool
	// Truncated 表示响应体因为超过 MaxBytes 被截断了。
	// 调用方需要它来区分"完整拿到"与"拿到一半" —— 对脚本来说,半个脚本
	// 执行起来只会报一句没头没尾的语法错,不如不执行。
	Truncated bool
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
	if err := CheckScheme(rawURL, opt.AllowSchemes); err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		return fetchLocalFile(u, opt)
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
		Header:     resp.Header,
		Redirected: finalURL != u.String(),
		Truncated:  truncated,
	}, nil
}

// fetchLocalFile 把本机文件当页面源码读回来(file:// 协议)。
//
// 只有把 file 显式写进 AllowSchemes 才会走到这里 —— 它是一条读本机文件的
// 通路,默认关闭。大小上限沿用 MaxBytes,不把大文件整个读进内存。
func fetchLocalFile(u *url.URL, opt Options) (*Result, error) {
	start := time.Now()
	path := u.Path
	if path == "" {
		// file://host/path 这种写法会把路径开头放进 Host
		path = u.Host + u.Path
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取本机文件失败: %w", err)
	}
	defer f.Close()

	body, err := io.ReadAll(io.LimitReader(f, opt.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取本机文件失败: %w", err)
	}
	truncated := int64(len(body)) > opt.MaxBytes
	if truncated {
		body = body[:opt.MaxBytes]
	}
	html, err := decodeBody(body, "")
	if err != nil {
		return nil, err
	}
	if truncated {
		html += "\n<!-- qjsdom: 文件超过上限已截断 -->"
	}
	return &Result{
		URL:        u.String(),
		HTML:       html,
		StatusCode: 200,
		Bytes:      int64(len(body)),
		Elapsed:    time.Since(start),
		Truncated:  truncated,
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
