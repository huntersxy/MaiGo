package services

// 图管理服务，对齐 Python core/runtime/services/graph_admin_service.py 的动作段

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// DeleteExecutor 是删除管理服务的执行入口，图服务把节点/边/来源删除委托给它
type DeleteExecutor interface {
	ExecuteDeleteAction(ctx context.Context, payload map[string]any) (map[string]any, error)
}

// GraphProjection 是权威图投影发布端口，内核维护服务实现
type GraphProjection interface {
	PublishAuthoritativeGraphProjection(ctx context.Context) (map[string]any, error)
}

// RelationVectorWriter 是关系向量写入端口
type RelationVectorWriter interface {
	EnsureRelationVector(ctx context.Context, hashValue string, subject string, predicate string, object string, typedID bool) (map[string]any, error)
}

// EntityVectorWriter 是实体向量写入端口
type EntityVectorWriter interface {
	EnsureEntityVector(ctx context.Context, hashValue string) (bool, error)
}

// GraphAdminOptions 汇总图服务依赖，全部可选以免强绑内核
type GraphAdminOptions struct {
	Base                   *Base
	Delete                 DeleteExecutor
	Projection             GraphProjection
	RelationVectors        RelationVectorWriter
	EntityVectors          EntityVectorWriter
	RelationVectorsEnabled bool
	TypedRelationIDs       bool
	LifecyclePolicy        func() utils.RelationLifecyclePolicy
	AliasRebuilder         AliasRebuilder
	VectorInvalidator      VectorInvalidator
}

// GraphAdminService 对齐 MemoryGraphAdminService
type GraphAdminService struct {
	*Base
	deleteExecutor         DeleteExecutor
	projection             GraphProjection
	relationVectors        RelationVectorWriter
	entityVectors          EntityVectorWriter
	relationVectorsEnabled bool
	typedRelationIDs       bool
	lifecyclePolicy        func() utils.RelationLifecyclePolicy
	aliasRebuilder         AliasRebuilder
	vectorInvalidator      VectorInvalidator
}

// NewGraphAdminService 构造图管理服务
func NewGraphAdminService(options GraphAdminOptions) *GraphAdminService {
	service := &GraphAdminService{
		Base:                   options.Base,
		deleteExecutor:         options.Delete,
		projection:             options.Projection,
		relationVectors:        options.RelationVectors,
		entityVectors:          options.EntityVectors,
		relationVectorsEnabled: options.RelationVectorsEnabled,
		typedRelationIDs:       options.TypedRelationIDs,
		lifecyclePolicy:        options.LifecyclePolicy,
		aliasRebuilder:         options.AliasRebuilder,
		vectorInvalidator:      options.VectorInvalidator,
	}
	if service.lifecyclePolicy == nil {
		service.lifecyclePolicy = utils.DefaultRelationLifecyclePolicy
	}
	return service
}

// MemoryGraphAdmin 对齐 memory_graph_admin
func (s *GraphAdminService) MemoryGraphAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	if s.Meta() == nil {
		return map[string]any{"success": false, "error": "metadata store 未初始化"}, nil
	}
	act := strings.ToLower(strings.TrimSpace(action))
	switch act {
	case "get_graph":
		result := map[string]any{"success": true}
		for key, value := range s.serializeGraph(pyIntOr(payload["limit"], 200, 1)) {
			result[key] = value
		}
		return result, nil
	case "search":
		return s.searchGraph(ctx, asString(payload["query"]), clampIntRange(pyIntOr(payload["limit"], 50, 1), 1, 200)), nil
	case "node_detail":
		nodeID := asString(payload["node_id"])
		if nodeID == "" {
			nodeID = asString(payload["node"])
		}
		return s.buildGraphNodeDetail(ctx, nodeID,
			pyIntOr(payload["relation_limit"], 20, 1),
			pyIntOr(payload["paragraph_limit"], 20, 1),
			max(12, pyIntOr(payload["evidence_node_limit"], 80, 12))), nil
	case "edge_detail":
		target := asString(payload["target"])
		if target == "" {
			target = asString(payload["object"])
		}
		return s.buildGraphEdgeDetail(ctx, asString(payload["source"]), target,
			pyIntOr(payload["paragraph_limit"], 20, 1),
			max(12, pyIntOr(payload["evidence_node_limit"], 80, 12))), nil
	case "create_node":
		return s.createNode(ctx, payload), nil
	case "delete_node":
		return s.deleteNode(ctx, payload), nil
	case "rename_node":
		return s.renameNodeAction(ctx, payload), nil
	case "create_edge":
		return s.createEdge(ctx, payload), nil
	case "delete_edge":
		return s.deleteEdge(ctx, payload), nil
	case "update_edge_weight":
		return s.updateEdgeWeightAction(ctx, payload), nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 graph action: %s", act)}, nil
	}
}

