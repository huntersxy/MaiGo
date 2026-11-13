package pluginmarket

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

// CacheInfo 对齐 market_cache.get 返回的缓存元数据
type CacheInfo struct {
	Hit              bool `json:"hit"`
	SharedRequest    bool `json:"shared_request"`
	TTLSeconds       int  `json:"ttl_seconds"`
	RemainingSeconds int  `json:"remaining_seconds"`
}

type marketCacheEntry struct {
	key      string
	storedAt time.Time
	value    any
}

type marketCacheCall struct {
	done     chan struct{}
	storedAt time.Time
	value    any
	err      error
}

// MarketCache 对齐 plugin_market.cache.MarketCache：TTL 缓存并合并同一资源的并发请求
type MarketCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List
	pending    map[string]*marketCacheCall
	maxEntries int
	now        func() time.Time
}

// NewMarketCache 构造缓存，maxEntries 对齐 Python 默认 256
func NewMarketCache(maxEntries int) *MarketCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	return &MarketCache{
		entries:    make(map[string]*list.Element, maxEntries),
		order:      list.New(),
		pending:    make(map[string]*marketCacheCall),
		maxEntries: maxEntries,
		now:        time.Now,
	}
}

// Get 对齐 MarketCache.get：命中返回副本，未命中由唯一 owner 执行 loader
func (c *MarketCache) Get(key string, loader func() (any, error), ttl time.Duration, forceRefresh bool) (any, CacheInfo, error) {
	c.mu.Lock()
	var entry *marketCacheEntry
	if element, ok := c.entries[key]; ok {
		entry = element.Value.(*marketCacheEntry)
	}
	hit := !forceRefresh && entry != nil && c.now().Sub(entry.storedAt) < ttl
	if hit {
		c.order.MoveToFront(c.entries[key])
	}
	call, pendingExists := c.pending[key]
	owner := !hit && !pendingExists
	if owner {
		call = &marketCacheCall{done: make(chan struct{})}
		c.pending[key] = call
	}
	c.mu.Unlock()

	if !hit {
		if owner {
			value, err := loader()
			if err != nil {
				c.mu.Lock()
				if c.pending[key] == call {
					delete(c.pending, key)
				}
				call.err = err
				c.mu.Unlock()
				close(call.done)
				return nil, CacheInfo{}, err
			}
			storedAt := c.now()
			c.mu.Lock()
			if c.pending[key] == call {
				c.storeLocked(key, storedAt, value)
			}
			call.storedAt = storedAt
			call.value = value
			delete(c.pending, key)
			c.mu.Unlock()
			close(call.done)
			entry = &marketCacheEntry{key: key, storedAt: storedAt, value: value}
		} else {
			<-call.done
			if call.err != nil {
				return nil, CacheInfo{}, call.err
			}
			entry = &marketCacheEntry{key: key, storedAt: call.storedAt, value: call.value}
		}
	}

	remaining := ttl - c.now().Sub(entry.storedAt)
	if remaining < 0 {
		remaining = 0
	}
	info := CacheInfo{
		Hit:              hit,
		SharedRequest:    !hit && !owner,
		TTLSeconds:       int(ttl / time.Second),
		RemainingSeconds: int(remaining / time.Second),
	}
	return deepCopyJSON(entry.value), info, nil
}

func (c *MarketCache) storeLocked(key string, storedAt time.Time, value any) {
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*marketCacheEntry)
		entry.storedAt = storedAt
		entry.value = value
		c.order.MoveToFront(element)
	} else {
		element = c.order.PushFront(&marketCacheEntry{key: key, storedAt: storedAt, value: value})
		c.entries[key] = element
	}
	for c.order.Len() > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*marketCacheEntry).key)
	}
}

// Invalidate 对齐 MarketCache.invalidate：删除前缀匹配的缓存与在途请求
func (c *MarketCache) Invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, element := range c.entries {
		if strings.HasPrefix(key, prefix) {
			c.order.Remove(element)
			delete(c.entries, key)
		}
	}
	for key := range c.pending {
		if strings.HasPrefix(key, prefix) {
			delete(c.pending, key)
		}
	}
}
