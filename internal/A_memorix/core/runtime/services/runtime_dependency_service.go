package services

// 运行时依赖服务，对齐 Python core/runtime/services/runtime_dependency_service.py
// 关系写入服务与检索运行时依赖对象由内核注入构造器，服务只负责编排与持久化顺序

import (
	"context"
	"log/slog"
	"sync"
)

// RuntimeDependencyDeps 运行时依赖服务依赖的内核能力
type RuntimeDependencyDeps struct {
	// MetadataStore 元数据存储，nil 表示未连接
	MetadataStore any
	// VectorStore 主向量库
	VectorStore any
	// ParagraphVectorStore 段向量库
	ParagraphVectorStore any
	// GraphVectorStore 图向量库
	GraphVectorStore any
	// GraphStore 图存储
	GraphStore any
	// DualVectorPoolsEnabled 判断双向量池是否就绪
	DualVectorPoolsEnabled func() bool
	// BuildRelationWriteService 构造关系写入服务，对齐 kernel_module.RelationWriteService
	BuildRelationWriteService func() any
	// SetRelationWriteService 写回内核的关系写入服务
	SetRelationWriteService func(service any)
	// RebuildRuntimeDependents 对齐 _refresh_runtime_dependents 中的内核对象重建
	RebuildRuntimeDependents func(preserveManagers bool)
	// VectorRebuildStatus 读取向量重建状态
	VectorRebuildStatus func() map[string]any
	// SaveVectorStore 持久化单个向量库
	SaveVectorStore func(store any)
	// SaveVectorInputs 持久化向量空间输入记录，对齐 _vector_space_service._save_inputs
	SaveVectorInputs func()
	// SaveGraphStore 持久化图存储
	SaveGraphStore func()
	// GraphProjectionLock 关系图投影锁，对齐 _relation_graph_projection_lock
	GraphProjectionLock *sync.Mutex
	// SparseIndexEnabled 判断稀疏索引是否启用
	SparseIndexEnabled func() bool
	// SparseIndexEnsureLoaded 确保稀疏索引已加载
	SparseIndexEnsureLoaded func()
	// ImageRuntimePersist 持久化图片记忆向量库
	ImageRuntimePersist func()
	// Logger 日志器，nil 时使用默认日志器
	Logger *slog.Logger
}

// RuntimeDependencyService 对齐 MemoryRuntimeDependencyService
type RuntimeDependencyService struct {
	Deps RuntimeDependencyDeps
}

// NewRuntimeDependencyService 构造运行时依赖服务
func NewRuntimeDependencyService(deps RuntimeDependencyDeps) *RuntimeDependencyService {
	return &RuntimeDependencyService{Deps: deps}
}

// logger 返回可用日志器
func (s *RuntimeDependencyService) logger() *slog.Logger {
	if s == nil || s.Deps.Logger == nil {
		return slog.Default()
	}
	return s.Deps.Logger
}

// dualVectorPoolsEnabled 判断双向量池是否就绪
func (s *RuntimeDependencyService) dualVectorPoolsEnabled() bool {
	return s.Deps.DualVectorPoolsEnabled != nil && s.Deps.DualVectorPoolsEnabled()
}

// RefreshRelationWriteService 对齐 _refresh_relation_write_service
func (s *RuntimeDependencyService) RefreshRelationWriteService(ctx context.Context) {
	_ = ctx
	if s.Deps.MetadataStore == nil {
		if s.Deps.SetRelationWriteService != nil {
			s.Deps.SetRelationWriteService(nil)
		}
		return
	}
	if s.Deps.BuildRelationWriteService == nil || s.Deps.SetRelationWriteService == nil {
		return
	}
	s.Deps.SetRelationWriteService(s.Deps.BuildRelationWriteService())
}

// RebuildRuntimeDependents 对齐 _refresh_runtime_dependents
func (s *RuntimeDependencyService) RebuildRuntimeDependents(ctx context.Context, preserveManagers bool) {
	_ = ctx
	if s.Deps.MetadataStore == nil {
		return
	}
	if s.Deps.RebuildRuntimeDependents != nil {
		s.Deps.RebuildRuntimeDependents(preserveManagers)
	}
}

// Persist 对齐 _persist，按 Python 顺序保存向量库、图存储与稀疏索引
func (s *RuntimeDependencyService) Persist(ctx context.Context, forceVectors bool) error {
	_ = ctx
	vectorAvailable := s.Deps.VectorStore != nil || s.Deps.ParagraphVectorStore != nil || s.Deps.GraphVectorStore != nil
	rebuildRequired := false
	if vectorAvailable && !forceVectors && s.Deps.VectorRebuildStatus != nil {
		rebuildRequired = pyTruthy(s.Deps.VectorRebuildStatus()["vector_rebuild_required"])
	}
	dualEnabled := s.dualVectorPoolsEnabled()
	if s.Deps.VectorStore != nil && !dualEnabled {
		if rebuildRequired {
			s.logger().Debug("检测到向量库需要重建，跳过向量库持久化以保留重建提示")
		} else if s.Deps.SaveVectorStore != nil {
			s.Deps.SaveVectorStore(s.Deps.VectorStore)
		}
	}
	if dualEnabled && !rebuildRequired && s.Deps.SaveVectorStore != nil {
		if s.Deps.ParagraphVectorStore != nil {
			s.Deps.SaveVectorStore(s.Deps.ParagraphVectorStore)
		}
		if s.Deps.GraphVectorStore != nil {
			s.Deps.SaveVectorStore(s.Deps.GraphVectorStore)
		}
	}
	if !rebuildRequired && s.Deps.SaveVectorInputs != nil {
		s.Deps.SaveVectorInputs()
	}
	if s.Deps.GraphStore != nil && s.Deps.SaveGraphStore != nil {
		if s.Deps.GraphProjectionLock != nil {
			s.Deps.GraphProjectionLock.Lock()
			s.Deps.SaveGraphStore()
			s.Deps.GraphProjectionLock.Unlock()
		} else {
			s.Deps.SaveGraphStore()
		}
	}
	if s.Deps.SparseIndexEnabled != nil && s.Deps.SparseIndexEnabled() && s.Deps.SparseIndexEnsureLoaded != nil {
		s.Deps.SparseIndexEnsureLoaded()
	}
	if s.Deps.ImageRuntimePersist != nil {
		s.Deps.ImageRuntimePersist()
	}
	return nil
}
