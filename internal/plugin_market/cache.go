package pluginmarket

import (
	"strings"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"
)

// CacheInfo 对齐 market_cache.get 返回的缓存元数据
type CacheInfo struct {
	Hit              bool `json:"hit"`
	SharedRequest    bool `json:"shared_request"`
	TTLSeconds       int  `json:"ttl_seconds"`
	RemainingSeconds int  `json:"remaining_seconds"`
}

// marketCacheEntry 缓存值携带写入时刻。TTL 由调用方按请求传入，
// 因此条目以 ttlcache.NoTTL 保存、过期判断在 Get 内完成，
// 与 Python 一样允许同一份缓存上并存不同 TTL 的条目。
type marketCacheEntry struct {
	storedAt time.Time
	value    any
}

// marketCacheFlight singleflight 的返回值，带上写入时刻供调用方计算剩余时间
type marketCacheFlight struct {
	storedAt time.Time
	value    any
	err      error
}

// MarketCache 对齐 plugin_market.cache.MarketCache：TTL 缓存并合并同一资源的并发请求。
//
// ttlcache 负责容量淘汰与命中时的 LRU 提升（关闭 touch-on-hit，
// TTL 不随读取续期），singleflight 负责合并同一 key 的在途加载。
type MarketCache struct {
	items *ttlcache.Cache[string, marketCacheEntry]
	group singleflight.Group

	// mu 保护 inflight 与 gen
	mu       sync.Mutex
	inflight map[string]struct{}
	gen      map[string]uint64

	now func() time.Time
}

// NewMarketCache 构造缓存，maxEntries 对齐 Python 默认 256
func NewMarketCache(maxEntries int) *MarketCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	return &MarketCache{
		items: ttlcache.New(
			ttlcache.WithCapacity[string, marketCacheEntry](uint64(maxEntries)),
			ttlcache.WithDisableTouchOnHit[string, marketCacheEntry](),
		),
		inflight: map[string]struct{}{},
		gen:      map[string]uint64{},
		now:      time.Now,
	}
}

// Get 对齐 MarketCache.get：命中返回副本，未命中由唯一 owner 执行 loader
func (c *MarketCache) Get(key string, loader func() (any, error), ttl time.Duration, forceRefresh bool) (any, CacheInfo, error) {
	if !forceRefresh {
		if entry, ok := c.load(key, ttl); ok {
			return deepCopyJSON(entry.value), c.info(ttl, entry.storedAt, true, false), nil
		}
	}

	owner := c.markInflight(key)
	defer c.clearInflight(key, owner)

	result, err, _ := c.group.Do(key, func() (any, error) {
		// 双检：拿到 singleflight 之前可能已有其他 goroutine 写入缓存。
		if !forceRefresh {
			if entry, ok := c.load(key, ttl); ok {
				return marketCacheFlight{storedAt: entry.storedAt, value: entry.value}, nil
			}
		}
		// 记录代数：loader 执行期间发生 Invalidate 时不能回填失效缓存。
		generation := c.generation(key)
		value, loadErr := loader()
		if loadErr != nil {
			return marketCacheFlight{err: loadErr}, nil
		}
		// TTL 从拉取完成时计算，慢请求不消耗缓存有效期。
		storedAt := c.now()
		c.store(key, generation, storedAt, value)
		return marketCacheFlight{storedAt: storedAt, value: value}, nil
	})
	if err != nil {
		return nil, CacheInfo{}, err
	}
	flight, ok := result.(marketCacheFlight)
	if !ok {
		return nil, CacheInfo{}, errMarketCacheResult
	}
	if flight.err != nil {
		return nil, CacheInfo{}, flight.err
	}
	return deepCopyJSON(flight.value), c.info(ttl, flight.storedAt, false, !owner), nil
}

// markInflight 记录本次调用是否为该 key 的在途 owner
func (c *MarketCache) markInflight(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.inflight[key]; exists {
		return false
	}
	c.inflight[key] = struct{}{}
	return true
}

func (c *MarketCache) clearInflight(key string, owner bool) {
	if !owner {
		return
	}
	c.mu.Lock()
	delete(c.inflight, key)
	// 代数只在在途期间有意义；owner 结束时清理，避免 map 随缓存键无限增长。
	delete(c.gen, key)
	c.mu.Unlock()
}

func (c *MarketCache) generation(key string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[key]
}

// load 读取仍在 TTL 内的条目
func (c *MarketCache) load(key string, ttl time.Duration) (marketCacheEntry, bool) {
	item := c.items.Get(key)
	if item == nil {
		return marketCacheEntry{}, false
	}
	entry := item.Value()
	if c.now().Sub(entry.storedAt) >= ttl {
		return marketCacheEntry{}, false
	}
	return entry, true
}

// store 在代数未变时写入；Invalidate 已把代数推进则丢弃这次结果。
// 检查与写入在同一把锁内完成，避免 Invalidate 与回填交错。
func (c *MarketCache) store(key string, generation uint64, storedAt time.Time, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen[key] != generation {
		return
	}
	c.items.Set(key, marketCacheEntry{storedAt: storedAt, value: value}, ttlcache.NoTTL)
}

func (c *MarketCache) info(ttl time.Duration, storedAt time.Time, hit, shared bool) CacheInfo {
	remaining := ttl - c.now().Sub(storedAt)
	if remaining < 0 {
		remaining = 0
	}
	return CacheInfo{
		Hit:              hit,
		SharedRequest:    shared,
		TTLSeconds:       int(ttl / time.Second),
		RemainingSeconds: int(remaining / time.Second),
	}
}

// Invalidate 对齐 MarketCache.invalidate：删除前缀匹配的缓存。
//
// 与 Python 一致，已开始的在途加载不会被取消，但它的结果会因代数推进被丢弃，
// 不会回填失效缓存。
func (c *MarketCache) Invalidate(prefix string) {
	c.mu.Lock()
	for key := range c.inflight {
		if strings.HasPrefix(key, prefix) {
			c.gen[key]++
		}
	}
	c.mu.Unlock()
	for _, key := range c.items.Keys() {
		if strings.HasPrefix(key, prefix) {
			c.items.Delete(key)
		}
	}
}
