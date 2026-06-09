package tools

import (
	"testing"
	"time"
)

func TestMemoryCache_GetSet(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Stop()

	// Test cache miss
	if _, ok := cache.Get("key1"); ok {
		t.Error("expected cache miss for 'key1'")
	}

	// Test cache hit after Set
	cache.Set("key1", "value1", 100*time.Millisecond)
	val, ok := cache.Get("key1")
	if !ok {
		t.Fatal("expected cache hit for 'key1'")
	}
	if val.(string) != "value1" {
		t.Errorf("expected value 'value1', got %v", val)
	}

	// Test cache expiration (lazy, on Get)
	time.Sleep(150 * time.Millisecond)
	if _, ok := cache.Get("key1"); ok {
		t.Error("expected cache key1 to have expired")
	}
}

// 更新已存在的 key:值被覆盖,且不新增条目。
func TestMemoryCache_Update(t *testing.T) {
	cache := NewMemoryCacheWithOptions(10, 0) // 无 janitor,确定性
	defer cache.Stop()

	cache.Set("k", "v1", time.Minute)
	cache.Set("k", "v2", time.Minute)
	if cache.Len() != 1 {
		t.Fatalf("更新同一 key 不应新增条目,Len=%d", cache.Len())
	}
	if v, _ := cache.Get("k"); v.(string) != "v2" {
		t.Errorf("期望更新为 v2,得到 %v", v)
	}
}

// 容量上限 + LRU 淘汰:超容时淘汰最久未使用项,而非最早写入项。
func TestMemoryCache_LRUEviction(t *testing.T) {
	cache := NewMemoryCacheWithOptions(2, 0) // 容量 2,无 janitor
	defer cache.Stop()

	cache.Set("a", 1, time.Minute)
	cache.Set("b", 2, time.Minute)

	// 访问 a,使其成为最近使用;b 变为最久未用。
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("a 应命中")
	}

	// 写入 c 触发淘汰:应淘汰 b(最久未用),保留 a 与 c。
	cache.Set("c", 3, time.Minute)

	if cache.Len() != 2 {
		t.Fatalf("容量上限应为 2,Len=%d", cache.Len())
	}
	if _, ok := cache.Get("b"); ok {
		t.Error("b 应被 LRU 淘汰")
	}
	if _, ok := cache.Get("a"); !ok {
		t.Error("a 最近被访问,不应被淘汰")
	}
	if _, ok := cache.Get("c"); !ok {
		t.Error("c 是最新写入,不应被淘汰")
	}
}

// 容量上限是硬上界:即便涌入远超容量的不同 key,条目数也不突破上限。
func TestMemoryCache_CapacityHardBound(t *testing.T) {
	const cap = 50
	cache := NewMemoryCacheWithOptions(cap, 0)
	defer cache.Stop()

	for i := 0; i < 1000; i++ {
		cache.Set(string(rune(i)), i, time.Minute)
		if cache.Len() > cap {
			t.Fatalf("条目数 %d 超过容量上限 %d", cache.Len(), cap)
		}
	}
	if cache.Len() != cap {
		t.Fatalf("写满后应恰好为容量上限 %d,得到 %d", cap, cache.Len())
	}
}

// janitor 的核心逻辑:deleteExpired 清掉「过期但从未被再次 Get」的项
// ——这正是旧 sync.Map 版本的泄漏点。这里直接调用以保证确定性。
func TestMemoryCache_DeleteExpired(t *testing.T) {
	cache := NewMemoryCacheWithOptions(100, 0) // 手动驱动,不靠后台 ticker
	defer cache.Stop()

	cache.Set("short", "x", 20*time.Millisecond)
	cache.Set("long", "y", time.Hour)

	time.Sleep(40 * time.Millisecond)
	cache.deleteExpired()

	if cache.Len() != 1 {
		t.Fatalf("过期项应被清理,期望 Len=1,得到 %d", cache.Len())
	}
	if _, ok := cache.Get("short"); ok {
		t.Error("short 已过期,应被 janitor 清掉")
	}
	if _, ok := cache.Get("long"); !ok {
		t.Error("long 未过期,不应被清掉")
	}
}

// 后台 janitor 端到端:短周期 ticker 能自动回收过期项(无需手动 Get/调用)。
func TestMemoryCache_JanitorBackground(t *testing.T) {
	cache := NewMemoryCacheWithOptions(100, 15*time.Millisecond)
	defer cache.Stop()

	cache.Set("k", "v", 10*time.Millisecond)
	// 等待足够时间让至少一次 ticker 触发清理。
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cache.Len() == 0 {
			return // 已被后台清理
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("后台 janitor 应自动清理过期项,但 Len 仍为 %d", cache.Len())
}

// Stop 幂等:多次调用不应 panic。
func TestMemoryCache_StopIdempotent(t *testing.T) {
	cache := NewMemoryCache()
	cache.Stop()
	cache.Stop() // 第二次不应 panic(sync.Once 保护)
}

// ttl<=0 表示永不过期(仍受容量上限约束)。
func TestMemoryCache_NoExpiry(t *testing.T) {
	cache := NewMemoryCacheWithOptions(10, 0)
	defer cache.Stop()

	cache.Set("k", "v", 0)
	time.Sleep(20 * time.Millisecond)
	cache.deleteExpired()
	if _, ok := cache.Get("k"); !ok {
		t.Error("ttl=0 应永不过期")
	}
}
