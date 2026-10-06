package delivery

import (
	"context"
	"strings"
	"sync"
)

// Derivative 是一个已生成的派生结果。
type Derivative struct {
	Key   string
	Spec  VariantSpec
	Data  []byte
	Bytes int64
}

// Cache 是派生结果缓存：
//   - 相同键的并发请求只触发一次生成（singleflight），其余等待并复用；
//   - 生成结果先落在临时缓冲，成功后才对后续请求可见，
//     取消/超时/失败不会留下会被当作有效结果的半成品；
//   - 容量超限时按 LRU 逐出，内存占用有界。
type Cache struct {
	mu        sync.Mutex
	entries   map[string]*cacheEntry
	inflight  map[string]*call
	maxBytes  int64
	usedBytes int64
	clock     uint64
}

type cacheEntry struct {
	d     Derivative
	lruAt uint64
}

type call struct {
	done chan struct{}
	d    Derivative
	err  error
}

// NewCache 构造缓存；maxBytes<=0 表示不限制容量。
func NewCache(maxBytes int64) *Cache {
	return &Cache{entries: map[string]*cacheEntry{}, inflight: map[string]*call{}, maxBytes: maxBytes}
}

// GetOrGenerate 返回键对应的结果：命中则复用（reused=true）；
// 未命中时只有一个调用者执行 generate，其余并发调用等待同一结果（reused=true）。
// generate 返回错误或 ctx 取消时，错误会传播给所有等待者且不留缓存。
// 等待中的调用者若自身 ctx 取消，仅自身返回错误，不影响进行中的生成。
func (c *Cache) GetOrGenerate(ctx context.Context, key string, spec VariantSpec, generate func(context.Context) ([]byte, error)) (Derivative, bool, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.clock++
		e.lruAt = c.clock
		d := e.d
		c.mu.Unlock()
		return d, true, nil
	}
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-cl.done:
			return cl.d, true, cl.err
		case <-ctx.Done():
			return Derivative{}, true, ctx.Err()
		}
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()

	data, err := generate(ctx)

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		cl.d = Derivative{Key: key, Spec: spec, Data: data, Bytes: int64(len(data))}
		c.commitLocked(cl.d)
	}
	cl.err = err
	close(cl.done)
	c.mu.Unlock()
	return cl.d, false, err
}

// commitLocked 把结果放入缓存并按需逐出（调用方须持锁）。
func (c *Cache) commitLocked(d Derivative) {
	c.clock++
	c.entries[d.Key] = &cacheEntry{d: d, lruAt: c.clock}
	c.usedBytes += d.Bytes
	if c.maxBytes > 0 {
		for c.usedBytes > c.maxBytes && len(c.entries) > 0 {
			// 找最久未使用的条目逐出。
			var oldestKey string
			var oldestAt uint64
			first := true
			for k, e := range c.entries {
				if first || e.lruAt < oldestAt {
					oldestKey, oldestAt, first = k, e.lruAt, false
				}
			}
			c.usedBytes -= c.entries[oldestKey].d.Bytes
			delete(c.entries, oldestKey)
		}
	}
}

// Invalidate 删除指定键；返回是否删掉了条目。
func (c *Cache) Invalidate(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.usedBytes -= e.d.Bytes
		delete(c.entries, key)
		return true
	}
	return false
}

// InvalidatePrefix 删除所有以 prefix 开头的键（用于源图/配置变化后的批量失效），
// 返回删除的条目数。进行中的生成不受影响：其完成后写入的是新键，
// 旧键不会再被返回。
func (c *Cache) InvalidatePrefix(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, e := range c.entries {
		if strings.HasPrefix(k, prefix) {
			c.usedBytes -= e.d.Bytes
			delete(c.entries, k)
			n++
		}
	}
	return n
}

// Stats 返回缓存条目数与占用字节数。
func (c *Cache) Stats() (entries int, usedBytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.usedBytes
}
