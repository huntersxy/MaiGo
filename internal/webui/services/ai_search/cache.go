package search

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const (
	DefaultCacheTTL        = 300 * time.Second
	DefaultCacheMaxEntries = 128
)

type cacheEntry struct {
	key       string
	expiresAt time.Time
	response  Response
}

// Cache 有界 TTL/LRU 缓存，行为对齐 Python _AI_SEARCH_CACHE
type Cache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]*list.Element
	order      *list.List
	now        func() time.Time
}

// NewCache 构造缓存，参数非法时回落默认值
func NewCache(ttl time.Duration, maxEntries int) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = DefaultCacheMaxEntries
	}
	return &Cache{
		ttl: ttl, maxEntries: maxEntries,
		entries: make(map[string]*list.Element), order: list.New(),
		now: time.Now,
	}
}

// Get 读取未过期缓存并提升 LRU 顺序，返回 Cached=true 的副本
func (c *Cache) Get(key string) (Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return Response{}, false
	}
	entry := element.Value.(*cacheEntry)
	if !entry.expiresAt.After(c.now()) {
		c.order.Remove(element)
		delete(c.entries, key)
		return Response{}, false
	}
	c.order.MoveToBack(element)
	cloned := cloneResponse(entry.response)
	cloned.Cached = true
	return cloned, true
}

// Set 写入缓存并按 LRU 淘汰
func (c *Cache) Set(key string, response Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*cacheEntry)
		entry.expiresAt = c.now().Add(c.ttl)
		entry.response = cloneResponse(response)
		c.order.MoveToBack(element)
		return
	}
	entry := &cacheEntry{key: key, expiresAt: c.now().Add(c.ttl), response: cloneResponse(response)}
	c.entries[key] = c.order.PushBack(entry)
	for c.order.Len() > c.maxEntries {
		front := c.order.Front()
		if front == nil {
			break
		}
		c.order.Remove(front)
		delete(c.entries, front.Value.(*cacheEntry).key)
	}
}

// Key 缓存键同时包含问题、语言和候选目录
func Key(request Request) string {
	type candidatePayload struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Document    string `json:"document"`
	}
	payload := struct {
		Query      string             `json:"query"`
		Language   string             `json:"language"`
		Candidates []candidatePayload `json:"candidates"`
	}{Query: trimQuery(request.Query), Language: NormalizeLanguage(request.Language)}
	for _, candidate := range request.Candidates {
		payload.Candidates = append(payload.Candidates, candidatePayload{
			ID: candidate.ID, Title: candidate.Title, Description: candidate.Description,
			Category: candidate.Category, Document: candidate.Document,
		})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func cloneResponse(response Response) Response {
	cloned := response
	cloned.Suggestions = append([]string(nil), response.Suggestions...)
	cloned.Sources = append([]Source(nil), response.Sources...)
	cloned.ExpandedTerms = append([]string(nil), response.ExpandedTerms...)
	cloned.Results = append([]ModelResult(nil), response.Results...)
	return cloned
}
