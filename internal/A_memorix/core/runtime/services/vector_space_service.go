package services

// 向量空间目录服务，对齐 vector_space_service.py 的模型指纹分库与目录维护

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
)

// VectorSpaceDeps 向量空间服务的依赖端口
type VectorSpaceDeps struct {
	// CurrentEmbeddingFingerprintForValidation 用于校验的当前 embedding 指纹
	CurrentEmbeddingFingerprintForValidation func(ctx context.Context, dimension *int) map[string]any
	// StoredEmbeddingFingerprint 当前向量库已存的 embedding 指纹
	StoredEmbeddingFingerprint func(ctx context.Context) map[string]any
	// ActiveVectorSpaceID 当前活动向量空间 id
	ActiveVectorSpaceID func() string
	// SetActiveVectorSpaceID 写入当前活动向量空间 id
	SetActiveVectorSpaceID func(string)
	// TargetVectorSpaceID 正在同步的目标向量空间 id
	TargetVectorSpaceID func() string
	// RebuildLock 向量重建互斥锁
	RebuildLock func() *sync.Mutex
	// SaveInputs 持久化当前空间的输入指纹表，对齐 _save_inputs
	SaveInputs func() error
	// VectorRebuildStatus 委托向量运行时读取重建状态
	VectorRebuildStatus func(ctx context.Context) (map[string]any, error)
	// VectorStoreSnapshot 委托向量运行时生成存储快照
	VectorStoreSnapshot func(ctx context.Context, store any) map[string]any
	// VectorPoolsStatus 委托向量运行时读取池状态
	VectorPoolsStatus func(ctx context.Context) map[string]any
}

// VectorSpaceService 向量空间目录服务
type VectorSpaceService struct {
	*Base
	deps VectorSpaceDeps
}

// NewVectorSpaceService 构造向量空间目录服务
func NewVectorSpaceService(base *Base, deps VectorSpaceDeps) *VectorSpaceService {
	return &VectorSpaceService{Base: base, deps: deps}
}

var vectorSpaceIDPattern = regexp.MustCompile("^[0-9a-f]{64}$")

// InputHash 对齐 input_hash
func (s *VectorSpaceService) InputHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// SpaceID 对齐 space_id
func (s *VectorSpaceService) SpaceID(fingerprint map[string]any) string {
	sum := sha256.Sum256([]byte(asString(fingerprint["hash"])))
	return hex.EncodeToString(sum[:])
}

func spaceFloat(value any) float64 {
	parsed, _ := asFloat(value)
	return parsed
}

func (s *VectorSpaceService) catalogRoot() string {
	return filepath.Join(s.DataDir(), "vectors", "spaces")
}

