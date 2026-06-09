package tools

import (
	"sync"
	"time"
)

type cacheItem struct {
	value      any
	expiration time.Time
}

type MemoryCache struct {
	items sync.Map
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{}
}

func (c *MemoryCache) Get(key string) (any, bool) {
	val, ok := c.items.Load(key)
	if !ok {
		return nil, false
	}
	item := val.(cacheItem)
	if time.Now().After(item.expiration) {
		c.items.Delete(key)
		return nil, false
	}
	return item.value, true
}

func (c *MemoryCache) Set(key string, value any, ttl time.Duration) {
	c.items.Store(key, cacheItem{
		value:      value,
		expiration: time.Now().Add(ttl),
	})
}