// createNode 对齐 create_node 分支
func (s *GraphAdminService) createNode(ctx context.Context, payload map[string]any) map[string]any {
	name := strings.TrimSpace(asString(payload["name"]))
	if name == "" {
		name = strings.TrimSpace(asString(payload["node"]))
	}
	if name == "" {
		return map[string]any{"success": false, "error": "node name 不能为空"}
	}
	entityHash := utils.ComputeEntityHash(name)
	created := false
	revived := false
	var operation map[string]any
	err := s.Meta().Transaction(true, func(tx *storage.Tx) error {
		existing, err := s.Meta().GetEntity(entityHash)
		if err != nil {
			return err
		}
		created = existing == nil
		revived = existing != nil && rowBool(existing, "is_deleted")
		if !created && !revived {
			return nil
		}
		if _, err := s.Meta().AddEntity(name, nil, "", asMap(payload["metadata"])); err != nil {
			return err
		}
		result := map[string]any{
			"success": true,
			"created": created,
			"revived": revived,
			"node":    map[string]any{"name": name, "hash": entityHash},
		}
		reason := asString(payload["reason"])
		if reason == "" {
			reason = "graph_create_node"
		}
		updatedBy := firstNonEmpty(payload["updated_by"], payload["requested_by"], "memory_graph_admin")
		operation, err = s.Meta().RecordV5Operation("graph_create_node", name, []string{entityHash}, reason, updatedBy, result, tx.Conn())
		return err
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	projection := map[string]any{"status": "not_required", "queued": 0}
	if created || revived {
		projection = s.publishProjection(ctx, 0)
	}
	result := map[string]any{
		"operation":  operation,
		"success":    true,
		"created":    created,
		"revived":    revived,
		"projection": projection,
		"node":       map[string]any{"name": name, "hash": entityHash},
	}
	return result
}

// deleteNode 对齐 delete_node 分支
func (s *GraphAdminService) deleteNode(ctx context.Context, payload map[string]any) map[string]any {
	name := strings.TrimSpace(asString(payload["name"]))
	if name == "" {
		name = strings.TrimSpace(asString(payload["node"]))
	}
	if name == "" {
		name = strings.TrimSpace(asString(payload["hash_or_name"]))
	}
	if name == "" {
		return map[string]any{"success": false, "error": "node name 不能为空"}
	}
	reason := asString(payload["reason"])
	if reason == "" {
		reason = "graph_delete_node"
	}
	result, err := s.delegateDelete(ctx, "entity", map[string]any{"query": name}, firstNonEmpty(payload["updated_by"], payload["requested_by"], "memory_graph_admin"), reason)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	deleted := rowInt(result, "deleted_entity_count") > 0 || rowInt(result, "deleted_count") > 0
	result["deleted"] = deleted
	result["node"] = name
	return result
}

// renameNodeAction 对齐 rename_node 分支
func (s *GraphAdminService) renameNodeAction(ctx context.Context, payload map[string]any) map[string]any {
	oldName := firstNonEmpty(payload["name"], payload["old_name"], payload["node"])
	newName := firstNonEmpty(payload["new_name"], payload["target_name"])
	reason := asString(payload["reason"])
	if reason == "" {
		reason = "graph_rename_node"
	}
	updatedBy := firstNonEmpty(payload["updated_by"], "memory_graph_admin")
	result := s.renameNode(ctx, oldName, newName, reason, updatedBy, true)
	if !pyTruthy(result["success"]) {
		return result
	}
	if !pyTruthy(result["renamed"]) {
		return result
	}
	invalidation := asMap(result["vector_projection"])
	entityHash := asString(result["entity_hash"])
	relationHashMap := asMap(result["relation_hash_map"])
	relationHashes := make([]string, 0, len(relationHashMap))
	for _, value := range relationHashMap {
		relationHashes = append(relationHashes, asString(value))
	}
	rebuild, err := s.rebuildRenamedVectors(ctx, entityHash, dedupeStrings(relationHashes))
	if err != nil {
		rebuild = map[string]any{"status": "failed", "error": err.Error()}
	}
	status := "completed"
	if asString(invalidation["status"]) == "failed" || asString(rebuild["status"]) == "failed" {
		status = "failed"
	}
	result["vector_projection"] = map[string]any{"status": status, "invalidation": invalidation, "rebuild": rebuild}
	return result
}

// createEdge 对齐 create_edge 分支
func (s *GraphAdminService) createEdge(ctx context.Context, payload map[string]any) map[string]any {
	subject := strings.TrimSpace(firstNonEmpty(payload["subject"], payload["source"]))
	predicate := strings.TrimSpace(firstNonEmpty(payload["predicate"], payload["label"]))
	object := strings.TrimSpace(firstNonEmpty(payload["object"], payload["target"]))
	if subject == "" || predicate == "" || object == "" {
		return map[string]any{"success": false, "error": "subject/predicate/object 不能为空"}
	}
	confidence := 1.0
	if raw, exists := payload["confidence"]; exists && raw != nil {
		parsed, ok := asFloat(raw)
		if !ok || math.IsInf(parsed, 0) || math.IsNaN(parsed) || parsed < 0 || parsed > 1 {
			return map[string]any{"success": false, "error": "confidence 必须位于[0, 1]"}
		}
		confidence = parsed
	}
	expectedHash, err := s.Meta().ComputeRelationHash(subject, predicate, object)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	created := false
	reactivated := false
	relationHash := ""
	queued := 0
	var operation map[string]any
	err = s.Meta().Transaction(true, func(tx *storage.Tx) error {
		existing, err := s.Meta().GetRelation(expectedHash, true)
		if err != nil {
			return err
		}
		created = existing == nil
		reactivated = existing != nil && rowBool(existing, "is_inactive")
		relationHash, err = s.Meta().AddRelation(subject, predicate, object, nil, confidence,
			asString(payload["source_paragraph"]), asMap(payload["metadata"]))
		if err != nil {
			return err
		}
		if reactivated {
			if _, err := s.Meta().ApplyRelationLifecycleEvent([]string{relationHash}, utils.LifecycleEventEvidence,
				s.lifecyclePolicy(), float64(time.Now().UnixNano())/1e9, 0); err != nil {
				return err
			}
		}
		queued, err = s.Meta().ReenqueueAuthoritativeRelationGraphProjectionJobs([]map[string]any{{
			"relation_hash": relationHash,
			"subject":       subject,
			"object":        object,
		}}, nil)
		if err != nil {
			return err
		}
		relation, err := s.Meta().GetRelation(relationHash, true)
		if err != nil {
			return err
		}
		weight := confidence
		if relation != nil {
			if parsed, ok := asFloat(relation["confidence"]); ok {
				weight = parsed
			}
		}
		result := map[string]any{
			"success":     true,
			"created":     created,
			"reactivated": reactivated,
			"edge": map[string]any{
				"hash":       relationHash,
				"subject":    subject,
				"predicate":  predicate,
				"object":     object,
				"weight":     weight,
				"confidence": weight,
			},
		}
		reason := asString(payload["reason"])
		if reason == "" {
			reason = "graph_create_edge"
		}
		updatedBy := firstNonEmpty(payload["updated_by"], payload["requested_by"], "memory_graph_admin")
		operation, err = s.Meta().RecordV5Operation("graph_create_edge", relationHash, []string{relationHash}, reason, updatedBy, result, tx.Conn())
		return err
	})
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}

	projection := s.publishProjection(ctx, queued)
	vectorProjection := map[string]any{"status": "disabled"}
	if s.relationVectorsEnabled {
		if s.relationVectors == nil {
			vectorProjection = map[string]any{"status": "failed", "error": "relation_write_service_unavailable"}
		} else {
			written, err := s.relationVectors.EnsureRelationVector(ctx, relationHash, subject, predicate, object, s.typedRelationIDs)
			if err != nil {
				vectorProjection = map[string]any{"status": "failed", "error": err.Error()}
			} else {
				vectorProjection = written
			}
		}
	}
	relation, _ := s.Meta().GetRelation(relationHash, true)
	weight := confidence
	if relation != nil {
		if parsed, ok := asFloat(relation["confidence"]); ok {
			weight = parsed
		}
	}
	return map[string]any{
		"operation":   operation,
		"success":     true,
		"created":     created,
		"reactivated": reactivated,
		"edge": map[string]any{
			"hash":       relationHash,
			"subject":    subject,
			"predicate":  predicate,
			"object":     object,
			"weight":     weight,
			"confidence": weight,
		},
		"projection":        projection,
		"vector_projection": vectorProjection,
	}
}

// deleteEdge 对齐 delete_edge 分支
func (s *GraphAdminService) deleteEdge(ctx context.Context, payload map[string]any) map[string]any {
	hashValue := strings.TrimSpace(firstNonEmpty(payload["hash"], payload["relation_hash"]))
	reason := asString(payload["reason"])
	if reason == "" {
		reason = "graph_delete_edge"
	}
	requestedBy := firstNonEmpty(payload["updated_by"], payload["requested_by"], "memory_graph_admin")
	if hashValue != "" {
		result, err := s.delegateDelete(ctx, "relation", map[string]any{"query": hashValue}, requestedBy, reason)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}
		}
		deleted := rowInt(result, "deleted_relation_count")
		if deleted == 0 {
			deleted = rowInt(result, "deleted_count")
		}
		result["deleted"] = deleted
		result["hash"] = hashValue
		return result
	}
	subject := strings.TrimSpace(asString(payload["subject"]))
	object := strings.TrimSpace(asString(payload["object"]))
	if subject == "" || object == "" {
		return map[string]any{"success": false, "error": "hash 或 subject/object 不能为空"}
	}
	rows, err := s.Meta().GetRelations(subject, "", "", true)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	hashes := make([]string, 0, len(rows))
	for _, row := range rows {
		if rowString(row, "object") == object {
			hashes = append(hashes, rowString(row, "hash"))
		}
	}
	result, err := s.delegateDelete(ctx, "relation", map[string]any{"hashes": hashes, "subject": subject, "object": object}, requestedBy, reason)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	deleted := rowInt(result, "deleted_relation_count")
	if deleted == 0 {
		deleted = rowInt(result, "deleted_count")
	}
	result["deleted"] = deleted
	result["subject"] = subject
	result["object"] = object
	return result
}

