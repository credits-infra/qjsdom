package fetcher

// 第四轮交叉评审 C5(高, 安全):三处「重定向后的 SSRF 逐跳校验」在端到端路径上
// 【没有任何回归锁】—— 把 CheckRedirect 分别换成 return nil,全仓库测试依然全绿。
// 原因是原先只测了 检查跳转 这个函数本身,没人测它有没有被挂到客户端上。
//
// 这一组用例锁的正是"有没有挂上":它抓住三处出站各自实际使用的 http.Client,
// 然后直接拿私网/公网/超跳数的请求去问它的 CheckRedirect。
//
// 注意为什么不去做一个"真服务器 302 跳私网"的端到端用例:本机测试服务器必然在
// 127.0.0.1 上,而 AllowPrivate=false 时【第一跳】就会被入口守卫拒掉,根本走不到
// 重定向那一步;AllowPrivate=true 时逐跳守卫又被整段跳过。两个方向都堵死,
// 所以只能在这一层锁。(这是构造上的限制,不是偷懒。)

import (
	"net/http"
	"testing"
	"time"
)

func Test三处出站都装了逐跳守卫(t *testing.T) {
	// 203.0.113.0/24 是 TEST-NET-3(文档用保留段):入口守卫放行,但不可路由,
	// 连接会立刻失败 —— 既不碰真实主机,又能让客户端真正被构造出来。
	不路由 := "http://203.0.113.1/x"
	opt := Options{Timeout: 100 * time.Millisecond}
	opt.AllowPrivate = false

	情形 := []struct {
		名 string
		跑 func()
	}{
		{"抓取 Fetch", func() { _, _ = Fetch(不路由, opt) }},
		{"图片探测 ProbeImage", func() { _ = ProbeImage(不路由+".png", opt) }},
		{"图片下载 FetchImageBytes", func() { _, _ = FetchImageBytes(不路由+".png", opt, 1024) }},
	}

	内网, _ := http.NewRequest("GET", "http://127.0.0.1:8080/", nil)
	元数据, _ := http.NewRequest("GET", "http://100.100.100.200/latest/meta-data/", nil)
	公网, _ := http.NewRequest("GET", "http://8.8.8.8/", nil)
	一跳 := []*http.Request{{}}

	for _, c := range 情形 {
		var 拿到 *http.Client
		旧建 := 建客户端
		// 关键:捕获的是【调用点实际用到的】客户端(旧建),而不是自己另造一个
		// 新逐跳守卫客户端 —— 后者等于绕过了"调用点到底有没有用这个构造入口",
		// 那样连"调用点换成裸 http.Client"都测不出来(第一版就是这么错的)。
		建客户端 = func(o Options, to time.Duration) *http.Client {
			拿到 = 旧建(o, to)
			return 拿到
		}
		c.跑()
		建客户端 = 旧建

		if 拿到 == nil {
			t.Errorf("%s:没有构造出 http.Client(入口守卫提前拒绝了?这条通路没测到钩子)", c.名)
			continue
		}
		if 拿到.CheckRedirect == nil {
			t.Errorf("%s:客户端没装 CheckRedirect —— 重定向后的 SSRF 校验形同虚设", c.名)
			continue
		}
		if err := 拿到.CheckRedirect(内网, 一跳); err == nil {
			t.Errorf("%s:重定向到 127.0.0.1 必须被拦下", c.名)
		}
		if err := 拿到.CheckRedirect(元数据, 一跳); err == nil {
			t.Errorf("%s:重定向到云元数据地址必须被拦下", c.名)
		}
		if err := 拿到.CheckRedirect(公网, 一跳); err != nil {
			t.Errorf("%s:公网地址不该被拦:%v", c.名, err)
		}
		if err := 拿到.CheckRedirect(公网, make([]*http.Request, 5)); err == nil {
			t.Errorf("%s:超过 5 跳应当拒绝", c.名)
		}
	}
}

// Test逐跳守卫在AllowPrivate下放行 本地测试全靠这个开关,别把它锁反了。
func Test逐跳守卫在AllowPrivate下放行(t *testing.T) {
	c := 新逐跳守卫客户端(Options{AllowPrivate: true}, time.Second)
	内网, _ := http.NewRequest("GET", "http://127.0.0.1:8080/", nil)
	if err := c.CheckRedirect(内网, []*http.Request{{}}); err != nil {
		t.Errorf("AllowPrivate 时不该拦:%v", err)
	}
}
