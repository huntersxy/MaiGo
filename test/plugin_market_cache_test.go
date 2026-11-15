package test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginmarket "MaiGo/internal/plugin_market"
)

func TestMarketCacheServesHitAndRemainingTTL(t *testing.T) {
	cache := pluginmarket.NewMarketCache(4)
	loads := 0
	loader := func() (any, error) {
		loads++
		return map[string]any{"name": "demo"}, nil
	}

	first, info, err := cache.Get("k", loader, time.Minute, false)
	if err != nil {
		t.Fatalf("first Get failed: %v", err)
	}
	if info.Hit || info.SharedRequest {
		t.Fatalf("first load should be a miss: %+v", info)
	}
	if first.(map[string]any)["name"] != "demo" {
		t.Fatalf("unexpected value: %#v", first)
	}

	second, info, err := cache.Get("k", loader, time.Minute, false)
	if err != nil {
		t.Fatalf("second Get failed: %v", err)
	}
	if !info.Hit {
		t.Fatalf("second Get should hit: %+v", info)
	}
	if loads != 1 {
		t.Fatalf("expected a single load, got %d", loads)
	}
	if info.TTLSeconds != 60 {
		t.Fatalf("unexpected ttl seconds: %d", info.TTLSeconds)
	}
	if info.RemainingSeconds > 60 || info.RemainingSeconds < 55 {
		t.Fatalf("unexpected remaining seconds: %d", info.RemainingSeconds)
	}
	if second.(map[string]any)["name"] != "demo" {
		t.Fatalf("unexpected cached value: %#v", second)
	}
}

func TestMarketCacheReturnsDeepCopy(t *testing.T) {
	cache := pluginmarket.NewMarketCache(4)
	value := map[string]any{"items": []any{"a"}}
	if _, _, err := cache.Get("k", func() (any, error) { return value, nil }, time.Minute, false); err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	got, _, err := cache.Get("k", func() (any, error) { return nil, nil }, time.Minute, false)
	if err != nil {
		t.Fatalf("second Get failed: %v", err)
	}
	got.(map[string]any)["items"].([]any)[0] = "mutated"

	again, _, err := cache.Get("k", func() (any, error) { return nil, nil }, time.Minute, false)
	if err != nil {
		t.Fatalf("third Get failed: %v", err)
	}
	if again.(map[string]any)["items"].([]any)[0] != "a" {
		t.Fatal("cached value was mutated through a returned copy")
	}
}

func TestMarketCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := pluginmarket.NewMarketCache(4)
	var loads int32
	release := make(chan struct{})
	loader := func() (any, error) {
		atomic.AddInt32(&loads, 1)
		<-release
		return "value", nil
	}

	const callers = 8
	var wait sync.WaitGroup
	var sharedCount int32
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			_, info, err := cache.Get("k", loader, time.Minute, false)
			if err != nil {
				return
			}
			if info.SharedRequest {
				atomic.AddInt32(&sharedCount, 1)
			}
		}()
	}
	// 让所有调用方都进入在途等待后再放行 loader
	time.Sleep(50 * time.Millisecond)
	close(release)
	wait.Wait()

	if got := atomic.LoadInt32(&loads); got != 1 {
		t.Fatalf("expected one coalesced load, got %d", got)
	}
	if got := atomic.LoadInt32(&sharedCount); got != callers-1 {
		t.Fatalf("expected %d shared requests, got %d", callers-1, got)
	}
}

func TestMarketCacheForceRefreshReloads(t *testing.T) {
	cache := pluginmarket.NewMarketCache(4)
	var loads int32
	loader := func() (any, error) {
		atomic.AddInt32(&loads, 1)
		return atomic.LoadInt32(&loads), nil
	}
	if _, _, err := cache.Get("k", loader, time.Minute, false); err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	value, info, err := cache.Get("k", loader, time.Minute, true)
	if err != nil {
		t.Fatalf("force refresh failed: %v", err)
	}
	if info.Hit {
		t.Fatalf("force refresh must not report a hit: %+v", info)
	}
	if value.(int32) != 2 {
		t.Fatalf("expected refreshed value 2, got %v", value)
	}
}

func TestMarketCacheLoaderErrorIsPropagated(t *testing.T) {
	cache := pluginmarket.NewMarketCache(4)
	boom := errors.New("boom")
	if _, _, err := cache.Get("k", func() (any, error) { return nil, boom }, time.Minute, false); !errors.Is(err, boom) {
		t.Fatalf("expected loader error, got %v", err)
	}
	// 失败不应留下缓存：下一次仍会重新加载
	value, info, err := cache.Get("k", func() (any, error) { return "ok", nil }, time.Minute, false)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if info.Hit || value != "ok" {
		t.Fatalf("unexpected retry result: value=%v info=%+v", value, info)
	}
}

func TestMarketCacheInvalidateDropsPrefix(t *testing.T) {
	cache := pluginmarket.NewMarketCache(4)
	loader := func() (any, error) { return "v", nil }
	if _, _, err := cache.Get("https://market/api/v2/a", loader, time.Minute, false); err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if _, _, err := cache.Get("https://other/api/v2/b", loader, time.Minute, false); err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	cache.Invalidate("https://market/api/v2/")

	if _, info, _ := cache.Get("https://market/api/v2/a", loader, time.Minute, false); info.Hit {
		t.Fatal("invalidated entry should be a miss")
	}
	if _, info, _ := cache.Get("https://other/api/v2/b", loader, time.Minute, false); !info.Hit {
		t.Fatal("unrelated entry should still hit")
	}
}

func TestMarketCacheEvictsWhenCapacityReached(t *testing.T) {
	cache := pluginmarket.NewMarketCache(2)
	loader := func() (any, error) { return "v", nil }
	for _, key := range []string{"a", "b", "c"} {
		if _, _, err := cache.Get(key, loader, time.Minute, false); err != nil {
			t.Fatalf("Get(%s) failed: %v", key, err)
		}
	}
	// 容量 2：最早的 a 应已被淘汰
	if _, info, _ := cache.Get("a", loader, time.Minute, false); info.Hit {
		t.Fatal("expected a to be evicted")
	}
}
