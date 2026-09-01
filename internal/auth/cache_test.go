package auth

import (
	"testing"
	"time"
)

func TestVerificationCacheExpiresEntries(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newVerificationCache(2, func() time.Time { return now })
	cache.Set("expired", cacheEntry{ok: true, exp: now.Add(time.Second)})
	now = now.Add(2 * time.Second)
	if _, ok := cache.Get("expired"); ok {
		t.Fatal("expired verification result remained readable")
	}
	if cache.Len() != 0 {
		t.Fatalf("cache Len() = %d, want 0 after expiration", cache.Len())
	}
}

func TestVerificationCacheEvictsLeastRecentlyUsed(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newVerificationCache(2, func() time.Time { return now })
	entry := cacheEntry{ok: true, exp: now.Add(time.Hour)}
	cache.Set("a", entry)
	cache.Set("b", entry)
	_, _ = cache.Get("a")
	cache.Set("c", entry)
	if _, ok := cache.Get("b"); ok {
		t.Fatal("least recently used entry b was not evicted")
	}
	if cache.Len() != 2 {
		t.Fatalf("cache Len() = %d, want 2", cache.Len())
	}
}
