package fetcher

import (
	"net/http"
	"strings"
	"testing"
)

// Test守卫覆盖保留段与运营商级 NAT —— 第三轮评审 D2(高)。
// 实测:guardSSRF 对 100.100.100.200(阿里云元数据地址)返回 nil,页面脚本真的
// 发出了连接(只是本机无路由而超时)。100.64.0.0/10 是 RFC 6598 的运营商级 NAT
// 保留段,云厂商把元数据服务放在里面 —— 第一轮评审也报过同一个洞。
func Test守卫覆盖保留段与运营商级NAT(t *testing.T) {
	该拦 := []string{
		"100.100.100.200", // 阿里云元数据服务
		"100.64.0.1",      // CGNAT 段起点
		"100.127.255.254", // CGNAT 段终点
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1",
		"169.254.169.254", // AWS/GCP 元数据
		"0.0.0.0",
	}
	for _, h := range 该拦 {
		if err := guardSSRF(h); err == nil {
			t.Errorf("守卫放行了 %s(应当拦下)", h)
		}
	}
	放行 := []string{"8.8.8.8", "104.224.153.93", "1.1.1.1", "100.63.255.255", "100.128.0.1"}
	for _, h := range 放行 {
		if err := guardSSRF(h); err != nil {
			t.Errorf("守卫拦掉了公网地址 %s: %v", h, err)
		}
	}
}

// Test跳转守卫逐跳校验 —— 第三轮评审的变异测试证明:把 Fetch 里
// `guardSSRF(req.URL.Hostname())` 那行删掉,整个包测试依然全绿,也就是说
// 这条安全边界当时没有任何回归锁。现在它被抽成具名函数(检查跳转),这条用例锁住它。
// 用 IP 字面量,不产生 DNS 查询与真实连接。
func Test跳转守卫逐跳校验(t *testing.T) {
	opt := DefaultOptions() // AllowPrivate=false
	已跳过一跳 := []*http.Request{{}}

	内网, _ := http.NewRequest("GET", "http://127.0.0.1:8080/x", nil)
	if err := 检查跳转(内网, 已跳过一跳, opt); err == nil {
		t.Error("跳到 127.0.0.1 必须被拦下(逐跳校验)")
	} else if !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("拒绝理由应当是 SSRF 防护,实际:%v", err)
	}

	元数据, _ := http.NewRequest("GET", "http://100.100.100.200/latest/meta-data/", nil)
	if err := 检查跳转(元数据, 已跳过一跳, opt); err == nil {
		t.Error("跳到云元数据地址必须被拦下")
	}

	公网, _ := http.NewRequest("GET", "http://8.8.8.8/x", nil)
	if err := 检查跳转(公网, 已跳过一跳, opt); err != nil {
		t.Errorf("公网地址不该被拦:%v", err)
	}

	// AllowPrivate(仅本地测试用)时放行
	if err := 检查跳转(内网, 已跳过一跳, Options{AllowPrivate: true}); err != nil {
		t.Errorf("AllowPrivate 时不该拦:%v", err)
	}

	// 跳数上限
	if err := 检查跳转(公网, make([]*http.Request, 5), opt); err == nil {
		t.Error("超过 5 跳应当拒绝")
	}
}
