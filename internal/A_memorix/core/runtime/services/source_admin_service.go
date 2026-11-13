package services

// 来源管理服务，对齐 Python core/runtime/services/source_admin_service.py

import (
	"context"
	"fmt"
	"strings"
)

// SourceAdminDeps 来源管理服务依赖的存储与删除能力
type SourceAdminDeps struct {
	// Initialize 确保运行时已初始化，对齐 await self.initialize()
	Initialize func(ctx context.Context) error
	// GetAllSources 列出全部来源
	GetAllSources func() ([]map[string]any, error)
	// GetEpisodeSourceQueryBlockedFlags 批量判断来源的 episode 重建阻塞态
	GetEpisodeSourceQueryBlockedFlags func(sources []string) (map[string]bool, error)
	// ExecuteDeleteAction 执行 source 模式的删除动作
	ExecuteDeleteAction func(ctx context.Context, mode string, selector map[string]any, requestedBy string, reason string) (map[string]any, error)
	// InvalidateImportManifestForSources 让相关导入清单失效
	InvalidateImportManifestForSources func(result map[string]any)
}

// SourceAdminService 对齐 MemorySourceAdminService
type SourceAdminService struct {
	Deps SourceAdminDeps
}

// NewSourceAdminService 构造来源管理服务
func NewSourceAdminService(deps SourceAdminDeps) *SourceAdminService {
	return &SourceAdminService{Deps: deps}
}

// MemorySourceAdmin 对齐 memory_source_admin
func (s *SourceAdminService) MemorySourceAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	act := normalizeAction(action)
	switch act {
	case "list":
		return s.listSources()
	case "delete":
		source := strings.TrimSpace(asString(payload["source"]))
		result, err := s.executeDelete(ctx, map[string]any{"sources": []any{source}}, payload, "memory_source_admin", "source_delete")
		if err != nil {
			return nil, err
		}
		return result, nil
	case "batch_delete":
		sources := make([]any, 0)
		if values, ok := payload["sources"].([]any); ok {
			sources = append(sources, values...)
		} else if values, ok := payload["sources"].([]string); ok {
			for _, item := range values {
				sources = append(sources, item)
			}
		}
		result, err := s.executeDelete(ctx, map[string]any{"sources": sources}, payload, "memory_source_admin", "source_batch_delete")
		if err != nil {
			return nil, err
		}
		return result, nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 source action: %s", act)}, nil
	}
}

// listSources 对齐 action=list 分支
func (s *SourceAdminService) listSources() (any, error) {
	if s.Deps.GetAllSources == nil {
		return nil, fmt.Errorf("metadata store unavailable")
	}
	sources, err := s.Deps.GetAllSources()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(sources))
	for _, row := range sources {
		names = append(names, asString(row["source"]))
	}
	blockedFlags := map[string]bool{}
	if s.Deps.GetEpisodeSourceQueryBlockedFlags != nil {
		flags, err := s.Deps.GetEpisodeSourceQueryBlockedFlags(names)
		if err != nil {
			return nil, err
		}
		if flags != nil {
			blockedFlags = flags
		}
	}
	items := make([]any, 0, len(sources))
	for _, row := range sources {
		sourceName := strings.TrimSpace(asString(row["source"]))
		item := cloneAnyMap(row)
		item["episode_rebuild_blocked"] = blockedFlags[sourceName]
		items = append(items, item)
	}
	return map[string]any{"success": true, "items": items, "count": len(items)}, nil
}

// executeDelete 对齐 delete 与 batch_delete 共用的删除编排
func (s *SourceAdminService) executeDelete(
	ctx context.Context,
	selector map[string]any,
	payload map[string]any,
	defaultRequestedBy string,
	defaultReason string,
) (map[string]any, error) {
	if s.Deps.ExecuteDeleteAction == nil {
		return nil, fmt.Errorf("delete service unavailable")
	}
	requestedBy := strings.TrimSpace(asString(payload["requested_by"]))
	if requestedBy == "" {
		requestedBy = defaultRequestedBy
	}
	reason := strings.TrimSpace(asString(payload["reason"]))
	if reason == "" {
		reason = defaultReason
	}
	result, err := s.Deps.ExecuteDeleteAction(ctx, "source", selector, requestedBy, reason)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	if s.Deps.InvalidateImportManifestForSources != nil {
		s.Deps.InvalidateImportManifestForSources(result)
	}
	return result, nil
}
