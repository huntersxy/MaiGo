package embedding

// 配置指纹与进程级缓存，对齐 api_adapter.py 的指纹与全局缓存逻辑

import (
	"container/list"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"MaiGo/internal/llm_models"
)

// textEmbeddingCacheMaxEntries 文本向量缓存上限，对齐 TEXT_EMBEDDING_CACHE_MAX_ENTRIES
const textEmbeddingCacheMaxEntries = 2048

// dimensionCacheEntry 进程级维度缓存条目，对应 (dimension, model_name, provider_name)
type dimensionCacheEntry struct {
	dimension    int
	modelName    string
	providerName string
}

var (
	globalDimensionCacheMu sync.Mutex
	globalDimensionCache   = map[string]dimensionCacheEntry{}

	globalTextEmbeddingCacheMu sync.Mutex
	globalTextEmbeddingCache   = map[embeddingTextCacheKey]*list.Element{}
	globalTextEmbeddingOrder   = list.New()
)

// embeddingTextCacheKey 文本向量缓存键，对应 (模型身份, 维度, 文本)
type embeddingTextCacheKey struct {
	identity  string
	dimension int
	text      string
}

// embeddingTextCacheEntry LRU 链表中的缓存值
type embeddingTextCacheEntry struct {
	key    embeddingTextCacheKey
	vector []float32
}

// EmbeddingFingerprintOptions 指纹选项，对应 get_embedding_fingerprint 的关键字参数
// ModelName 为 nil 表示使用最近一次成功观测到的模型
type EmbeddingFingerprintOptions struct {
	Dimension *int
	ModelName *string
}

// fingerprintHash 计算指纹哈希，对齐 _fingerprint_hash
// canonical JSON 与 sha256 工具收敛到 llm，避免 memorix 内重复实现
func fingerprintHash(payload map[string]any) (string, error) {
	return llm.CanonicalFingerprintHash(payload)
}

// dimensionCacheKey 计算维度缓存键，对齐 _dimension_cache_key
func (a *EmbeddingAPIAdapter) dimensionCacheKey() (string, error) {
	file, err := a.modelConfigFile()
	if err != nil {
		return "", err
	}
	candidates := a.resolveCandidateModelNames(file)
	models := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		info, err := a.findModelInfo(file, candidate)
		if err != nil {
			return "", err
		}
		provider, err := a.findProvider(file, info.ApiProvider)
		if err != nil {
			return "", err
		}
		models = append(models, []any{
			candidate,
			info.ModelIdentifier,
			strings.TrimRight(provider.BaseUrl, "/"),
			stripDimensionControlKeys(info.ExtraParams),
		})
	}
	payload := map[string]any{
		"models":    models,
		"dimension": a.defaultDimension,
		"mode":      a.dimensionRequestMode,
	}
	return fingerprintHash(payload)
}

// newEmbeddingTextCacheKey 构造文本向量缓存键，对齐 _embedding_cache_key
func newEmbeddingTextCacheKey(identity string, dimension int, text string) embeddingTextCacheKey {
	return embeddingTextCacheKey{identity: identity, dimension: dimension, text: text}
}

// embeddingCacheKey 按实际模型、请求维度语义和正文隔离缓存
func (a *EmbeddingAPIAdapter) embeddingCacheKey(
	text string,
	dimensions *int,
	modelName string,
	includeDimension bool,
) (embeddingTextCacheKey, error) {
	requestedDimension := a.resolveCanonicalDimension(dimensions)
	fingerprint, err := a.GetEmbeddingFingerprint(&EmbeddingFingerprintOptions{
		Dimension: &requestedDimension,
		ModelName: &modelName,
	})
	if err != nil {
		return embeddingTextCacheKey{}, err
	}
	identity, err := fingerprintHash(map[string]any{
		"model":             fingerprint["hash"],
		"include_dimension": a.shouldIncludeDimension(dimensions, includeDimension),
	})
	if err != nil {
		return embeddingTextCacheKey{}, err
	}
	return newEmbeddingTextCacheKey(identity, requestedDimension, text), nil
}

