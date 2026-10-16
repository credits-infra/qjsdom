package renderer

// 第四轮交叉评审 B1:整页的联网时间预算原先只盖住页面脚本的 fetch/XHR,
// 而 `<script src>` 那一遍(10 个 × 8s = 80s)与 Image 探测(20 × 8s = 160s)
// 都不受约束 —— 160s 比当初担心的 150s 更大。
// 现在三条出站通路共用同一个 网络预算 窗口,这里各锁一条。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func Test外链脚本也守同一预算(t *testing.T) {
	var 命中 int32
	mux := http.NewServeMux()
	for i := 1; i <= 4; i++ {
		i := i
		mux.HandleFunc("/s"+string(rune('0'+i))+".js", func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&命中, 1)
			time.Sleep(150 * time.Millisecond) // 每个脚本慢一点,好让预算先到
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(`document.querySelector("#c p").textContent = "脚本` + string(rune('0'+i)) + `跑过了";`))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	html := `<!DOCTYPE html><html lang="zh"><head><title>预算</title></head><body>` +
		`<article id="c"><p>原文本</p></article>` +
		`<script src="/s1.js"></script><script src="/s2.js"></script>` +
		`<script src="/s3.js"></script><script src="/s4.js"></script>` +
		`</body></html>`

	opt := 测试选项带基准()
	opt.JSFetchBudget = 400 * time.Millisecond // 4 个 ×150ms ≈ 600ms,必然不够
	res, err := Render(html, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	t.Logf("外链脚本:取回 %d 个,正文=%q errs=%v", atomic.LoadInt32(&命中), res.Text, res.ScriptErrs)

	if 取回 := atomic.LoadInt32(&命中); 取回 >= 4 {
		t.Errorf("4 个慢脚本应当被预算拦下至少一个,实际全都取回了(%d 个)", 取回)
	}
	if strings.Contains(res.Text, "脚本4跑过了") {
		t.Errorf("超出预算之后的脚本不该还在执行:正文=%q", res.Text)
	}
	记录 := strings.Join(res.ScriptErrs, " | ")
	if !strings.Contains(记录, "联网时间预算已用尽") {
		t.Errorf("被预算拦下必须留说明,实际 errs=%v", res.ScriptErrs)
	}
}
