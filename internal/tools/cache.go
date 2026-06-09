package tools

import (
	"container/list"
	"sync"
	"time"
)

const (
	// defaultMaxEntries 是缓存默认容量上限,超出后按 LRU 淘汰最久未用项。
	defaultMaxEntries = 1024
	// defaultJanitorInterval 是后台清理 goroutine 的默认扫描周期。
	defaultJanitorInterval = time.Minute
)

// cacheItem 是链表节点承载的实际数据。expiration 为零值表示永不过期。
type cacheItem struct {
	key        string
	value      any
	expiration time.Time
}

func (it *cacheItem) expired(now time.Time) bool {
	return !it.expiration.IsZero() && now.After(it.expiration)
}

// MemoryCache 是并发安全的内存缓存,具备三重内存保护:
//  1. TTL 过期:Get 命中过期项时即时删除(惰性);
//  2. 后台 janitor:定期扫描并清理已过期但从未被再次 Get 的项(防止「只写不再读」的 key
//     永久占用内存——这正是旧 sync.Map 版本的慢性泄漏点);
//  3. 容量上限 + LRU 淘汰:总条目数超过 maxEntries 时,从链表尾部淘汰最久未使用项,
//     保证内存有硬上界,即便 janitor 周期内涌入大量不同 key 也不会无界增长。
//
// 内部用双向链表维护 LRU 顺序(Front=最近使用,Back=最久未用),map 提供 O(1) 定位。
type MemoryCache struct {
	mu         sync.Mutex
	ll         *list.List               // 元素 Value 为 *cacheItem
	items      map[string]*list.Element // key -> 链表节点
	maxEntries int                      // <=0 表示不限容量(仅靠 TTL+janitor 回收)

	janitorStop chan struct{}
	stopOnce    sync.Once
}

// NewMemoryCache 用默认容量(1024)与默认清理周期(1min)构造缓存,并启动后台清理 goroutine。
func NewMemoryCache() *MemoryCache {
	return NewMemoryCacheWithOptions(defaultMaxEntries, defaultJanitorInterval)
}

// NewMemoryCacheWithOptions 自定义容量上限与清理周期:
//   - maxEntries <= 0:不限容量(只靠 TTL + janitor 回收);
//   - janitorInterval <= 0:不启动后台清理(仅 Get 时惰性过期 + LRU 淘汰)。
//
// 不启动 janitor 时务必设置正的 maxEntries,否则永不过期的 key 仍可能无界增长。
func NewMemoryCacheWithOptions(maxEntries int, janitorInterval time.Duration) *MemoryCache {
	c := &MemoryCache{
		ll:          list.New(),
		items:       make(map[string]*list.Element),
		maxEntries:  maxEntries,
		janitorStop: make(chan struct{}),
	}
	if janitorInterval > 0 {
		go c.janitor(janitorInterval)
	}
	return c
}

// Get 返回 key 对应值;未命中或已过期返回 (nil,false)。命中会把该项标记为最近使用。
func (c *MemoryCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	it := el.Value.(*cacheItem)
	if it.expired(time.Now()) {
		c.removeElement(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return it.value, true
}

// Set 写入/更新 key。ttl<=0 表示永不过期(仍受容量上限约束)。写入后若超过容量上限,
// 立即从尾部按 LRU 淘汰,直至不超限。
func (c *MemoryCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}

	if el, ok := c.items[key]; ok {
		it := el.Value.(*cacheItem)
		it.value = value
		it.expiration = exp
		c.ll.MoveToFront(el)
		return
	}

	el := c.ll.PushFront(&cacheItem{key: key, value: value, expiration: exp})
	c.items[key] = el

	if c.maxEntries > 0 {
		for c.ll.Len() > c.maxEntries {
			c.removeOldest()
		}
	}
}

// Delete 删除指定 key(不存在则无操作)。
func (c *MemoryCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

// Len 返回当前缓存条目数(含尚未被清理的过期项)。
func (c *MemoryCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Stop 停止后台清理 goroutine,可安全地重复调用。进程级长生命周期缓存通常无需调用;
// 测试或临时缓存应调用以避免 goroutine 泄漏。
func (c *MemoryCache) Stop() {
	c.stopOnce.Do(func() { close(c.janitorStop) })
}

// removeOldest 淘汰链表尾部(最久未用)的一项。调用方须持有 c.mu。
func (c *MemoryCache) removeOldest() {
	if el := c.ll.Back(); el != nil {
		c.removeElement(el)
	}
}

// removeElement 同时从链表与 map 中移除节点。调用方须持有 c.mu。
func (c *MemoryCache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.items, el.Value.(*cacheItem).key)
}

// janitor 定期触发过期清理,直到 Stop 被调用。
func (c *MemoryCache) janitor(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.janitorStop:
			return
		case <-ticker.C:
			c.deleteExpired()
		}
	}
}

// deleteExpired 扫描并删除所有已过期项。容量上限保证条目数有界,O(n) 全表扫描可接受。
// 在 range 中 delete map 元素在 Go 中是安全的。
func (c *MemoryCache) deleteExpired() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, el := range c.items {
		if el.Value.(*cacheItem).expired(now) {
			c.removeElement(el)
		}
	}
}

// sharedCache 是各工具复用的同一层缓存。不同工具用 "<tool>:<op>:<参数>" 命名空间隔离键,
// 共用一个容量上限与一个 janitor goroutine(即用户所说「复用同一层」)。
var sharedCache = NewMemoryCache()
