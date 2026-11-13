package services

// 删除管理服务的内核兼容入口，把 kernel_compat 的 payload 转发到实际实现

import (
	"context"
	"strings"
)

// SelectorDict 归一化选择器
func (s *DeleteAdminService) SelectorDict(ctx context.Context, payload map[string]any) (any, error) {
	if selector, ok := payload["selector"]; ok {
		return s.selectorDict(map[string]any{"selector": selector}), nil
	}
	return s.selectorDict(payload), nil
}

// ResolveParagraphTargets 解析段落目标
func (s *DeleteAdminService) ResolveParagraphTargets(ctx context.Context, payload map[string]any) (any, error) {
	includeDeleted := false
	if value, ok := payload["include_deleted"].(bool); ok {
		includeDeleted = value
	}
	return s.resolveParagraphTargets(deleteCompatSelector(payload), includeDeleted), nil
}

// ResolveEntityTargets 解析实体目标
func (s *DeleteAdminService) ResolveEntityTargets(ctx context.Context, payload map[string]any) (any, error) {
	return s.resolveEntityTargets(deleteCompatSelector(payload)), nil
}

// ResolveSourceTargets 解析来源目标
func (s *DeleteAdminService) ResolveSourceTargets(ctx context.Context, payload map[string]any) (any, error) {
	return s.resolveSourceTargets(deleteCompatSelector(payload)), nil
}

// SnapshotRelationItem 生成关系快照
func (s *DeleteAdminService) SnapshotRelationItem(ctx context.Context, payload map[string]any) (any, error) {
	hashValue := deleteCompatHash(payload)
	if hashValue == "" {
		return nil, nil
	}
	return s.snapshotRelationItem(hashValue), nil
}

// SnapshotParagraphItem 生成段落快照
func (s *DeleteAdminService) SnapshotParagraphItem(ctx context.Context, payload map[string]any) (any, error) {
	if row := deleteCompatRow(payload); row != nil {
		return s.snapshotParagraphItem(row), nil
	}
	hashValue := deleteCompatHash(payload)
	if hashValue == "" || s.Meta() == nil {
		return nil, nil
	}
	row, err := s.Meta().GetParagraph(hashValue)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return s.snapshotParagraphItem(row), nil
}

// SnapshotEntityItem 生成实体快照
func (s *DeleteAdminService) SnapshotEntityItem(ctx context.Context, payload map[string]any) (any, error) {
	if row := deleteCompatRow(payload); row != nil {
		return s.snapshotEntityItem(row), nil
	}
	hashValue := deleteCompatHash(payload)
	if hashValue == "" || s.Meta() == nil {
		return nil, nil
	}
	row, err := s.Meta().GetEntity(hashValue)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return s.snapshotEntityItem(row), nil
}

// RelationHasRemainingParagraphs 判断关系是否仍有其他段落引用
func (s *DeleteAdminService) RelationHasRemainingParagraphs(ctx context.Context, payload map[string]any) (any, error) {
	hashValue := deleteCompatHash(payload)
	if hashValue == "" {
		return false, nil
	}
	removing := make(map[string]struct{})
	for _, value := range asStringSlice(payload["removing_hashes"]) {
		removing[value] = struct{}{}
	}
	return s.relationHasRemainingParagraphs(hashValue, removing), nil
}

// BuildDeletePreviewItem 构造删除预览项
func (s *DeleteAdminService) BuildDeletePreviewItem(ctx context.Context, payload map[string]any) (any, error) {
	itemType := strings.TrimSpace(asString(payload["item_type"]))
	if itemType == "" {
		itemType = strings.TrimSpace(asString(payload["type"]))
	}
	itemHash := strings.TrimSpace(asString(payload["item_hash"]))
	if itemHash == "" {
		itemHash = strings.TrimSpace(asString(payload["hash"]))
	}
	itemKey := strings.TrimSpace(asString(payload["item_key"]))
	if itemKey == "" {
		itemKey = strings.TrimSpace(asString(payload["key"]))
	}
	return s.buildDeletePreviewItem(itemType, itemHash, itemKey, deleteCompatRow(payload)), nil
}

