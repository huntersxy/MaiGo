package imagememory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// ImageMemoryRuntimeConfig 对齐 Python ImageMemoryRuntime.__init__ 的关键字参数
type ImageMemoryRuntimeConfig struct {
	MetadataStore      *storage.Store
	AssetStore         *ImageAssetStore
	Embedder           ImageEmbedder
	VectorRoot         string
	Config             map[string]any
	PersistVectorStore func(store *storage.VectorStore, fingerprint map[string]any) error
	Logger             utils.Logger
}

// ImageMemoryRuntime 对齐 Python ImageMemoryRuntime
type ImageMemoryRuntime struct {
	metadataStore      *storage.Store
	assetStore         *ImageAssetStore
	embedder           ImageEmbedder
	vectorRoot         string
	config             map[string]any
	persistVectorStore func(store *storage.VectorStore, fingerprint map[string]any) error
	logger             utils.Logger

	stateMu       sync.RWMutex
	vectorStore   *storage.VectorStore
	fingerprint   map[string]any
	generation    int64
	statusValue   string
	statusMessage string
	recovery      map[string]any

	initializationMu sync.Mutex
	publicationMu    sync.Mutex
}

// runtimeSnapshot 是运行时状态的只读快照
type runtimeSnapshot struct {
	status      string
	message     string
	fingerprint map[string]any
	generation  int64
	vectorStore *storage.VectorStore
	recovery    map[string]any
}

// NewImageMemoryRuntime 构造图片记忆运行时
func NewImageMemoryRuntime(config ImageMemoryRuntimeConfig) (*ImageMemoryRuntime, error) {
	if config.MetadataStore == nil {
		return nil, validationErrorf("图片运行时缺少元数据存储")
	}
	if config.AssetStore == nil {
		return nil, validationErrorf("图片运行时缺少资产存储")
	}
	if config.Embedder == nil {
		return nil, validationErrorf("图片运行时缺少图片嵌入器")
	}
	if config.PersistVectorStore == nil {
		return nil, validationErrorf("图片运行时缺少向量持久化回调")
	}
	vectorRoot := config.VectorRoot
	if strings.TrimSpace(vectorRoot) == "" {
		vectorRoot = "."
	}
	settings := make(map[string]any, len(config.Config))
	for key, value := range config.Config {
		settings[key] = value
	}
	return &ImageMemoryRuntime{
		metadataStore:      config.MetadataStore,
		assetStore:         config.AssetStore,
		embedder:           config.Embedder,
		vectorRoot:         vectorRoot,
		config:             settings,
		persistVectorStore: config.PersistVectorStore,
		logger:             utils.OrLogger(config.Logger),
		fingerprint:        map[string]any{},
		statusValue:        "pending",
		statusMessage:      "等待真实图片嵌入探测",
		recovery:           map[string]any{"removed_files": 0, "issues": []any{}},
	}, nil
}

// MetadataStore 返回元数据存储
func (r *ImageMemoryRuntime) MetadataStore() *storage.Store { return r.metadataStore }

// AssetStore 返回图片资产存储
func (r *ImageMemoryRuntime) AssetStore() *ImageAssetStore { return r.assetStore }

// Embedder 返回图片嵌入器
func (r *ImageMemoryRuntime) Embedder() ImageEmbedder { return r.embedder }

// VectorRoot 返回图片向量根目录
func (r *ImageMemoryRuntime) VectorRoot() string { return r.vectorRoot }

// VectorStore 返回当前图片向量库
func (r *ImageMemoryRuntime) VectorStore() *storage.VectorStore {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.vectorStore
}

// Fingerprint 返回当前图片嵌入指纹副本
func (r *ImageMemoryRuntime) Fingerprint() map[string]any {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return fingerprintCopy(r.fingerprint)
}

// Generation 返回当前索引代数
func (r *ImageMemoryRuntime) Generation() int64 {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.generation
}

// Recovery 返回上次资产核对结果副本
func (r *ImageMemoryRuntime) Recovery() map[string]any {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return copyAnyMap(r.recovery)
}

