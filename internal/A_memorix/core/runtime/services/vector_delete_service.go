package services

// 向量删除服务，对齐 Python core/runtime/services/vector_delete_service.py

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
)

// VectorDeleteDeps 向量删除服务依赖的向量库与双池状态能力
// 取数走函数端口，保证双池重建换库后服务能看到最新句柄
type VectorDeleteDeps struct {
	// LegacyStore 旧版单向量库
	LegacyStore func() *storage.VectorStore
	// ParagraphStore 段向量库
	ParagraphStore func() *storage.VectorStore
	// GraphStore 图向量库
	GraphStore func() *storage.VectorStore
	// DualVectorPoolsEnabled 判断双向量池是否就绪
	DualVectorPoolsEnabled func() bool
	// GraphVectorID 生成图向量 ID，对齐 _graph_vector_id
	GraphVectorID func(itemType string, hashValue string) string
}

// VectorDeleteService 对齐 MemoryVectorDeleteService
type VectorDeleteService struct {
	Deps VectorDeleteDeps
}

// NewVectorDeleteService 构造向量删除服务
func NewVectorDeleteService(deps VectorDeleteDeps) *VectorDeleteService {
	return &VectorDeleteService{Deps: deps}
}

// DeleteVectors 对齐 _delete_vectors_by_type，按向量类型删除并返回删除条数
func (s *VectorDeleteService) DeleteVectors(ctx context.Context, paragraphHashes []string, entityHashes []string, relationHashes []string) (int, error) {
	_ = ctx
	deleted := 0
	legacyIDs := mergeTokens(paragraphHashes, entityHashes, relationHashes)
	if legacyStore := s.legacyStore(); legacyStore != nil && len(legacyIDs) > 0 {
		count, err := legacyStore.Delete(legacyIDs)
		if err != nil {
			return deleted, err
		}
		deleted += count
	}
	if !s.dualVectorPoolsEnabled() {
		return deleted, nil
	}
	paragraphIDs := mergeTokens(paragraphHashes)
	if paragraphStore := s.paragraphStore(); paragraphStore != nil && len(paragraphIDs) > 0 {
		count, err := paragraphStore.Delete(paragraphIDs)
		if err != nil {
			return deleted, err
		}
		deleted += count
	}
	graphIDs := make([]string, 0, len(entityHashes)+len(relationHashes))
	for _, hashValue := range mergeTokens(entityHashes) {
		graphIDs = append(graphIDs, s.graphVectorID("entity", hashValue))
	}
	for _, hashValue := range mergeTokens(relationHashes) {
		graphIDs = append(graphIDs, s.graphVectorID("relation", hashValue))
	}
	if graphStore := s.graphStore(); graphStore != nil && len(graphIDs) > 0 {
		count, err := graphStore.Delete(graphIDs)
		if err != nil {
			return deleted, err
		}
		deleted += count
	}
	return deleted, nil
}

// DeleteVectorsByType 按向量池清空向量，Go 侧管理入口
// Python 的 _delete_vectors_by_type 需要显式哈希，管理端按池清空时使用本方法
func (s *VectorDeleteService) DeleteVectorsByType(ctx context.Context, storeType string) (map[string]any, error) {
	_ = ctx
	kind := strings.ToLower(strings.TrimSpace(storeType))
	deleted := 0
	deletePool := func(store *storage.VectorStore) error {
		if store == nil {
			return nil
		}
		hashes := store.KnownHashes()
		if len(hashes) == 0 {
			return nil
		}
		count, err := store.Delete(hashes)
		if err != nil {
			return err
		}
		deleted += count
		return nil
	}
	switch kind {
	case "legacy", "all":
		if err := deletePool(s.legacyStore()); err != nil {
			return nil, err
		}
		if kind == "legacy" {
			break
		}
		fallthrough
	case "paragraph":
		if err := deletePool(s.paragraphStore()); err != nil {
			return nil, err
		}
		if kind == "paragraph" {
			break
		}
		fallthrough
	case "graph", "entity", "relation":
		if err := deletePool(s.graphStore()); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("不支持的向量类型: %s", kind)
	}
	return map[string]any{"success": true, "store_type": kind, "deleted": deleted}, nil
}

// legacyStore 返回旧版向量库句柄
func (s *VectorDeleteService) legacyStore() *storage.VectorStore {
	if s.Deps.LegacyStore == nil {
		return nil
	}
	return s.Deps.LegacyStore()
}

// paragraphStore 返回段向量库句柄
func (s *VectorDeleteService) paragraphStore() *storage.VectorStore {
	if s.Deps.ParagraphStore == nil {
		return nil
	}
	return s.Deps.ParagraphStore()
}

// graphStore 返回图向量库句柄
func (s *VectorDeleteService) graphStore() *storage.VectorStore {
	if s.Deps.GraphStore == nil {
		return nil
	}
	return s.Deps.GraphStore()
}

// dualVectorPoolsEnabled 判断双向量池是否就绪
func (s *VectorDeleteService) dualVectorPoolsEnabled() bool {
	return s.Deps.DualVectorPoolsEnabled != nil && s.Deps.DualVectorPoolsEnabled()
}

// graphVectorID 生成图向量 ID
func (s *VectorDeleteService) graphVectorID(itemType string, hashValue string) string {
	if s.Deps.GraphVectorID != nil {
		return s.Deps.GraphVectorID(itemType, hashValue)
	}
	return itemType + ":" + hashValue
}
