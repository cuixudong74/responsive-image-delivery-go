package delivery

import (
	"container/list"
	"sync"
)

// cacheKey uniquely identifies a derivative: it depends only on the
// source content hash, the source version, the target spec and the
// generator configuration, so results are reproducible and
// invalidation by source is exact.
type cacheKey struct {
	SrcHash  string
	Version  uint64
	Width    int
	Height   int
	Format   Format
	Quality  int
	ConfigID int
}

// entry is a cached derivative.
type entry struct {
	key   cacheKey
	data  []byte
	bytes int64
	elem  *list.Element
}

// call represents an in-flight or completed generation (singleflight).
type call struct {
	done chan struct{}
	data []byte
	err  error
}

// cache is a byte-bounded LRU store with singleflight generation.
// A slow generation of one key never blocks lookups or generations of
// other keys: the mutex is only held for map operations.
type cache struct {
	mu       sync.Mutex
	maxSize  int64
	size     int64
	items    map[cacheKey]*entry
	lru      *list.List // front = most recently used
	inflight map[cacheKey]*call
}

func newCache(maxBytes int64) *cache {
	if maxBytes <= 0 {
		maxBytes = 1 << 30
	}
	return &cache{
		maxSize:  maxBytes,
		items:    map[cacheKey]*entry{},
		lru:      list.New(),
		inflight: map[cacheKey]*call{},
	}
}

// get returns a cached value and marks it recently used.
func (c *cache) get(k cacheKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(e.elem)
	return e.data, true
}

// add stores a value, evicting least-recently-used entries to stay
// within the byte budget. Values larger than the budget are not
// stored at all, keeping memory bounded.
func (c *cache) add(k cacheKey, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addLocked(k, data)
}

// addLocked is add; the caller must hold c.mu.
func (c *cache) addLocked(k cacheKey, data []byte) {
	n := int64(len(data))
	if old, ok := c.items[k]; ok {
		c.size -= old.bytes
		c.lru.Remove(old.elem)
		delete(c.items, k)
	}
	if n > c.maxSize {
		return
	}
	for c.size+n > c.maxSize && c.lru.Len() > 0 {
		back := c.lru.Back()
		be := back.Value.(*entry)
		c.size -= be.bytes
		delete(c.items, be.key)
		c.lru.Remove(back)
	}
	e := &entry{key: k, data: data, bytes: n}
	e.elem = c.lru.PushFront(e)
	c.items[k] = e
	c.size += n
}

// invalidateHash drops every entry derived from srcHash. Returns the
// number of entries removed. In-flight generations are left alone:
// when they finish they store under their own (stale) key, which is
// unreachable because lookups always use the current hash/version.
func (c *cache) invalidateHash(srcHash string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := 0
	for k, e := range c.items {
		if k.SrcHash == srcHash {
			c.size -= e.bytes
			c.lru.Remove(e.elem)
			delete(c.items, k)
			removed++
		}
	}
	return removed
}

// outcome describes how a do() call obtained its result.
type outcome int

const (
	outcomeGenerated outcome = iota // this caller ran fn
	outcomeHit                      // found in the cache
	outcomeCoalesced                // waited on an in-flight call
)

// do implements singleflight: the first caller for k becomes the
// leader and runs fn; concurrent callers wait on the same call and
// share its result. The completed result is published and the
// in-flight marker removed under one lock section, so a key is never
// generated twice.
func (c *cache) do(k cacheKey, fn func() ([]byte, error)) (data []byte, oc outcome, err error) {
	c.mu.Lock()
	if e, ok := c.items[k]; ok {
		c.lru.MoveToFront(e.elem)
		c.mu.Unlock()
		return e.data, outcomeHit, nil
	}
	if cl, ok := c.inflight[k]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.data, outcomeCoalesced, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[k] = cl
	c.mu.Unlock()

	cl.data, cl.err = fn()
	close(cl.done)

	c.mu.Lock()
	delete(c.inflight, k)
	if cl.err == nil {
		c.addLocked(k, cl.data)
	}
	c.mu.Unlock()
	return cl.data, outcomeGenerated, cl.err
}
