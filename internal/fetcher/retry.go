package fetcher

// HTTP 重试:限流与临时性服务端错误不该直接算失败。
//
// 实测动机:连续下载 64 张维基图片时有 2 张返回 HTTP 429。429 是
// **暂时性**的 —— 等一下就能拿到,直接放弃等于白丢内容。
//
// 只重试明确表示「稍后再试」的状态码。对 404 / 403 这类重试多少次
// 都一样的状态码不做无谓重试,免得平白拖慢失败路径。

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// maxRetries 是额外重试次数(不含首次请求)。
const maxRetries = 2

// retryableStatus 报告该状态码是否值得重试。
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429 限流
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// retryBackoff 给出第 attempt 次重试前的等待时间(attempt 从 0 起)。
// 指数退避并封顶 —— 抓一个网页不值得等太久。
func retryBackoff(attempt int) time.Duration {
	d := time.Duration(500*(1<<attempt)) * time.Millisecond // 500ms、1s
	if d > 3*time.Second {
		d = 3 * time.Second
	}
	return d
}

// retryAfter 解析响应里的 Retry-After 头,只认秒数形式。
//
// 超过 10 秒就不等了 —— 抓一个页面不该为服务端的建议等那么久,
// 那时直接返回 429 让调用方决定更合适。
func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 || secs > 10 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// doWithRetry 执行请求,对可重试状态码退避重试。
//
// build 每次都构造一个**新的**请求 —— 重试必须重新构造,因为已发出的
// 请求其 Body 已被消费,复用只会得到空 body。
//
// 重试耗尽时返回最后一次的响应(而不是错误),让调用方按原逻辑处理
// 状态码 —— 它已经知道该怎么报告 429 了。
func doWithRetry(client *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			// 网络层错误也重试:连接重置、超时都是常见的瞬时故障
			if attempt == maxRetries {
				return nil, err
			}
			time.Sleep(retryBackoff(attempt))
			continue
		}
		if !retryableStatus(resp.StatusCode) || attempt == maxRetries {
			return resp, nil
		}
		wait := retryBackoff(attempt)
		if d, ok := retryAfter(resp); ok {
			wait = d
		}
		resp.Body.Close()
		time.Sleep(wait)
	}
	return nil, errors.New("重试耗尽")
}

// retryNote 用于把「重试过」这个事实写进错误信息,便于排查。
func retryNote(code int) error {
	return fmt.Errorf("HTTP %d(已重试 %d 次)", code, maxRetries)
}