// updateEdgeWeightAction 对齐 update_edge_weight 分支
func (s *GraphAdminService) updateEdgeWeightAction(ctx context.Context, payload map[string]any) map[string]any {
	weightValue := payload["weight"]
	if weightValue == nil {
		weightValue = payload["confidence"]
	}
	weight, ok := asFloat(weightValue)
	if !ok || math.IsInf(weight, 0) || math.IsNaN(weight) || weight < 0 || weight > 1 {
		return map[string]any{"success": false, "error": "weight 必须位于[0, 1]"}
	}
	reason := asString(payload["reason"])
	if reason == "" {
		reason = "graph_update_relation_confidence"
	}
	updatedBy := firstNonEmpty(payload["updated_by"], "memory_graph_admin")
	return s.updateEdgeWeight(ctx, firstNonEmpty(payload["hash"], payload["relation_hash"]),
		firstNonEmpty(payload["subject"], payload["source"]), firstNonEmpty(payload["object"], payload["target"]),
		weight, reason, updatedBy)
}

// delegateDelete 调用删除管理服务，执行器缺失时明确失败
func (s *GraphAdminService) delegateDelete(ctx context.Context, mode string, selector map[string]any, requestedBy string, reason string) (map[string]any, error) {
	if s.deleteExecutor == nil {
		return nil, fmt.Errorf("delete_admin_service 未初始化")
	}
	payload := map[string]any{"mode": mode, "selector": selector, "requested_by": requestedBy, "reason": reason}
	result, err := s.deleteExecutor.ExecuteDeleteAction(ctx, payload)
	if err != nil {
		return nil, err
	}
	normalized := asMap(result)
	if normalized == nil {
		return nil, fmt.Errorf("delete_admin execute 返回结构无效")
	}
	return normalized, nil
}

