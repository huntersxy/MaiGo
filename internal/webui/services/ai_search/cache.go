package search

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

const (
	DefaultCacheTTL        = 300 * time.Second
	DefaultCacheMaxEntries = 128
)

// Cache 有界 TTL/LRU 缓存，行为对齐 Python _AI_SEARCH_CACHE。
//
// Python 命中时只把键移到末尾，不延长 expires_at；ttlcache 默认在 Get 时续期，
// 这里关闭 touch-on-hit 保持「过期时间从写入时刻计算」。
// 过期项由 Get 懒删除，容量满时按 LRU 淘汰，不需要后台清理 goroutine。
type Cache struct {
	items *ttlcache.Cache[string, Response]
}

// NewCache 构造缓存，参数非法时回落默认值
func NewCache(ttl time.Duration, maxEntries int) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = DefaultCacheMaxEntries
	}
	return &Cache{items: ttlcache.New(
		ttlcache.WithTTL[string, Response](ttl),
		ttlcache.WithCapacity[string, Response](uint64(maxEntries)),
		ttlcache.WithDisableTouchOnHit[string, Response](),
	)}
}

// Get 读取未过期缓存并提升 LRU 顺序，返回 Cached=true 的副本
func (c *Cache) Get(key string) (Response, bool) {
	if c == nil || c.items == nil {
		return Response{}, false
	}
	item := c.items.Get(key)
	if item == nil {
		return Response{}, false
	}
	cloned := cloneResponse(item.Value())
	cloned.Cached = true
	return cloned, true
}

// Set 写入缓存并按 LRU 淘汰
func (c *Cache) Set(key string, response Response) {
	if c == nil || c.items == nil {
		return
	}
	c.items.Set(key, cloneResponse(response), ttlcache.DefaultTTL)
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
	cloned.Plugins = append([]PluginInfo(nil), response.Plugins...)
	cloned.InstallRequests = append([]InstallRequest(nil), response.InstallRequests...)
	cloned.ConfigUpdates = append([]ConfigUpdate(nil), response.ConfigUpdates...)
	cloned.Artifacts = append([]HelpArtifact(nil), response.Artifacts...)
	return cloned
}