// lookupGlobalTextEmbedding 查询进程级文本向量缓存，命中时刷新最近使用顺序
func lookupGlobalTextEmbedding(key embeddingTextCacheKey) ([]float32, bool) {
	globalTextEmbeddingCacheMu.Lock()
	defer globalTextEmbeddingCacheMu.Unlock()
	element, ok := globalTextEmbeddingCache[key]
	if !ok {
		return nil, false
	}
	globalTextEmbeddingOrder.MoveToBack(element)
	entry, _ := element.Value.(embeddingTextCacheEntry)
	return cloneVector(entry.vector), true
}

// storeGlobalTextEmbedding 写入进程级文本向量缓存，按最近使用顺序淘汰
func storeGlobalTextEmbedding(key embeddingTextCacheKey, vector []float32) {
	globalTextEmbeddingCacheMu.Lock()
	defer globalTextEmbeddingCacheMu.Unlock()
	if element, ok := globalTextEmbeddingCache[key]; ok {
		globalTextEmbeddingOrder.Remove(element)
		delete(globalTextEmbeddingCache, key)
	}
	element := globalTextEmbeddingOrder.PushBack(embeddingTextCacheEntry{key: key, vector: cloneVector(vector)})
	globalTextEmbeddingCache[key] = element
	for len(globalTextEmbeddingCache) > textEmbeddingCacheMaxEntries {
		oldest := globalTextEmbeddingOrder.Front()
		if oldest == nil {
			break
		}
		entry, _ := oldest.Value.(embeddingTextCacheEntry)
		globalTextEmbeddingOrder.Remove(oldest)
		delete(globalTextEmbeddingCache, entry.key)
	}
}

// getGlobalDimensionCache 查询进程级维度缓存
func getGlobalDimensionCache(key string) (dimensionCacheEntry, bool) {
	globalDimensionCacheMu.Lock()
	defer globalDimensionCacheMu.Unlock()
	entry, ok := globalDimensionCache[key]
	return entry, ok
}

// storeGlobalDimensionCache 写入进程级维度缓存
func storeGlobalDimensionCache(key string, entry dimensionCacheEntry) {
	globalDimensionCacheMu.Lock()
	defer globalDimensionCacheMu.Unlock()
	globalDimensionCache[key] = entry
}

// resetGlobalEmbeddingCaches 清空进程级缓存，仅供测试隔离使用
func resetGlobalEmbeddingCaches() {
	globalDimensionCacheMu.Lock()
	globalDimensionCache = map[string]dimensionCacheEntry{}
	globalDimensionCacheMu.Unlock()
	globalTextEmbeddingCacheMu.Lock()
	globalTextEmbeddingCache = map[embeddingTextCacheKey]*list.Element{}
	globalTextEmbeddingOrder = list.New()
	globalTextEmbeddingCacheMu.Unlock()
}