// snapshot 读取运行时状态快照
func (r *ImageMemoryRuntime) snapshot() runtimeSnapshot {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return runtimeSnapshot{
		status:      r.statusValue,
		message:     r.statusMessage,
		fingerprint: fingerprintCopy(r.fingerprint),
		generation:  r.generation,
		vectorStore: r.vectorStore,
		recovery:    copyAnyMap(r.recovery),
	}
}

// setStatus 更新状态与说明
func (r *ImageMemoryRuntime) setStatus(status string, message string) {
	r.stateMu.Lock()
	r.statusValue = status
	r.statusMessage = message
	r.stateMu.Unlock()
}

// setReady 更新就绪状态
func (r *ImageMemoryRuntime) setReady(store *storage.VectorStore, fingerprint map[string]any, generation int64, message string) {
	r.stateMu.Lock()
	r.vectorStore = store
	r.fingerprint = fingerprint
	r.generation = generation
	r.statusValue = "ready"
	r.statusMessage = message
	r.stateMu.Unlock()
}

// setRecovery 更新资产核对结果
func (r *ImageMemoryRuntime) setRecovery(report map[string]any) {
	r.stateMu.Lock()
	r.recovery = report
	r.stateMu.Unlock()
}

// cfg 读取图片配置项
func (r *ImageMemoryRuntime) cfg(key string, fallback any) any {
	if r.config == nil {
		return fallback
	}
	if value, ok := r.config[key]; ok {
		return value
	}
	return fallback
}

// cfgInt 读取整数配置项，对齐 Python 的 int(_cfg(key, default))
func (r *ImageMemoryRuntime) cfgInt(key string, fallback int) (int, error) {
	value, err := valueToInt(r.cfg(key, fallback))
	if err != nil {
		return 0, validationErrorf("图片配置项 %s 无法转换为整数: %v", key, err)
	}
	return value, nil
}

// cfgFloat 读取浮点配置项，对齐 Python 的 float(_cfg(key, default))
func (r *ImageMemoryRuntime) cfgFloat(key string, fallback float64) (float64, error) {
	value, err := valueToFloat(r.cfg(key, fallback))
	if err != nil {
		return 0, validationErrorf("图片配置项 %s 无法转换为浮点数: %v", key, err)
	}
	return value, nil
}

// Status 对齐 Python status()
func (r *ImageMemoryRuntime) Status() (map[string]any, error) {
	snapshot := r.snapshot()
	dimension := 0
	if snapshot.vectorStore != nil {
		dimension = snapshot.vectorStore.Dimension()
	}
	stats, err := r.metadataStore.ImageMemoryStats()
	if err != nil {
		return nil, err
	}
	progress, err := r.metadataStore.ImageIndexProgress(FingerprintHash(snapshot.fingerprint))
	if err != nil {
		return nil, err
	}
	recovery := snapshot.recovery
	if recovery == nil {
		recovery = map[string]any{"removed_files": 0, "issues": []any{}}
	}
	return map[string]any{
		"status":         snapshot.status,
		"message":        snapshot.message,
		"fingerprint":    snapshot.fingerprint,
		"dimension":      dimension,
		"generation":     snapshot.generation,
		"stats":          stats,
		"recovery":       recovery,
		"index_progress": progress,
	}, nil
}

// RecoverAssets 对齐 Python recover_assets，在发布锁内核对已提交资产
func (r *ImageMemoryRuntime) RecoverAssets(ctx context.Context) error {
	r.publicationMu.Lock()
	report, reconcileErr := r.ReconcileAssets()
	if reconcileErr != nil {
		r.publicationMu.Unlock()
		if ctx != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		return reconcileErr
	}
	// Python 在等待被取消时不会写入 recovery，这里保持同样的提交边界
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			r.publicationMu.Unlock()
			return ctxErr
		}
	}
	r.setRecovery(report)
	r.publicationMu.Unlock()
	return nil
}