// ReadActiveSpace 对齐 _read_active_space
func (s *VectorSpaceService) ReadActiveSpace(ctx context.Context) string {
	path := filepath.Join(s.DataDir(), "vectors", "active.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return asString(payload["space_id"])
}

func writeJSONFile(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o666); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// VectorsRoot 对齐共享的 vectors 根目录
func (s *VectorSpaceService) VectorsRoot(ctx context.Context) string {
	spaceID := ""
	if s.deps.ActiveVectorSpaceID != nil {
		spaceID = strings.TrimSpace(s.deps.ActiveVectorSpaceID())
	}
	if spaceID != "" {
		return filepath.Join(s.DataDir(), "vectors", "spaces", spaceID)
	}
	return filepath.Join(s.DataDir(), "vectors")
}

// RecordInput 对齐 record_input
func (s *VectorSpaceService) RecordInput(ctx context.Context, itemType string, itemID string, text string, inputs map[string]string) {
	if inputs != nil {
		inputs[itemType+":"+itemID] = s.InputHash(text)
	}
}

// ListSpaces 对齐 _list_spaces
func (s *VectorSpaceService) ListSpaces(ctx context.Context, targetSpaceID string) ([]map[string]any, error) {
	catalog := s.catalogRoot()
	if !pathExists(catalog) {
		return []map[string]any{}, nil
	}
	entries, err := os.ReadDir(catalog)
	if err != nil {
		return nil, err
	}
	selected := s.ReadActiveSpace(ctx)
	activeSpaceID := ""
	if s.deps.ActiveVectorSpaceID != nil {
		activeSpaceID = s.deps.ActiveVectorSpaceID()
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !vectorSpaceIDPattern.MatchString(entry.Name()) {
			continue
		}
		root := filepath.Join(catalog, entry.Name())
		info := map[string]any{}
		if raw, err := os.ReadFile(filepath.Join(root, "space.json")); err == nil {
			_ = json.Unmarshal(raw, &info)
		}
		fingerprint := asMap(info["embedding_fingerprint"])
		state := "saved"
		if entry.Name() == targetSpaceID {
			state = "syncing"
			if s.deps.CurrentEmbeddingFingerprintForValidation != nil {
				if current := s.deps.CurrentEmbeddingFingerprintForValidation(ctx, nil); current != nil {
					fingerprint = current
				}
			}
		} else if entry.Name() == activeSpaceID || entry.Name() == selected {
			state = "active"
		}
		counts := map[string]int{}
		for _, pool := range []string{"paragraph", "graph", "single"} {
			scanDir := root
			if pool != "single" {
				scanDir = filepath.Join(root, pool)
			}
			metaPath := filepath.Join(scanDir, "vectors_metadata.json")
			raw, err := os.ReadFile(metaPath)
			if err != nil {
				continue
			}
			var meta map[string]any
			if err := json.Unmarshal(raw, &meta); err != nil {
				continue
			}
			counts[pool] = len(asList(meta["known_hashes"])) - len(asList(meta["deleted_ids"]))
		}
		vectorCount := 0
		for _, value := range counts {
			vectorCount += value
		}
		sizeBytes := int64(0)
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && info.Mode().IsRegular() {
				sizeBytes += info.Size()
			}
			return nil
		})
		item := map[string]any{}
		for key, value := range info {
			item[key] = value
		}
		item["space_id"] = entry.Name()
		item["embedding_fingerprint"] = fingerprint
		item["state"] = state
		item["can_delete"] = state == "saved"
		item["vector_count"] = vectorCount
		item["counts"] = counts
		item["size_bytes"] = sizeBytes
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return spaceFloat(items[i]["last_used_at"]) > spaceFloat(items[j]["last_used_at"])
	})
	return items, nil
}

// DeleteSpace 对齐 delete_space
func (s *VectorSpaceService) DeleteSpace(ctx context.Context, spaceID string) (map[string]any, error) {
	if s.deps.RebuildLock != nil {
		if lock := s.deps.RebuildLock(); lock != nil {
			lock.Lock()
			defer lock.Unlock()
		}
	}
	if !vectorSpaceIDPattern.MatchString(spaceID) {
		return map[string]any{"success": false, "error": "vector_space_not_found"}, nil
	}
	activeSpaceID := ""
	if s.deps.ActiveVectorSpaceID != nil {
		activeSpaceID = s.deps.ActiveVectorSpaceID()
	}
	targetSpaceID := ""
	if s.deps.TargetVectorSpaceID != nil {
		targetSpaceID = s.deps.TargetVectorSpaceID()
	}
	selected := s.ReadActiveSpace(ctx)
	if spaceID == activeSpaceID || spaceID == targetSpaceID || spaceID == selected {
		return map[string]any{"success": false, "error": "vector_space_in_use"}, nil
	}
	root := filepath.Join(s.catalogRoot(), spaceID)
	if !pathExists(root) {
		return map[string]any{"success": false, "error": "vector_space_not_found"}, nil
	}
	if err := os.RemoveAll(root); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "deleted": spaceID}, nil
}

