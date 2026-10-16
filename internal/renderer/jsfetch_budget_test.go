package renderer

// 页面脚本联网的时间预算(D6)。
//
// 次数上限管"多少次",不管"多久":15 次请求各自等满超时同样是串行的两分半,
// 而且调用方无法打断。这一组用例钉三件事:
//   ① 预算用尽后请求被拒,且原因写进正文(不是静默少取)
//   ② 预算充裕时一次不少
//   ③ 零值走默认值(10s),不会把正常页面卡住

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 慢页 5 次串行 fetch,服务器每次睡 150ms —— 总耗时必然超过 300ms 的预算。
const 慢页 = `<!DOCTYPE html><html lang="zh"><head><title>预算</title></head><body>
<article id="c"><p>原文本</p>
<script>
var 结果 = [];
function 下一个(i) {
  if (i >= 5) { document.querySelector('#c p').textContent = 结果.join(" | "); return; }
  fetch('slow.json')
    .then(function () { 结果.push("ok" + i); })
    .catch(function (e) { 结果.push("拒" + i + ":" + String(e.message).slice(0, 30)); })
    .then(function () { 下一个(i + 1); });
}
下一个(0);
</script>
</article></body></html>`

func 慢服务(t *testing.T, 每次 time.Duration) (*httptest.Server, func() int32) {
	t.Helper()
	var 命中 int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&命中, 1)
		time.Sleep(每次)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() int32 { return atomic.LoadInt32(&命中) }
}

func Test页面脚本联网超预算后拒绝转发(t *testing.T) {
	srv, 命中 := 慢服务(t, 150*time.Millisecond)

	opt := 测试选项带基准()
	opt.JSFetchBudget = 300 * time.Millisecond
	res, err := Render(慢页, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	t.Logf("预算 300ms:转发 %d 次,正文=%q,scriptErrs=%v", 命中(), res.Text, res.ScriptErrs)

	if !strings.Contains(res.Text, "网络时间预算已用尽") {
		t.Errorf("预算用尽必须让页面脚本看得见原因(而不是静默失败):正文=%q", res.Text)
	}
	if 命中() >= 5 {
		t.Errorf("预算已用尽却仍然把 5 次请求都转发了(转发 %d 次)", 命中())
	}
	if res.Degraded {
		t.Errorf("预算用尽不该让整页降级")
	}
}

func Test预算充裕时全部转发(t *testing.T) {
	srv, 命中 := 慢服务(t, 50*time.Millisecond)

	opt := 测试选项带基准()
	opt.JSFetchBudget = 5 * time.Second
	res, err := Render(慢页, srv.URL+"/p", opt)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if 命中() != 5 || !strings.Contains(res.Text, "ok4") {
		t.Errorf("预算充裕时不该少取:转发 %d 次,正文=%q", 命中(), res.Text)
	}
}

func Test预算零值用默认值(t *testing.T) {
	srv, 命中 := 慢服务(t, 50*time.Millisecond)

	// 零值 = 用默认的 10s:这一页实际只用 250ms,不该被拒
	res, err := Render(慢页, srv.URL+"/p", 测试选项带基准())
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if 命中() != 5 || !strings.Contains(res.Text, "ok4") {
		t.Errorf("零值应当走默认预算(10s)而不是 0:转发 %d 次,正文=%q", 命中(), res.Text)
	}
}
