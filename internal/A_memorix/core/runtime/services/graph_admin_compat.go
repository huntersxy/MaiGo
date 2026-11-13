package services

// 图管理服务的内核兼容入口，把 kernel_compat 的 payload 转发到实际实现
// Python 侧这些方法以 *args/**kwargs 直接透传，Go 版统一用 payload 承载关键字参数

import "context"

// SerializeGraph 序列化图结构
func (s *GraphAdminService) SerializeGraph(ctx context.Context, payload map[string]any) (any, error) {
	return s.serializeGraph(intOr(payload["limit"], 200, 1)), nil
}

// GraphSearchMatchRank 计算单个字段的匹配等级
func (s *GraphAdminService) GraphSearchMatchRank(ctx context.Context, payload map[string]any) (any, error) {
	rank, ok := graphSearchMatchRank(asString(payload["value"]), asString(payload["keyword"]))
	if !ok {
		return nil, nil
	}
	return rank, nil
}

// PickGraphSearchMatch 从字段列表中选择最佳匹配
func (s *GraphAdminService) PickGraphSearchMatch(ctx context.Context, payload map[string]any) (any, error) {
	field, value, rank, ok := pickGraphSearchMatch(graphCompatFields(payload["fields"]), asString(payload["keyword"]))
	if !ok {
		return nil, nil
	}
	return map[string]any{"field": field, "value": value, "rank": rank}, nil
}

// SearchGraph 搜索图节点与关系
func (s *GraphAdminService) SearchGraph(ctx context.Context, payload map[string]any) (any, error) {
	return s.searchGraph(ctx, asString(payload["query"]), intOr(payload["limit"], 50, 1)), nil
}

// DedupeStrings 按序去重去空
func (s *GraphAdminService) DedupeStrings(ctx context.Context, payload map[string]any) (any, error) {
	return dedupeStrings(asStringSlice(payload["values"])), nil
}

// BuildGraphEdgeLabel 构造图边标签
func (s *GraphAdminService) BuildGraphEdgeLabel(ctx context.Context, payload map[string]any) (any, error) {
	return buildGraphEdgeLabel(asStringSlice(payload["predicates"])), nil
}

// TrimText 截断文本
func (s *GraphAdminService) TrimText(ctx context.Context, payload map[string]any) (any, error) {
	return trimText(payload["value"], intOr(payload["limit"], 220, 1)), nil
}

// FormatRelationText 格式化关系文本
func (s *GraphAdminService) FormatRelationText(ctx context.Context, payload map[string]any) (any, error) {
	return formatRelationText(asString(payload["subject"]), asString(payload["predicate"]), asString(payload["object"])), nil
}

// QueryRelationRowsByHashes 按 hash 查询关系行
func (s *GraphAdminService) QueryRelationRowsByHashes(ctx context.Context, payload map[string]any) (any, error) {
	includeInactive := false
	if value, ok := payload["include_inactive"].(bool); ok {
		includeInactive = value
	}
	return s.queryRelationRowsByHashes(asStringSlice(payload["hashes"]), includeInactive), nil
}

// QueryDistinctParagraphHashesForRelations 查询关系关联的段落哈希
func (s *GraphAdminService) QueryDistinctParagraphHashesForRelations(ctx context.Context, payload map[string]any) (any, error) {
	return s.queryDistinctParagraphHashesForRelations(asStringSlice(payload["hashes"]), intOr(payload["limit"], 0, 0)), nil
}

// LoadParagraphRows 批量加载段落行
func (s *GraphAdminService) LoadParagraphRows(ctx context.Context, payload map[string]any) (any, error) {
	return s.loadParagraphRows(asStringSlice(payload["hashes"])), nil
}

// ResolveGraphNodeName 解析图节点显示名
func (s *GraphAdminService) ResolveGraphNodeName(ctx context.Context, payload map[string]any) (any, error) {
	nodeID := asString(payload["node_id"])
	if nodeID == "" {
		nodeID = asString(payload["node"])
	}
	return s.resolveGraphNodeName(nodeID), nil
}

// GetRelatedRelationRowsForEntity 查询实体相关关系
func (s *GraphAdminService) GetRelatedRelationRowsForEntity(ctx context.Context, payload map[string]any) (any, error) {
	name := asString(payload["entity_name"])
	if name == "" {
		name = asString(payload["name"])
	}
	return s.getRelatedRelationRowsForEntity(name, intOr(payload["limit"], 0, 0)), nil
}

// BuildRelationSummary 构造关系摘要
func (s *GraphAdminService) BuildRelationSummary(ctx context.Context, payload map[string]any) (any, error) {
	return s.buildRelationSummary(graphCompatRow(payload), graphCompatOptionalStrings(payload, "paragraph_hashes")), nil
}

// BuildParagraphSummary 构造段落摘要
func (s *GraphAdminService) BuildParagraphSummary(ctx context.Context, payload map[string]any) (any, error) {
	return s.buildParagraphSummary(graphCompatRow(payload)), nil
}

