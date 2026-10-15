package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGateLimitsConcurrency 钉住:同时进入闸门的请求数不超过上限。
func TestGateLimitsConcurrency(t *testing.T) {
	闸 := 新并发闸(2, 10)

	var 同时, 峰值 int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			释放, err := 闸.进入()
			if err != nil {
				return
			}
			mu.Lock()
			同时++
			if 同时 > 峰值 {
				峰值 = 同时
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			同时--
			mu.Unlock()
			释放()
		}()
	}
	wg.Wait()
	if 峰值 > 2 {
		t.Errorf("同时进入 %d 个,超过了上限 2", 峰值)
	}
	if 峰值 == 0 {
		t.Error("没有任何请求被放行,闸门把路堵死了")
	}
	处理中, 排队中, 拒绝 := 闸.状态()
	if 处理中 != 0 || 排队中 != 0 {
		t.Errorf("全部结束后应当归零,实际 处理中=%d 排队中=%d", 处理中, 排队中)
	}
	if 拒绝 != 0 {
		t.Errorf("8 个请求、上限 2、排队 10,不该有拒绝,实际拒绝 %d", 拒绝)
	}
}

// TestGateQueuesInsteadOfFailing 钉住排队语义:槽位满了要先排队(查询类请求
// 等一会儿比直接失败好),排到才会放行。
func TestGateQueuesInsteadOfFailing(t *testing.T) {
	闸 := 新并发闸(1, 5)
	释放, err := 闸.进入()
	if err != nil {
		t.Fatalf("第一个请求应当直接拿到槽位: %v", err)
	}

	已放行 := make(chan struct{})
	go func() {
		释放2, err := 闸.进入()
		if err != nil {
			close(已放行)
			return
		}
		defer 释放2()
		close(已放行)
	}()

	select {
	case <-已放行:
		t.Fatal("槽位没释放就放行了第二个请求")
	case <-time.After(80 * time.Millisecond):
		// 正确:在排队
	}
	if _, 排队中, _ := 闸.状态(); 排队中 != 1 {
		t.Errorf("应当有 1 个在排队,实际 %d", 排队中)
	}

	释放()
	select {
	case <-已放行:
		// 正确:释放后轮到它
	case <-time.After(time.Second):
		t.Fatal("释放槽位后排队者没被放行")
	}
}

// TestGateRejectsWhenQueueFull 钉住队列上限:排队也满了就该明确拒绝,
// 而不是把无上限的请求堆在内存里(这正是"有界环缓冲"相对无限队列的意义)。
func TestGateRejectsWhenQueueFull(t *testing.T) {
	闸 := 新并发闸(1, 2)
	释放, err := 闸.进入()
	if err != nil {
		t.Fatal(err)
	}

	// 先把 2 个排队位占满
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if 释放, err := 闸.进入(); err == nil {
				time.Sleep(30 * time.Millisecond)
				释放()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)

	放行, err := 闸.进入()
	if err == nil {
		放行()
		t.Fatal("排队已满时仍然放行,队列没有上限")
	}
	if !strings.Contains(err.Error(), "服务忙") {
		t.Errorf("拒绝理由应当是人能看懂的「服务忙」,实际 %v", err)
	}
	if _, _, 拒绝 := 闸.状态(); 拒绝 != 1 {
		t.Errorf("拒绝计数应当是 1,实际 %d", 拒绝)
	}

	// 必须先把槽位放掉,排队的两个才可能被放行 —— 否则 wg.Wait() 会一直等
	释放()
	wg.Wait()
}

// TestGateZeroAndNegativeConfig 钉住非法配置不会把服务锁死。
func TestGateZeroAndNegativeConfig(t *testing.T) {
	闸 := 新并发闸(0, -1) // 上限 0 当 1 用;负排队当 0 用
	释放, err := 闸.进入()
	if err != nil {
		t.Fatalf("上限写成 0 不该让服务起不来: %v", err)
	}
	释放()
	if _, _, 拒绝数 := 闸.状态(); 拒绝数 != 0 {
		t.Errorf("刚启动不该有拒绝,实际 %d", 拒绝数)
	}
}
