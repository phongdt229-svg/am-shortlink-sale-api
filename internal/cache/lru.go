// Package cache: LRU trong RAM có TTL (tầng 1 của đường redirect).
package cache

import (
	"container/list"
	"sync"
	"time"
)

type LRU[V any] struct {
	mu    sync.Mutex
	size  int
	ttl   time.Duration
	ll    *list.List
	items map[string]*list.Element
}

type item[V any] struct {
	key string
	val V
	exp time.Time
}

func NewLRU[V any](size int, ttl time.Duration) *LRU[V] {
	if size <= 0 {
		size = 10000
	}
	return &LRU[V]{size: size, ttl: ttl, ll: list.New(), items: make(map[string]*list.Element, size)}
}

func (c *LRU[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	el, ok := c.items[key]
	if !ok {
		return zero, false
	}
	it := el.Value.(*item[V])
	if time.Now().After(it.exp) {
		c.ll.Remove(el)
		delete(c.items, key)
		return zero, false
	}
	c.ll.MoveToFront(el)
	return it.val, true
}

// Set lưu với TTL mặc định; ttl > 0 ghi đè.
func (c *LRU[V]) Set(key string, v V, ttl time.Duration) {
	if ttl <= 0 {
		ttl = c.ttl
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		it := el.Value.(*item[V])
		it.val, it.exp = v, time.Now().Add(ttl)
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(&item[V]{key: key, val: v, exp: time.Now().Add(ttl)})
	for c.ll.Len() > c.size {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.items, last.Value.(*item[V]).key)
	}
}

func (c *LRU[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.Remove(el)
		delete(c.items, key)
	}
}

func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