// ReconcileAssets 对齐 Python reconcile_assets，清理发布中断留下的文件并报告异常
func (r *ImageMemoryRuntime) ReconcileAssets() (map[string]any, error) {
	assets, err := r.metadataStore.Query("SELECT * FROM image_assets")
	if err != nil {
		return nil, err
	}
	removed := 0
	issues := []any{}
	retained := map[string]string{}
	for _, asset := range assets {
		assetID := rowString(asset, "asset_id")
		storageKey := rowString(asset, "storage_key")
		contentHash := rowString(asset, "content_hash")
		var unreferenced bool
		err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
			aborted, abortErr := r.metadataStore.AbortImageJobsIfUnreferenced(assetID, tx.Conn())
			if abortErr != nil {
				return abortErr
			}
			unreferenced = aborted
			return nil
		})
		if err != nil {
			return nil, err
		}
		if unreferenced {
			deleted, err := r.assetStore.Delete(storageKey)
			if err != nil {
				return nil, err
			}
			if deleted {
				removed++
			}
		} else {
			retained[storageKey] = contentHash
		}
	}
	entries, err := os.ReadDir(r.assetStore.Root())
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := retained[name]; ok {
			continue
		}
		digest := name
		if index := strings.Index(name, "."); index >= 0 {
			digest = name[:index]
		}
		suffix := ""
		if index := strings.LastIndex(name, "."); index >= 0 {
			suffix = name[index:]
		}
		managed := len(digest) == 64 && isLowerHex(digest) && isManagedImageSuffix(suffix)
		temporaryDigest := ""
		if len(name) > 1 {
			temporaryDigest = name[1:]
			if index := strings.Index(temporaryDigest, "."); index >= 0 {
				temporaryDigest = temporaryDigest[:index]
			}
		}
		temporary := len(name) > 1 && name[0] == '.' && strings.HasSuffix(name, ".tmp") && len(temporaryDigest) == 64 && isLowerHex(temporaryDigest)
		fullPath := filepath.Join(r.assetStore.Root(), name)
		info, statErr := os.Stat(fullPath)
		isFile := statErr == nil && info.Mode().IsRegular()
		isSymlink := entry.Type()&os.ModeSymlink != 0
		if isFile && !isSymlink && (managed || temporary) {
			deleted, err := r.assetStore.Delete(name)
			if err != nil {
				return nil, err
			}
			if deleted {
				removed++
			}
		} else {
			issues = append(issues, map[string]any{"storage_key": name, "error": "资产目录中存在未知文件或目录"})
		}
	}
	for key, expectedHash := range retained {
		err := verifyRetainedAsset(r.assetStore, key, expectedHash)
		if err != nil {
			issues = append(issues, map[string]any{"storage_key": key, "error": err.Error()})
		}
	}
	if len(issues) > 0 {
		r.logger.Error(fmt.Sprintf("图片资产恢复核对发现 %d 项异常: %v", len(issues), issues))
	}
	return map[string]any{"removed_files": removed, "issues": issues}, nil
}

// verifyRetainedAsset 校验保留资产的登记哈希
func verifyRetainedAsset(store *ImageAssetStore, storageKey string, expectedHash string) error {
	path, err := store.PathForRead(storageKey)
	if err != nil {
		return err
	}
	actualHash, err := hashFileSHA256(path)
	if err != nil {
		return err
	}
	if actualHash != expectedHash {
		return validationErrorf("图片文件校验和与登记值不一致")
	}
	return nil
}

// isLowerHex 判断小写十六进制串
func isLowerHex(text string) bool {
	for _, symbol := range text {
		if (symbol < '0' || symbol > '9') && (symbol < 'a' || symbol > 'f') {
			return false
		}
	}
	return true
}

// isManagedImageSuffix 判断资产库管理的图片后缀
func isManagedImageSuffix(suffix string) bool {
	switch suffix {
	case ".png", ".jpg", ".bmp", ".webp":
		return true
	}
	return false
}

// PublicationGuard 对齐 Python publication_guard，在发布锁内执行回调
func (r *ImageMemoryRuntime) PublicationGuard(fn func() error) error {
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	return fn()
}