// publishProjection 对齐 _publish_authoritative_graph_projection
func (s *GraphAdminService) publishProjection(ctx context.Context, queued int) map[string]any {
	if s.projection == nil {
		status := "failed"
		if queued > 0 {
			status = "pending_retry"
		}
		return map[string]any{"status": status, "queued": queued}
	}
	result, err := s.projection.PublishAuthoritativeGraphProjection(ctx)
	if err != nil {
		status := "failed"
		if queued > 0 {
			status = "pending_retry"
		}
		return map[string]any{"status": status, "queued": queued, "error": err.Error()}
	}
	out := map[string]any{"status": "completed", "queued": queued}
	for key, value := range result {
		if key == "status" || key == "queued" {
			continue
		}
		out[key] = value
	}
	return out
}

// rebuildRenamedVectors 对齐 _rebuild_renamed_vectors
func (s *GraphAdminService) rebuildRenamedVectors(ctx context.Context, entityHash string, relationHashes []string) (map[string]any, error) {
	entityReady := false
	if s.entityVectors != nil && strings.TrimSpace(entityHash) != "" {
		if _, err := s.Meta().GetEntity(entityHash); err == nil {
			entityReady, _ = s.entityVectors.EnsureEntityVector(ctx, entityHash)
		}
	}
	relationState := map[string]any{}
	allReady := entityReady
	if s.relationVectorsEnabled && s.relationVectors != nil && len(relationHashes) > 0 {
		rows, err := s.Meta().GetRelationsByHashes(relationHashes, false)
		if err != nil {
			return nil, err
		}
		for _, hashValue := range relationHashes {
			row, ok := rows[hashValue]
			if !ok {
				relationState[hashValue] = false
				allReady = false
				continue
			}
			written, err := s.relationVectors.EnsureRelationVector(ctx, hashValue,
				rowString(row, "subject"), rowString(row, "predicate"), rowString(row, "object"), s.typedRelationIDs)
			ready := err == nil && pyTruthy(written["written"])
			relationState[hashValue] = ready
			if !ready {
				allReady = false
			}
		}
	}
	status := "failed"
	if allReady {
		status = "completed"
	}
	return map[string]any{
		"status":                   status,
		"entity":                   map[string]any{"hash": entityHash, "ready": entityReady},
		"relations":                relationState,
		"relation_vectors_enabled": s.relationVectorsEnabled,
	}, nil
}