// RegisterCurrentSpace 对齐 _register_current_space
func (s *VectorSpaceService) RegisterCurrentSpace(ctx context.Context, fingerprint map[string]any) error {
	spaceID := ""
	if s.deps.ActiveVectorSpaceID != nil {
		spaceID = s.deps.ActiveVectorSpaceID()
	}
	root := filepath.Join(s.catalogRoot(), spaceID)
	path := filepath.Join(root, "space.json")
	previous := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	createdAt := spaceFloat(previous["created_at"])
	if _, exists := previous["created_at"]; !exists {
		createdAt = float64(time.Now().UnixNano()) / 1e9
	}
	payload := map[string]any{
		"space_id":              spaceID,
		"embedding_fingerprint": fingerprint,
		"created_at":            createdAt,
		"last_used_at":          float64(time.Now().UnixNano()) / 1e9,
	}
	if err := writeJSONFile(path, payload); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(s.DataDir(), "vectors", "active.json"), map[string]any{"space_id": spaceID}); err != nil {
		return err
	}
	if s.deps.SaveInputs != nil {
		return s.deps.SaveInputs()
	}
	return nil
}

// MigrateLegacySpace 对齐 _migrate_legacy_space
func (s *VectorSpaceService) MigrateLegacySpace(ctx context.Context) (bool, error) {
	activeSpaceID := ""
	if s.deps.ActiveVectorSpaceID != nil {
		activeSpaceID = s.deps.ActiveVectorSpaceID()
	}
	if activeSpaceID != "" {
		return false, nil
	}
	var fingerprint map[string]any
	if s.deps.StoredEmbeddingFingerprint != nil {
		fingerprint = s.deps.StoredEmbeddingFingerprint(ctx)
	}
	if fingerprint == nil {
		return false, nil
	}
	oldRoot := filepath.Join(s.DataDir(), "vectors")
	spaceID := s.SpaceID(fingerprint)
	target := filepath.Join(s.catalogRoot(), spaceID)
	if err := os.MkdirAll(target, 0o777); err != nil {
		return false, err
	}
	for _, name := range []string{
		"paragraph",
		"graph",
		"dual_ready.json",
		"embedding_inputs.json",
		"vectors.bin",
		"vectors_ids.bin",
		"vectors.index",
		"vectors_metadata.json",
		"vectors_metadata.pkl",
	} {
		source := filepath.Join(oldRoot, name)
		if !pathExists(source) {
			continue
		}
		if err := os.Rename(source, filepath.Join(target, name)); err != nil {
			return false, fmt.Errorf("迁移旧向量空间失败: %w", err)
		}
	}
	if s.deps.SetActiveVectorSpaceID != nil {
		s.deps.SetActiveVectorSpaceID(spaceID)
	}
	return true, s.RegisterCurrentSpace(ctx, fingerprint)
}

// VectorRebuildStatus 委托向量运行时重建状态
func (s *VectorSpaceService) VectorRebuildStatus(ctx context.Context) (map[string]any, error) {
	if s != nil && s.deps.VectorRebuildStatus != nil {
		return s.deps.VectorRebuildStatus(ctx)
	}
	return map[string]any{}, nil
}

// VectorStoreSnapshot 委托向量运行时存储快照
func (s *VectorSpaceService) VectorStoreSnapshot(ctx context.Context, store any) map[string]any {
	if s != nil && s.deps.VectorStoreSnapshot != nil {
		return s.deps.VectorStoreSnapshot(ctx, store)
	}
	typed, _ := store.(*storage.VectorStore)
	return vectorStoreSnapshotOrEmpty(typed)
}

// VectorPoolsStatus 委托向量运行时的池状态
func (s *VectorSpaceService) VectorPoolsStatus(ctx context.Context) map[string]any {
	if s != nil && s.deps.VectorPoolsStatus != nil {
		return s.deps.VectorPoolsStatus(ctx)
	}
	return map[string]any{}
}

// vectorStoreSnapshotOrEmpty 生成对齐 Python 的向量库快照
func vectorStoreSnapshotOrEmpty(store *storage.VectorStore) map[string]any {
	if store == nil {
		return map[string]any{"available": false, "dimension": 0, "num_vectors": 0, "has_data": false}
	}
	hasData := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				hasData = false
			}
		}()
		hasData = store.HasData()
	}()
	return map[string]any{
		"available":   true,
		"dimension":   store.Dimension(),
		"num_vectors": store.NumVectors(),
		"has_data":    hasData,
	}
}