// GetEmbeddingFingerprint 计算 embedding 配置指纹，对齐 get_embedding_fingerprint
func (a *EmbeddingAPIAdapter) GetEmbeddingFingerprint(opt *EmbeddingFingerprintOptions) (map[string]any, error) {
	if opt == nil {
		opt = &EmbeddingFingerprintOptions{}
	}
	file, err := a.modelConfigFile()
	if err != nil {
		return nil, err
	}
	candidates := a.resolveCandidateModelNames(file)
	candidateNames := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			candidateNames = append(candidateNames, trimmed)
		}
	}

	a.stateMu.Lock()
	lastModel := a.lastModel
	lastProvider := a.lastProvider
	lastDimension := a.lastDimension
	a.stateMu.Unlock()

	effectiveDimension := 0
	if opt.Dimension != nil {
		effectiveDimension = *opt.Dimension
	}
	if effectiveDimension == 0 {
		effectiveDimension = lastDimension
	}
	if effectiveDimension == 0 {
		effectiveDimension = a.GetEmbeddingDimension()
	}
	if effectiveDimension < 1 {
		effectiveDimension = 1
	}

	var modelToken string
	providerToken := ""
	observed := false
	if opt.ModelName != nil {
		modelToken = strings.TrimSpace(*opt.ModelName)
		providerToken = a.resolveModelProviderName(file, modelToken)
	} else {
		modelToken = strings.TrimSpace(lastModel)
		providerToken = strings.TrimSpace(lastProvider)
		if modelToken != "" {
			observed = true
		}
	}
	source := "configured"
	if observed {
		source = "observed"
	}

	if modelToken == "" {
		modelToken = strings.TrimSpace(a.modelName)
		if modelToken == "" {
			modelToken = defaultEmbeddingModelName
		}
		if modelToken == defaultEmbeddingModelName && len(candidateNames) > 0 {
			modelToken = candidateNames[0]
		}
		if modelToken == defaultEmbeddingModelName {
			providerToken = ""
		} else {
			providerToken = a.resolveModelProviderName(file, modelToken)
		}
	}

	payload := map[string]any{
		"model":                  modelToken,
		"provider":               providerToken,
		"dimension":              effectiveDimension,
		"dimension_request_mode": a.dimensionRequestMode,
	}
	if modelToken == defaultEmbeddingModelName {
		payload["candidate_models"] = candidateNames
	} else {
		info, err := a.findModelInfo(file, modelToken)
		if err != nil {
			return nil, err
		}
		provider, err := a.findProvider(file, info.ApiProvider)
		if err != nil {
			return nil, err
		}
		payload["model_identifier"] = info.ModelIdentifier
		payload["base_url"] = strings.TrimRight(provider.BaseUrl, "/")
		payload["extra_params"] = stripDimensionControlKeys(info.ExtraParams)
	}

	hashValue, err := fingerprintHash(payload)
	if err != nil {
		return nil, err
	}
	modelIdentifier, _ := payload["model_identifier"].(string)
	baseURL, _ := payload["base_url"].(string)
	return map[string]any{
		"version":                2,
		"hash":                   hashValue,
		"model":                  modelToken,
		"provider":               providerToken,
		"dimension":              effectiveDimension,
		"dimension_request_mode": a.dimensionRequestMode,
		"source":                 source,
		"model_identifier":       modelIdentifier,
		"base_url":               baseURL,
	}, nil
}

// GetEmbeddingConfigurationKey 返回当前 embedding 配置指纹，对齐 get_embedding_configuration_key
func (a *EmbeddingAPIAdapter) GetEmbeddingConfigurationKey() (string, error) {
	file, err := a.modelConfigFile()
	if err != nil {
		return "", err
	}
	candidates := a.resolveCandidateModelNames(file)
	primary := defaultEmbeddingModelName
	if len(candidates) > 0 {
		primary = candidates[0]
	}
	a.stateMu.Lock()
	lastModel := a.lastModel
	lastPrimary := a.lastPrimary
	a.stateMu.Unlock()
	model := lastModel
	if !slices.Contains(candidates, model) || primary != lastPrimary {
		model = primary
	}
	fingerprint, err := a.GetEmbeddingFingerprint(&EmbeddingFingerprintOptions{ModelName: &model})
	if err != nil {
		return "", err
	}
	hashValue, ok := fingerprint["hash"].(string)
	if !ok {
		return "", errors.New("embedding 配置指纹缺少 hash")
	}
	return hashValue, nil
}

// describeFingerprint 便于日志输出指纹摘要
func describeFingerprint(fingerprint map[string]any) string {
	return fmt.Sprintf(
		"EmbeddingFingerprint(model=%v, provider=%v, dimension=%v, source=%v)",
		fingerprint["model"],
		fingerprint["provider"],
		fingerprint["dimension"],
		fingerprint["source"],
	)
}
