package test

import (
	"testing"
	"time"

	search "MaiGo/internal/webui/services/ai_search"
)

func TestAISearchCacheHitIsMarkedCached(t *testing.T) {
	cache := search.NewCache(time.Minute, 4)
	cache.Set("key", search.Response{Answer: "hello"})

	got, ok := cache.Get("key")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !got.Cached {
		t.Fatal("expected Cached=true on hit")
	}
	if got.Answer != "hello" {
		t.Fatalf("unexpected answer: %q", got.Answer)
	}
}

func TestAISearchCacheReturnsCopy(t *testing.T) {
	cache := search.NewCache(time.Minute, 4)
	cache.Set("key", search.Response{Suggestions: []string{"a"}})

	got, _ := cache.Get("key")
	got.Suggestions[0] = "mutated"

	again, _ := cache.Get("key")
	if again.Suggestions[0] != "a" {
		t.Fatalf("cached value was mutated through a returned copy: %#v", again.Suggestions)
	}
}

func TestAISearchCacheExpires(t *testing.T) {
	cache := search.NewCache(10*time.Millisecond, 4)
	cache.Set("key", search.Response{Answer: "hello"})
	time.Sleep(30 * time.Millisecond)
	if _, ok := cache.Get("key"); ok {
		t.Fatal("expected entry to expire")
	}
}

func TestAISearchCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := search.NewCache(time.Minute, 2)
	cache.Set("a", search.Response{Answer: "a"})
	cache.Set("b", search.Response{Answer: "b"})
	// 读取 a 后，b 成为最久未使用项
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("expected a to be present")
	}
	cache.Set("c", search.Response{Answer: "c"})

	if _, ok := cache.Get("b"); ok {
		t.Fatal("expected b to be evicted")
	}
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("expected a to survive eviction")
	}
}

func TestAISearchCacheInvalidArgumentsFallBackToDefaults(t *testing.T) {
	cache := search.NewCache(0, 0)
	cache.Set("key", search.Response{Answer: "hello"})
	if _, ok := cache.Get("key"); !ok {
		t.Fatal("expected default-configured cache to work")
	}
}
