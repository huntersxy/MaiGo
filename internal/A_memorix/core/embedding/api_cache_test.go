package embedding

// embedding 缓存配置与模型身份的局部回归测试

import (
	"context"
	"sync"
	"testing"

	"MaiGo/internal/config/maibot"
)

type staticEmbeddingConfig struct {
	file *maibot.ModelConfigFile
}

func (s staticEmbeddingConfig) ModelConfigFile() *maibot.ModelConfigFile { return s.file }

type countingEmbeddingTransport struct {
	mu     sync.Mutex
	calls  int
	vector []float32
}

func (t *countingEmbeddingTransport) GetEmbedding(context.Context, EmbeddingRequest) ([]float32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	return append([]float32{}, t.vector...), nil
}

func (t *countingEmbeddingTransport) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func newTestEmbeddingAdapter(t *testing.T, transport EmbeddingTransport, enableCache bool) *EmbeddingAPIAdapter {
	t.Helper()
	file := &maibot.ModelConfigFile{
		Models: []maibot.ModelInfo{
			{Name: "m1", ModelIdentifier: "m1-ident", ApiProvider: "p1"},
			{Name: "m2", ModelIdentifier: "m2-ident", ApiProvider: "p1"},
		},
		ApiProviders: []maibot.APIProvider{{Name: "p1", BaseUrl: "http://example.invalid"}},
		ModelTaskConfig: maibot.ModelTaskConfig{
			Embedding: maibot.TaskConfig{ModelList: []string{"m1"}},
		},
	}
	return NewEmbeddingAPIAdapter(&EmbeddingAPIAdapterOptions{
		DefaultDimension:     4,
		EnableCache:          enableCache,
		DimensionRequestMode: "explicit",
		ConfigSource:         staticEmbeddingConfig{file: file},
		Transport:            transport,
	})
}

func TestTextEmbeddingCacheHonorsUseCache(t *testing.T) {
	resetGlobalEmbeddingCaches()
	t.Cleanup(resetGlobalEmbeddingCaches)

	transport := &countingEmbeddingTransport{vector: []float32{1, 2, 3, 4}}
	adapter := newTestEmbeddingAdapter(t, transport, true)
	dimension := 4
	options := &EncodeOptions{Dimensions: &dimension}

	if _, err := adapter.EncodeOne(context.Background(), "hello", options); err != nil {
		t.Fatalf("首次编码失败: %v", err)
	}
	if _, err := adapter.EncodeOne(context.Background(), "hello", options); err != nil {
		t.Fatalf("缓存编码失败: %v", err)
	}
	if calls := transport.Calls(); calls != 1 {
		t.Fatalf("缓存命中不应重复请求: calls=%d", calls)
	}

	useCache := false
	if _, err := adapter.EncodeOne(context.Background(), "hello", &EncodeOptions{
		Dimensions: &dimension, UseCache: &useCache,
	}); err != nil {
		t.Fatalf("绕过缓存编码失败: %v", err)
	}
	if calls := transport.Calls(); calls != 2 {
		t.Fatalf("use_cache=false 应请求真实服务: calls=%d", calls)
	}
}

func TestEmbeddingCacheKeyIsolatesActualModel(t *testing.T) {
	resetGlobalEmbeddingCaches()
	t.Cleanup(resetGlobalEmbeddingCaches)

	adapter := newTestEmbeddingAdapter(t, &countingEmbeddingTransport{vector: []float32{1, 2, 3, 4}}, true)
	dimension := 4
	first, err := adapter.embeddingCacheKey("hello", &dimension, "m1", true)
	if err != nil {
		t.Fatalf("m1 缓存键失败: %v", err)
	}
	second, err := adapter.embeddingCacheKey("hello", &dimension, "m2", true)
	if err != nil {
		t.Fatalf("m2 缓存键失败: %v", err)
	}
	if first.identity == second.identity {
		t.Fatal("不同模型必须使用不同缓存身份")
	}
	if first.dimension != dimension || first.text != "hello" {
		t.Fatalf("缓存键字段不符: %+v", first)
	}
}

func TestTextEmbeddingCacheEvictsOldest(t *testing.T) {
	resetGlobalEmbeddingCaches()
	t.Cleanup(resetGlobalEmbeddingCaches)

	for index := 0; index < textEmbeddingCacheMaxEntries+1; index++ {
		storeGlobalTextEmbedding(newEmbeddingTextCacheKey("identity", index, "text"), []float32{float32(index)})
	}
	globalTextEmbeddingCacheMu.Lock()
	size := len(globalTextEmbeddingCache)
	globalTextEmbeddingCacheMu.Unlock()
	if size != textEmbeddingCacheMaxEntries {
		t.Fatalf("缓存容量应限制为 %d，实际 %d", textEmbeddingCacheMaxEntries, size)
	}
	if _, ok := lookupGlobalTextEmbedding(newEmbeddingTextCacheKey("identity", 0, "text")); ok {
		t.Fatal("最旧条目应被淘汰")
	}
	if _, ok := lookupGlobalTextEmbedding(newEmbeddingTextCacheKey("identity", textEmbeddingCacheMaxEntries, "text")); !ok {
		t.Fatal("最新条目应被保留")
	}
}