// BuildStandardDeleteResult 构造标准删除结果
func (s *DeleteAdminService) BuildStandardDeleteResult(ctx context.Context, payload map[string]any) (any, error) {
	return buildStandardDeleteResult(
		asString(payload["mode"]),
		asString(payload["operation_id"]),
		asMap(payload["counts"]),
		asStringSlice(payload["sources"]),
		intOr(payload["deleted_entity_count"], 0, 0),
		intOr(payload["deleted_relation_count"], 0, 0),
		intOr(payload["deleted_paragraph_count"], 0, 0),
		intOr(payload["deleted_source_count"], 0, 0),
		intOr(payload["deleted_vector_count"], 0, 0),
		intOr(payload["requested_source_count"], 0, 0),
		intOr(payload["matched_source_count"], 0, 0),
		asString(payload["error"]),
	), nil
}

// BuildDeletePlan 构建删除计划
func (s *DeleteAdminService) BuildDeletePlan(ctx context.Context, payload map[string]any) (any, error) {
	plan := s.buildDeletePlan(asString(payload["mode"]), deleteCompatSelector(payload))
	return map[string]any{
		"success":                plan.Success,
		"mode":                   plan.Mode,
		"selector":               plan.Selector,
		"items":                  plan.Items,
		"counts":                 plan.Counts,
		"vector_ids":             plan.VectorIDs,
		"sources":                plan.Sources,
		"target_hashes":          plan.TargetHashes,
		"requested_source_count": plan.RequestedSourceCount,
		"matched_source_count":   plan.MatchedSourceCount,
		"error":                  plan.ErrorMessage,
	}, nil
}

// PreviewDeleteAction 预览删除动作
func (s *DeleteAdminService) PreviewDeleteAction(ctx context.Context, payload map[string]any) (any, error) {
	return s.previewDeleteAction(asString(payload["mode"]), deleteCompatSelector(payload)), nil
}

// ExecuteDeleteAction 执行删除动作
func (s *DeleteAdminService) ExecuteDeleteAction(ctx context.Context, payload map[string]any) (any, error) {
	result, err := s.executeDeleteAction(ctx, asString(payload["mode"]), deleteCompatSelector(payload),
		asString(payload["reason"]), asString(payload["requested_by"]), payload)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// InvalidateImportManifestForSources 让导入清单按来源失效
func (s *DeleteAdminService) InvalidateImportManifestForSources(ctx context.Context, payload map[string]any) (any, error) {
	if s.importInvalidator != nil {
		s.importInvalidator.InvalidateImportManifestForSources(ctx, asStringSlice(payload["sources"]))
	}
	return nil, nil
}

// RestoreDeleteAction 恢复删除动作
func (s *DeleteAdminService) RestoreDeleteAction(ctx context.Context, payload map[string]any) (any, error) {
	return s.restoreDeleteAction(ctx, asString(payload["mode"]), deleteCompatSelector(payload),
		asString(payload["operation_id"]), asString(payload["requested_by"]), asString(payload["reason"])), nil
}

// RestoreDeleteOperation 按操作记录恢复
func (s *DeleteAdminService) RestoreDeleteOperation(ctx context.Context, payload map[string]any) (any, error) {
	operation := asMap(payload["operation"])
	if len(operation) == 0 {
		operationID := strings.TrimSpace(asString(payload["operation_id"]))
		if operationID == "" || s.Meta() == nil {
			return map[string]any{"success": false, "error": "operation 不存在"}, nil
		}
		loaded, err := s.Meta().GetDeleteOperation(operationID)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		if loaded == nil {
			return map[string]any{"success": false, "error": "operation 不存在"}, nil
		}
		operation = loaded
	}
	return s.restoreDeleteOperation(ctx, operation), nil
}

// PurgeDeletedMemory 清理回收站
func (s *DeleteAdminService) PurgeDeletedMemory(ctx context.Context, payload map[string]any) (any, error) {
	return s.purgeDeletedMemory(optionalFloat(payload["grace_hours"]), intOr(payload["limit"], 1000, 1)), nil
}

// deleteCompatSelector 读取选择器，缺省时退化为整个 payload
func deleteCompatSelector(payload map[string]any) map[string]any {
	if selector, ok := payload["selector"]; ok {
		return asMap(selector)
	}
	return payload
}

// deleteCompatHash 读取 hash 或目标值
func deleteCompatHash(payload map[string]any) string {
	for _, key := range []string{"hash", "hash_value", "item_hash", "target"} {
		if value := strings.TrimSpace(asString(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

// deleteCompatRow 读取快照行，存在返回副本，缺省返回 nil
func deleteCompatRow(payload map[string]any) map[string]any {
	for _, key := range []string{"row", "item"} {
		if row := asMap(payload[key]); len(row) > 0 {
			return row
		}
	}
	return nil
}