// rebuildGraphFromMetadata 对齐 _rebuild_graph_from_metadata
func (s *GraphAdminService) rebuildGraphFromMetadata() (map[string]any, error) {
	if s.Meta() == nil {
		return nil, fmt.Errorf("metadata store 未初始化")
	}
	graphStore := s.Graph()
	if graphStore == nil {
		return nil, fmt.Errorf("图存储未初始化")
	}
	entityRows, err := s.Meta().Query("SELECT name FROM entities WHERE COALESCE(is_deleted, 0) = 0 ORDER BY name ASC")
	if err != nil {
		return nil, err
	}
	relationRows, err := s.Meta().Query(
		"SELECT hash, subject, predicate, object FROM relations WHERE (is_inactive IS NULL OR is_inactive = 0)")
	if err != nil {
		return nil, err
	}
	nameSet := map[string]struct{}{}
	nodes := make([]string, 0, len(entityRows)+len(relationRows))
	for _, row := range entityRows {
		name := strings.TrimSpace(rowString(row, "name"))
		if name == "" {
			continue
		}
		if _, ok := nameSet[name]; ok {
			continue
		}
		nameSet[name] = struct{}{}
		nodes = append(nodes, name)
	}
	edges := make([]any, 0, len(relationRows))
	hashes := make([]string, 0, len(relationRows))
	for _, row := range relationRows {
		subject := strings.TrimSpace(rowString(row, "subject"))
		object := strings.TrimSpace(rowString(row, "object"))
		if subject == "" || object == "" {
			continue
		}
		for _, name := range []string{subject, object} {
			if _, ok := nameSet[name]; ok {
				continue
			}
			nameSet[name] = struct{}{}
			nodes = append(nodes, name)
		}
		edges = append(edges, map[string]any{"source": subject, "target": object})
		hashes = append(hashes, rowString(row, "hash"))
	}
	graphStore.Clear()
	if len(nodes) > 0 {
		graphStore.AddNodes(nodes, nil)
	}
	if len(edges) > 0 {
		edgeList := make([]storage.Edge, 0, len(edges))
		for _, edgeValue := range edges {
			edge := edgeValue.(map[string]any)
			edgeList = append(edgeList, storage.Edge{Source: asString(edge["source"]), Target: asString(edge["target"])})
		}
		if _, err := graphStore.AddEdges(edgeList, nil, hashes); err != nil {
			return nil, err
		}
	}
	return map[string]any{"node_count": graphStore.NumNodes(), "edge_count": graphStore.NumEdges()}, nil
}

// deleteSources 对齐 _delete_sources
func (s *GraphAdminService) deleteSources(ctx context.Context, sources []string) map[string]any {
	tokensList := dedupeStrings(sources)
	if len(tokensList) == 0 {
		return map[string]any{"success": false, "error": "source 不能为空"}
	}
	result, err := s.delegateDelete(ctx, "source", map[string]any{"sources": tokensList}, "memory_graph_admin", "graph_source_delete")
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return result
}

// firstNonEmpty 返回第一个非空字符串值
func firstNonEmpty(values ...any) string {
	for _, value := range values {
		text := strings.TrimSpace(asString(value))
		if text != "" {
			return text
		}
	}
	return ""
}

// sortStringsDesc 按时间戳降序排序辅助
func sortStringsDesc(values []string) []string {
	out := append([]string{}, values...)
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}
