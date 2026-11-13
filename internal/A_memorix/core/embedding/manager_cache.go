package embedding

// 嵌入缓存持久化，对齐 manager.py 的 save_cache/load_cache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultEmbeddingCacheName = "embeddings_cache.npz"
	legacyPickleCacheName     = "embeddings_cache.pkl"
)

// SaveCache 保存内存缓存到 npz 文件，cachePath 为空时使用缓存目录默认文件名
func (m *EmbeddingManager) SaveCache(cachePath string) error {
	path, err := m.resolveCachePath(cachePath)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(path), ".pkl") {
		return fmt.Errorf("embedding cache 不再支持 pickle 格式")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m.cacheMu.Lock()
	entries := make(map[string][]float32, len(m.embeddingCache))
	for key, vector := range m.embeddingCache {
		entries[key] = cloneVector(vector)
	}
	m.cacheMu.Unlock()

	tempPath := path + ".tmp"
	if err := writeNPZCompressed(tempPath, entries); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	managerLogger.Info(fmt.Sprintf("缓存已保存: %s (%d 条)", path, len(entries)))
	return nil
}

// LoadCache 从 npz 文件加载缓存，对齐 load_cache 对旧 pickle 文件的兼容处理
func (m *EmbeddingManager) LoadCache(cachePath string) error {
	path, err := m.resolveCachePath(cachePath)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(path), ".pkl") {
		managerLogger.Warn(fmt.Sprintf("忽略旧 pickle embedding 缓存: %s", path))
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		legacyPath := filepath.Join(filepath.Dir(path), legacyPickleCacheName)
		if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
			managerLogger.Info(fmt.Sprintf("检测到旧 pickle embedding 缓存，将忽略并按需重建: %s", legacyPath))
		}
		managerLogger.Warn(fmt.Sprintf("缓存文件不存在: %s", path))
		return nil
	}
	entries, err := readNPZCompressed(path)
	if err != nil {
		return err
	}
	m.cacheMu.Lock()
	m.embeddingCache = entries
	m.cacheMu.Unlock()
	managerLogger.Info(fmt.Sprintf("缓存已加载: %s (%d 条)", path, len(entries)))
	return nil
}

// resolveCachePath 解析缓存路径，空路径回落到缓存目录下的默认文件
func (m *EmbeddingManager) resolveCachePath(cachePath string) (string, error) {
	if cachePath != "" {
		return cachePath, nil
	}
	if m.cacheDir == "" {
		return "", fmt.Errorf("未指定缓存目录")
	}
	return filepath.Join(m.cacheDir, defaultEmbeddingCacheName), nil
}
