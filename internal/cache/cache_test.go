package cache

// 这个包此前【一个测试文件都没有】(第三轮交叉评审 C3/C5)。缓存的两道防线
// (TTL + 容量上限)是多轮踩坑换来的,尤其"写入前后各清一次"这条:
// 只清一次会出现「清理时没超容量、写完就超了」(实测上限 600KB 时两个 440KB
// 的页面同时留在目录里)。这里逐条钉住。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func 新缓存(t *testing.T, ttl time.Duration, maxBytes int64) *Cache {
	t.Helper()
	return New(Options{Dir: filepath.Join(t.TempDir(), "qjsdom-cache"), TTL: ttl, MaxBytes: maxBytes})
}

// 页面 造一个指定字节数的假页面。
func 页面(n int) string {
	return "<html><body>" + strings.Repeat("x", n) + "</body></html>"
}

func 数条目(t *testing.T, c *Cache) int {
	t.Helper()
	ents, err := os.ReadDir(c.Dir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".html") {
			n++
		}
	}
	return n
}

func Test缓存往返(t *testing.T) {
	c := 新缓存(t, time.Minute, 1<<20)
	html := 页面(100)

	if _, ok := c.Get("https://example.com/a"); ok {
		t.Error("空缓存不该命中")
	}
	if err := c.Put("https://example.com/a", html); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, ok := c.Get("https://example.com/a")
	if !ok || got != html {
		t.Errorf("取回的内容与写入不一致:ok=%v len=%d/%d", ok, len(got), len(html))
	}
	// 不同 URL 不互相命中
	if _, ok := c.Get("https://example.com/b"); ok {
		t.Error("不同 URL 不该命中同一个条目")
	}
}

