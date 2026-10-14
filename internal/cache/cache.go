// Package cache 缓存抓取到的页面源码。
//
// 用途:同一个 URL 换 mode 重试时(比如 article 读不出内容、改试 all),
// 不必重新抓取。缓存的是【HTML 源码】而不是提取结果 —— 抓取与渲染本就是
// 分开的两步,所以换 mode 只重跑渲染,重试几乎零成本。
//
// 两道防线防止挤爆磁盘:
//
//	TTL      —— 超时的条目视为失效(网页会变,不能无限信任)
//	容量上限 —— 超出后按最久未访问淘汰(LRU)
//
// 每次写入前先清理一遍,所以长期运行也不会无限增长。
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options 是缓存配置。零值经 normalize 后变成一套保守的默认值。
type Options struct {
	// Dir 缓存目录。默认 os.UserCacheDir()/qjsdom。
	Dir string
	// TTL 条目有效期。默认 10 分钟 —— 网页内容变化快,
	// 缓存的价值主要在「同一次任务里换 mode 重试」这种秒级场景。
	TTL time.Duration
	// MaxBytes 目录总容量上限。默认 64 MB。
	MaxBytes int64
}

// DefaultTTL 与 DefaultMaxBytes 是 normalize 使用的兜底值。
const (
	DefaultTTL      = 10 * time.Minute
	DefaultMaxBytes = 64 << 20 // 64 MB
)

func (o Options) normalize() Options {
	if o.Dir == "" {
		base, err := os.UserCacheDir()
		if err != nil || base == "" {
			base = os.TempDir()
		}
		o.Dir = filepath.Join(base, "qjsdom")
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	return o
}

// Cache 是一个基于文件的页面源码缓存。并发安全由调用方保证 ——
// CLI 与单进程服务都是顺序访问。
type Cache struct {
	opt Options
}

// New 创建缓存。不会立刻创建目录(首次 Put 时再建)。
func New(opt Options) *Cache {
	return &Cache{opt: opt.normalize()}
}

// Dir 返回实际使用的缓存目录。
func (c *Cache) Dir() string { return c.opt.Dir }

// Stats 是一次清理的结果。
type Stats struct {
	Entries    int   // 清理后剩余条目数
	Bytes      int64 // 清理后占用字节数
	Expired    int   // 因超时删除的条目数
	Evicted    int   // 因超容量淘汰的条目数
	FreedBytes int64 // 释放的总字节数
}

// keyPath 把 URL 映射成缓存文件路径。
// 用哈希而不是 URL 本身,避免文件名非法字符与路径穿越。
func (c *Cache) keyPath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(c.opt.Dir, hex.EncodeToString(sum[:])[:32]+".html")
}

// Get 读取缓存。命中条件:文件存在、未超过 TTL、大小非零。
//
// 命中时会 touch 文件的访问时间(mtime),以便 LRU 淘汰时知道它是热的。
func (c *Cache) Get(url string) (string, bool) {
	p := c.keyPath(url)
	fi, err := os.Stat(p)
	if err != nil || fi.Size() == 0 {
		return "", false
	}
	if time.Since(fi.ModTime()) > c.opt.TTL {
		// 过期即删,顺手回收空间
		_ = os.Remove(p)
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	// 刷新 mtime 供 LRU 使用
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return string(data), true
}

// Put 写入缓存。写入前先清理(超时 + 超容量),所以长期运行不会无限增长。
//
// 超过容量上限的单个条目会被拒绝 —— 不值得为它挤掉整个缓存。
func (c *Cache) Put(url, html string) error {
	if html == "" {
		return nil
	}
	if int64(len(html)) > c.opt.MaxBytes {
		return fmt.Errorf("页面 %.1f MB 超过缓存容量上限 %.1f MB,不缓存",
			float64(len(html))/(1<<20), float64(c.opt.MaxBytes)/(1<<20))
	}
	if err := os.MkdirAll(c.opt.Dir, 0o700); err != nil {
		return fmt.Errorf("创建缓存目录失败: %w", err)
	}

	// 先清理一次,给新条目腾出空间
	c.Purge()

	p := c.keyPath(url)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(html), 0o600); err != nil {
		return fmt.Errorf("写缓存失败: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交缓存失败: %w", err)
	}

	// 写入后再清理一次 —— 这一步是必须的。
	// 只清理一次的话,新条目在清理时还不存在,于是会出现
	// 「清理时没超容量、写完就超了」的情况(实测:上限 600KB,
	// 两个 440KB 的页面同时留在目录里)。
	c.Purge()
	return nil
}

// Purge 清理过期条目,再按 LRU 淘汰直到总容量回到上限内。
func (c *Cache) Purge() Stats {
	var st Stats

	entries, err := os.ReadDir(c.opt.Dir)
	if err != nil {
		return st // 目录不存在 = 空缓存
	}

	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var live []item
	var total int64

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			// 顺手清掉上次崩溃遗留的临时文件
			if strings.HasSuffix(e.Name(), ".tmp") {
				_ = os.Remove(filepath.Join(c.opt.Dir, e.Name()))
			}
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(c.opt.Dir, e.Name())
		if time.Since(fi.ModTime()) > c.opt.TTL {
			if err := os.Remove(p); err == nil {
				st.Expired++
				st.FreedBytes += fi.Size()
			}
			continue
		}
		live = append(live, item{path: p, size: fi.Size(), mod: fi.ModTime()})
		total += fi.Size()
	}

	// 仍然超容量 → 从最旧的开始淘汰(LRU 的近似:mtime 越早越冷)
	if total > c.opt.MaxBytes {
		sort.Slice(live, func(i, j int) bool { return live[i].mod.Before(live[j].mod) })
		for _, it := range live {
			if total <= c.opt.MaxBytes {
				break
			}
			if err := os.Remove(it.path); err == nil {
				st.Evicted++
				st.FreedBytes += it.size
				total -= it.size
				st.Entries--
			}
		}
	}

	st.Entries += len(live)
	st.Bytes = total
	return st
}

// Clear 删除整个缓存目录。
func (c *Cache) Clear() error {
	return os.RemoveAll(c.opt.Dir)
}
