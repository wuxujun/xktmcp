package tools

import (
	"testing"
	"time"
)

func TestMemoryCache_GetSet(t *testing.T) {
	cache := NewMemoryCache()

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

	// Test cache expiration
	time.Sleep(150 * time.Millisecond)
	if _, ok := cache.Get("key1"); ok {
		t.Error("expected cache key1 to have expired")
	}
}