func TestTTL过期即删(t *testing.T) {
	c := 新缓存(t, 50*time.Millisecond, 1<<20)
	if err := c.Put("https://example.com/a", 页面(100)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, ok := c.Get("https://example.com/a"); !ok {
		t.Fatal("刚写入就该命中")
	}

	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("https://example.com/a"); ok {
		t.Error("超过 TTL 不该命中")
	}
	// 过期即删:顺手回收空间,而不是留在目录里等下一次 Purge
	if _, err := os.Stat(c.keyPath("https://example.com/a")); !os.IsNotExist(err) {
		t.Errorf("过期条目应当被删掉,实际 stat 返回 %v", err)
	}
}

func Test容量上限按最久未访问淘汰(t *testing.T) {
	// 单页 200KB,上限 500KB —— 放得下两个,放不下三个
	//
	// ⚠️ TTL 必须远大于"做旧"的幅度:第一版两处都写 1 小时,于是 time.Since(mtime)
	// 正好超过 TTL,老条目是被【过期】删掉的,压根没走到 LRU 淘汰 —— 把排序方向
	// 反过来测试依然通过(变异验证当场抓出这条测试没有判别力)。现在 TTL 24h、
	// 只往回 1 小时,进入淘汰的才是"没超时但最久没被访问"的那一个。
	c := 新缓存(t, 24*time.Hour, 500<<10)
	老 := "https://example.com/老"
	A := 页面(200 << 10)
	if err := c.Put(老, A); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 把它做旧,模拟"很久没人访问"
	旧 := time.Now().Add(-time.Hour)
	if err := os.Chtimes(c.keyPath(老), 旧, 旧); err != nil {
		t.Fatalf("改 mtime 失败: %v", err)
	}
	if err := c.Put("https://example.com/中", 页面(200<<10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := c.Put("https://example.com/新", 页面(200<<10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	if _, ok := c.Get(老); ok {
		t.Error("最久未访问的条目应当被淘汰")
	}
	if _, ok := c.Get("https://example.com/中"); !ok {
		t.Error("较新的条目不该被淘汰")
	}
	if _, ok := c.Get("https://example.com/新"); !ok {
		t.Error("最新写入的条目不该被淘汰")
	}
	if n := 数条目(t, c); n != 2 {
		t.Errorf("淘汰后应当剩 2 个条目,实际 %d", n)
	}
}

// Test写入前后各清一次 —— 这条锁住的是一个真坑。
// 只在写入前清:清的时候新条目还不存在,于是"清理时没超容量、写完就超了"。
func Test写入前后各清一次(t *testing.T) {
	c := 新缓存(t, time.Hour, 600<<10) // 上限 600KB

	if err := c.Put("https://example.com/一", 页面(440<<10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := c.Put("https://example.com/二", 页面(440<<10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	// 两个 440KB 同时留下 = 880KB > 600KB,正是只清一次时的实测现象
	if n := 数条目(t, c); n != 1 {
		t.Fatalf("写入后目录里应当只剩 1 个条目(实际 %d 个)—— 写入后的那次清理没生效", n)
	}
	st := c.Purge()
	if st.Bytes > 600<<10 {
		t.Errorf("清理后占用 %d 字节,仍超过上限", st.Bytes)
	}
	if _, ok := c.Get("https://example.com/二"); !ok {
		t.Error("最新写入的那个应当活下来")
	}
}

func Test单条超过总容量则拒绝且不伤已有条目(t *testing.T) {
	c := 新缓存(t, time.Hour, 1000)
	if err := c.Put("https://example.com/小", 页面(100)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	err := c.Put("https://example.com/巨", 页面(5000))
	if err == nil {
		t.Fatal("比总容量还大的条目应当被拒绝")
	}
	if !strings.Contains(err.Error(), "超过缓存容量上限") {
		t.Errorf("拒绝理由应当说清是容量问题,实际:%v", err)
	}
	// 关键:拒绝不能顺手把缓存清空 —— 为一条塞不下的内容挤掉所有缓存不值得
	if _, ok := c.Get("https://example.com/小"); !ok {
		t.Error("拒绝超大条目时不该伤到已有的条目")
	}
}

func Test目录只在首次写入时创建(t *testing.T) {
	c := 新缓存(t, time.Hour, 1<<20)
	if _, err := os.Stat(c.Dir()); !os.IsNotExist(err) {
		t.Errorf("New 不该立刻建目录,实际 stat 返回 %v", err)
	}
	if err := c.Put("https://example.com/a", 页面(10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if fi, err := os.Stat(c.Dir()); err != nil || !fi.IsDir() {
		t.Errorf("首次写入后目录应当存在:err=%v", err)
	}
}

func Test空内容不写缓存(t *testing.T) {
	c := 新缓存(t, time.Hour, 1<<20)
	if err := c.Put("https://example.com/a", ""); err != nil {
		t.Fatalf("空内容不该报错: %v", err)
	}
	if n := 数条目(t, c); n != 0 {
		t.Errorf("空内容不该落盘,实际 %d 个条目", n)
	}
}

func TestPurge顺手清掉临时文件(t *testing.T) {
	c := 新缓存(t, time.Hour, 1<<20)
	if err := c.Put("https://example.com/a", 页面(10)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 模拟上次崩溃留下的 .tmp
	遗留 := filepath.Join(c.Dir(), "1234abcd.tmp")
	if err := os.WriteFile(遗留, []byte("半截"), 0o600); err != nil {
		t.Fatalf("造遗留文件失败: %v", err)
	}
	c.Purge()
	if _, err := os.Stat(遗留); !os.IsNotExist(err) {
		t.Errorf("Purge 应当顺手清掉 .tmp 残留,实际 stat 返回 %v", err)
	}
}

func TestPurge报告超时条目数(t *testing.T) {
	c := 新缓存(t, 40*time.Millisecond, 1<<20)
	if err := c.Put("https://example.com/a", 页面(100)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	st := c.Purge()
	if st.Expired != 1 {
		t.Errorf("应当报告 1 个过期条目,实际 %+v", st)
	}
	if st.Entries != 0 || st.Bytes != 0 {
		t.Errorf("过期之后不该还有条目:%+v", st)
	}
}

func TestClear删掉整个目录(t *testing.T) {
	c := 新缓存(t, time.Hour, 1<<20)
	if err := c.Put("https://example.com/a", 页面(100)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := c.Clear(); err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}
	if _, err := os.Stat(c.Dir()); !os.IsNotExist(err) {
		t.Errorf("Clear 之后目录应当不存在,实际 stat 返回 %v", err)
	}
}