// EvidenceEntityNodeID 生成实体证据节点 id
func (s *GraphAdminService) EvidenceEntityNodeID(ctx context.Context, payload map[string]any) (any, error) {
	return evidenceEntityNodeID(asString(payload["name"])), nil
}

// EvidenceRelationNodeID 生成关系证据节点 id
func (s *GraphAdminService) EvidenceRelationNodeID(ctx context.Context, payload map[string]any) (any, error) {
	return evidenceRelationNodeID(asString(payload["hash"])), nil
}

// EvidenceParagraphNodeID 生成段落证据节点 id
func (s *GraphAdminService) EvidenceParagraphNodeID(ctx context.Context, payload map[string]any) (any, error) {
	return evidenceParagraphNodeID(asString(payload["hash"])), nil
}

// BuildEvidenceGraph 构造证据图
func (s *GraphAdminService) BuildEvidenceGraph(ctx context.Context, payload map[string]any) (any, error) {
	focus := asStringSlice(payload["focus_entities"])
	relationRows := s.queryRelationRowsByHashes(asStringSlice(payload["relation_hashes"]), false)
	if rows, ok := payload["relation_rows"].([]map[string]any); ok {
		relationRows = rows
	}
	paragraphRows := s.loadParagraphRows(asStringSlice(payload["paragraph_hashes"]))
	if rows, ok := payload["paragraph_rows"].([]map[string]any); ok {
		paragraphRows = rows
	}
	return s.buildEvidenceGraph(focus, relationRows, paragraphRows, intOr(payload["node_limit"], 80, 0)), nil
}

// BuildGraphNodeDetail 构造节点详情
func (s *GraphAdminService) BuildGraphNodeDetail(ctx context.Context, payload map[string]any) (any, error) {
	nodeID := asString(payload["node_id"])
	if nodeID == "" {
		nodeID = asString(payload["node"])
	}
	return s.buildGraphNodeDetail(ctx, nodeID,
		intOr(payload["relation_limit"], 20, 1),
		intOr(payload["paragraph_limit"], 20, 1),
		maxInt(12, intOr(payload["evidence_node_limit"], 80, 12))), nil
}

// BuildGraphEdgeDetail 构造边详情
func (s *GraphAdminService) BuildGraphEdgeDetail(ctx context.Context, payload map[string]any) (any, error) {
	source := asString(payload["source"])
	target := asString(payload["target"])
	if target == "" {
		target = asString(payload["object"])
	}
	return s.buildGraphEdgeDetail(ctx, source, target,
		intOr(payload["paragraph_limit"], 20, 1),
		maxInt(12, intOr(payload["evidence_node_limit"], 80, 12))), nil
}

// DeleteSources 删除来源
func (s *GraphAdminService) DeleteSources(ctx context.Context, payload map[string]any) (any, error) {
	return s.deleteSources(ctx, asStringSlice(payload["sources"])), nil
}

// RebuildGraphFromMetadata 从元数据重建图
func (s *GraphAdminService) RebuildGraphFromMetadata(ctx context.Context, payload map[string]any) (any, error) {
	result, err := s.rebuildGraphFromMetadata()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RenameNode 重命名图节点
func (s *GraphAdminService) RenameNode(ctx context.Context, payload map[string]any) (any, error) {
	record := true
	if value, ok := payload["record_operation"].(bool); ok {
		record = value
	}
	return s.renameNode(ctx,
		asString(payload["old_name"]),
		asString(payload["new_name"]),
		asString(payload["reason"]),
		asString(payload["updated_by"]),
		record), nil
}

// UpdateEdgeWeight 更新边权重
func (s *GraphAdminService) UpdateEdgeWeight(ctx context.Context, payload map[string]any) (any, error) {
	weight := 0.0
	if value, ok := asFloat(payload["weight"]); ok {
		weight = value
	}
	return s.updateEdgeWeight(ctx,
		asString(payload["relation_hash"]),
		asString(payload["subject"]),
		asString(payload["object"]),
		weight,
		asString(payload["reason"]),
		asString(payload["updated_by"])), nil
}

// graphCompatRow 把 payload 里的 row 或平铺列转成行字典
func graphCompatRow(payload map[string]any) map[string]any {
	if row := asMap(payload["row"]); len(row) > 0 {
		return row
	}
	if row := asMap(payload["item"]); len(row) > 0 {
		return row
	}
	row := make(map[string]any, len(payload))
	for key, value := range payload {
		row[key] = value
	}
	return row
}

// graphCompatOptionalStrings 读取可选字符串列表，缺省返回 nil
func graphCompatOptionalStrings(payload map[string]any, key string) []string {
	values := asStringSlice(payload[key])
	if len(values) == 0 {
		return nil
	}
	return values
}

// graphCompatFields 把字段列表解析成字段名与值对
func graphCompatFields(value any) [][2]string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([][2]string, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case []any:
			if len(typed) >= 2 {
				out = append(out, [2]string{asString(typed[0]), asString(typed[1])})
			}
		case []string:
			if len(typed) >= 2 {
				out = append(out, [2]string{typed[0], typed[1]})
			}
		case map[string]any:
			out = append(out, [2]string{asString(typed["field"]), asString(typed["value"])})
		}
	}
	return out
}
