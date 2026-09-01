package auth

import (
	"container/list"
	"sync"
	"time"
)

type verificationCacheItem struct {
	key   string
	entry cacheEntry
}

type verificationCache struct {
	mu         sync.Mutex
	items      map[string]*list.Element
	lru        *list.List
	maxEntries int
	now        func() time.Time
}

func newVerificationCache(maxEntries int, now func() time.Time) *verificationCache {
	if maxEntries <= 0 {
		panic("auth: verification cache capacity must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &verificationCache{
		items:      make(map[string]*list.Element),
		lru:        list.New(),
		maxEntries: maxEntries,
		now:        now,
	}
}

func (c *verificationCache) Get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	item := el.Value.(*verificationCacheItem)
	if !item.entry.exp.IsZero() && !c.now().Before(item.entry.exp) {
		c.remove(el)
		return cacheEntry{}, false
	}
	c.lru.MoveToFront(el)
	return item.entry, true
}

func (c *verificationCache) Set(key string, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	for _, el := range c.items {
		item := el.Value.(*verificationCacheItem)
		if !item.entry.exp.IsZero() && !now.Before(item.entry.exp) {
			c.remove(el)
		}
	}
	if el, ok := c.items[key]; ok {
		el.Value.(*verificationCacheItem).entry = entry
		c.lru.MoveToFront(el)
		return
	}
	c.items[key] = c.lru.PushFront(&verificationCacheItem{key: key, entry: entry})
	for c.lru.Len() > c.maxEntries {
		c.remove(c.lru.Back())
	}
}

func (c *verificationCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *verificationCache) remove(el *list.Element) {
	delete(c.items, el.Value.(*verificationCacheItem).key)
	c.lru.Remove(el)
}
