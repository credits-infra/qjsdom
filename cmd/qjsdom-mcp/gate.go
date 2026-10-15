package main

import (
	"fmt"
	"sync/atomic"
)

// 并发闸 限制同时进行的页面请求数。
//
// 为什么需要:一次渲染会新建一个 qjs 运行时(设计如此,见 decisions.md 的
// 「One JavaScript runtime per page」),而一个运行时实测峰值约 120 MB 且
// 不归还给操作系统 —— 8 个并发请求实测把常驻内存从 0.7 MB 抬到 452 MB。
// MCP 服务是长驻进程,不设上限的话,并发数就是内存占用。
//
// 语义:槽位满了之后排队的请求继续等(查询类请求适合排队,不该直接失败),
// 但排队数也有上限 —— 超过就明确拒绝,而不是把无上限的请求堆在内存里
// (那正是"环缓冲"相对"无限队列"的意义)。
type 并发闸 struct {
	槽    chan struct{}
	最多排队 int64
	正在处理 atomic.Int64
	正在排队 atomic.Int64
	累计拒绝 atomic.Int64
}

func 新并发闸(并发上限, 排队上限 int) *并发闸 {
	if 并发上限 < 1 {
		并发上限 = 1
	}
	if 排队上限 < 0 {
		排队上限 = 0
	}
	return &并发闸{槽: make(chan struct{}, 并发上限), 最多排队: int64(排队上限)}
}

// 进入 取一个槽位。返回释放函数;队列已满时返回 nil 与原因。
func (g *并发闸) 进入() (func(), error) {
	// 先试无阻塞地拿槽位:这是常见路径(空闲时不该有任何计数开销)
	select {
	case g.槽 <- struct{}{}:
		g.正在处理.Add(1)
		return g.释放, nil
	default:
	}

	// 槽位满了 → 想排队。排队前先看队列还有没有位置。
	if g.正在排队.Add(1) > g.最多排队 {
		g.正在排队.Add(-1)
		g.累计拒绝.Add(1)
		return nil, fmt.Errorf("服务忙:同时处理的页面请求已达上限(%d),排队也已满(%d)",
			cap(g.槽), g.最多排队)
	}
	defer g.正在排队.Add(-1)

	g.槽 <- struct{}{} // 阻塞等,直到有人释放
	g.正在处理.Add(1)
	return g.释放, nil
}

func (g *并发闸) 释放() {
	<-g.槽
	g.正在处理.Add(-1)
}

// 状态 供诊断输出。
func (g *并发闸) 状态() (处理中, 排队中, 拒绝数 int64) {
	return g.正在处理.Load(), g.正在排队.Load(), g.累计拒绝.Load()
}
