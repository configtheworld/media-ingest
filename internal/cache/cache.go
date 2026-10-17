// Package cache is a small in-memory TTL cache with a size cap.
package cache

import (
	"sync"
	"time"
)

type entry struct {
	val     any
	expires time.Time
}

type TTL struct {
	mu  sync.Mutex
	m   map[string]entry
	max int
	now func() time.Time
}

func New(max int) *TTL { return &TTL{m: map[string]entry{}, max: max, now: time.Now} }

func (c *TTL) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return nil, false
	}
	if c.now().After(e.expires) {
		delete(c.m, key)
		return nil, false
	}
	return e.val, true
}

func (c *TTL) Set(key string, val any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		now := c.now()
		for k, e := range c.m { // drop expired entries first
			if now.After(e.expires) {
				delete(c.m, k)
			}
		}
		for k := range c.m { // still full: drop arbitrary entries
			if len(c.m) < c.max {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[key] = entry{val: val, expires: c.now().Add(ttl)}
}
