package webuiapi

// WebUI 图谱、来源、Episode 管理路由；服务缺失时由 invokeMemoryAdmin 返回真实 503

import (
	"context"
	"net/http"
	"strings"

	memruntime "maibot-go/internal/A_memorix/core/runtime"
)

func bodyRequiredString(w http.ResponseWriter, payload map[string]any, key string) (string, bool) {
	value := strings.TrimSpace(anyTrimmed(payload[key]))
	if value == "" {
		writeValidationError(w, key+" 不能为空")
		return "", false
	}
	return value, true
}

func (d Deps) handleMemoryGraphGet(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 200, 1, 5000)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "get_graph", map[string]any{"limit": limit})
}

func (d Deps) handleMemoryGraphSearch(w http.ResponseWriter, r *http.Request) {
	query, ok := mqRequiredString(w, r, "query")
	if !ok {
		return
	}
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "search", map[string]any{"query": query, "limit": limit})
}

func (d Deps) handleMemoryGraphNodeDetail(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := mqRequiredString(w, r, "node_id")
	if !ok {
		return
	}
	relationLimit, ok := mqInt(w, r, "relation_limit", 20, 1, 100)
	if !ok {
		return
	}
	paragraphLimit, ok := mqInt(w, r, "paragraph_limit", 20, 1, 100)
	if !ok {
		return
	}
	evidenceLimit, ok := mqInt(w, r, "evidence_node_limit", 80, 12, 200)
	if !ok {
		return
	}
	d.invokeGraphDetail(w, r, "node_detail", map[string]any{
		"node_id": nodeID, "relation_limit": relationLimit,
		"paragraph_limit": paragraphLimit, "evidence_node_limit": evidenceLimit,
	})
}

func (d Deps) handleMemoryGraphEdgeDetail(w http.ResponseWriter, r *http.Request) {
	source, ok := mqRequiredString(w, r, "source")
	if !ok {
		return
	}
	target, ok := mqRequiredString(w, r, "target")
	if !ok {
		return
	}
	paragraphLimit, ok := mqInt(w, r, "paragraph_limit", 20, 1, 100)
	if !ok {
		return
	}
	evidenceLimit, ok := mqInt(w, r, "evidence_node_limit", 80, 12, 200)
	if !ok {
		return
	}
	d.invokeGraphDetail(w, r, "edge_detail", map[string]any{
		"source": source, "target": target,
		"paragraph_limit": paragraphLimit, "evidence_node_limit": evidenceLimit,
	})
}

// invokeGraphDetail 图谱详情：服务返回 success=false 时按 Python 语义映射 404
func (d Deps) invokeGraphDetail(w http.ResponseWriter, r *http.Request, action string, payload map[string]any) {
	host := memorixHostCaller()
	if host == nil {
		d.invokeMemoryAdmin(w, r, "memory_graph_admin", action, payload)
		return
	}
	result, err := host.invoke(r, "memory_graph_admin", action, payload)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if result == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_graph_admin/"+action)
		return
	}
	if mapped, ok := result.(map[string]any); ok && memoryNotFound(mapped) {
		writeAPIError(w, http.StatusNotFound, firstNonEmptyText(mapped["error"], firstNonEmptyText(mapped["detail"], "未找到图谱对象")))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// memoryAdminCaller 抽象宿主 Invoke 与内核直连两种调用方式
type memoryAdminCaller struct{}

func memorixHostCaller() *memoryAdminCaller { return &memoryAdminCaller{} }

func (c *memoryAdminCaller) invoke(r *http.Request, component, action string, payload map[string]any) (any, error) {
	args := cloneMemoryPayload(payload)
	args["action"] = action
	host := memorixDefaultHost()
	if service, ok := host.(interface {
		Invoke(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error)
	}); ok {
		return service.Invoke(r.Context(), component, args, nil)
	}
	kernel := runtimeKernel()
	if kernel == nil {
		return nil, nil
	}
	command, err := memruntime.ParseAdminCommand(component, args)
	if err != nil {
		return nil, err
	}
	return memruntime.DispatchAdminCommand(r.Context(), kernel, command)
}

func (d Deps) handleMemoryGraphParagraphDetail(w http.ResponseWriter, r *http.Request) {
	paragraphHash, ok := mqRequiredString(w, r, "paragraph_hash")
	if !ok {
		return
	}
	evidenceLimit, ok := mqInt(w, r, "evidence_node_limit", 80, 12, 200)
	if !ok {
		return
	}
	rows, err := queryMemoryRows(`SELECT hash, content, source, knowledge_type, word_count, vector_index,
		created_at, updated_at, metadata, is_deleted FROM paragraphs WHERE hash = ? LIMIT 1`, paragraphHash)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "查询长期记忆 metadata 失败: "+err.Error())
		return
	}
	if len(rows) == 0 || anyBoolInt(rows[0]["is_deleted"]) {
		writeAPIError(w, http.StatusNotFound, "未找到段落: "+paragraphHash)
		return
	}
	row := rows[0]
	entities := queryMemoryRowsOptional(`SELECT e.hash, e.name, e.appearance_count FROM paragraph_entities pe
		JOIN entities e ON e.hash = pe.entity_hash
		WHERE pe.paragraph_hash = ? AND COALESCE(e.is_deleted, 0) = 0
		ORDER BY e.appearance_count DESC LIMIT ?`, paragraphHash, evidenceLimit)
	relations := queryMemoryRowsOptional(`SELECT r.hash, r.subject, r.predicate, r.object, r.confidence
		FROM paragraph_relations pr JOIN relations r ON r.hash = pr.relation_hash
		WHERE pr.paragraph_hash = ? ORDER BY COALESCE(r.last_reinforced, r.created_at, 0) DESC LIMIT ?`, paragraphHash, evidenceLimit)
	entityItems := make([]map[string]any, 0, len(entities))
	for _, item := range entities {
		entityItems = append(entityItems, map[string]any{
			"hash": anyTrimmed(item["hash"]), "name": anyTrimmed(item["name"]),
			"appearance_count": anyInt(item["appearance_count"]),
		})
	}
	relationItems := make([]map[string]any, 0, len(relations))
	for _, item := range relations {
		relationItems = append(relationItems, map[string]any{
			"hash": anyTrimmed(item["hash"]), "subject": anyTrimmed(item["subject"]),
			"predicate": anyTrimmed(item["predicate"]), "object": anyTrimmed(item["object"]),
			"confidence": memoryFloatOrNil(item["confidence"]),
			"title":      formatMemoryRelation(anyTrimmed(item["subject"]), anyTrimmed(item["predicate"]), anyTrimmed(item["object"])),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"paragraph": map[string]any{
			"hash": anyTrimmed(row["hash"]), "content": anyTrimmed(row["content"]),
			"source": anyTrimmed(row["source"]), "knowledge_type": firstNonEmptyText(row["knowledge_type"], "mixed"),
			"word_count": anyInt(row["word_count"]), "vector_indexed": row["vector_index"] != nil,
			"created_at": memoryFloatOrNil(row["created_at"]), "updated_at": memoryFloatOrNil(row["updated_at"]),
			"metadata": decodeMemoryMetadata(row["metadata"]),
		},
		"entities":       entityItems,
		"relations":      relationItems,
		"counts":         map[string]any{"entities": len(entityItems), "relations": len(relationItems)},
		"evidence_nodes": []any{},
	})
}

func (d Deps) handleMemoryGraphCreateNode(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	name, ok := bodyRequiredString(w, payload, "name")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "create_node", map[string]any{
		"name": name, "reason": anyTrimmed(payload["reason"]), "updated_by": defaultUpdatedBy(payload),
	})
}

func (d Deps) handleMemoryGraphDeleteNode(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	name, ok := bodyRequiredString(w, payload, "name")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "delete_node", map[string]any{
		"name": name, "reason": anyTrimmed(payload["reason"]), "updated_by": defaultUpdatedBy(payload),
	})
}

func (d Deps) handleMemoryGraphRenameNode(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	oldName, ok := bodyRequiredString(w, payload, "old_name")
	if !ok {
		return
	}
	newName, ok := bodyRequiredString(w, payload, "new_name")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "rename_node", map[string]any{
		"old_name": oldName, "new_name": newName,
		"reason": anyTrimmed(payload["reason"]), "updated_by": defaultUpdatedBy(payload),
	})
}

func (d Deps) handleMemoryGraphCreateEdge(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	subject, ok := bodyRequiredString(w, payload, "subject")
	if !ok {
		return
	}
	predicate, ok := bodyRequiredString(w, payload, "predicate")
	if !ok {
		return
	}
	object, ok := bodyRequiredString(w, payload, "object")
	if !ok {
		return
	}
	confidence := 1.0
	if value, exists := payload["confidence"]; exists && value != nil {
		parsed, ok := anyFloat(value)
		if !ok || parsed < 0 || parsed > 1 {
			writeValidationError(w, "confidence 必须在 0 到 1 之间")
			return
		}
		confidence = parsed
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "create_edge", map[string]any{
		"subject": subject, "predicate": predicate, "object": object, "confidence": confidence,
		"reason": anyTrimmed(payload["reason"]), "updated_by": defaultUpdatedBy(payload),
	})
}

func (d Deps) handleMemoryGraphDeleteEdge(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	hash := anyTrimmed(payload["hash"])
	subject := anyTrimmed(payload["subject"])
	object := anyTrimmed(payload["object"])
	if hash == "" && (subject == "" || object == "") {
		writeValidationError(w, "hash 或 subject+object 至少需要提供一组")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "delete_edge", map[string]any{
		"hash": hash, "subject": subject, "object": object,
		"reason": anyTrimmed(payload["reason"]), "updated_by": defaultUpdatedBy(payload),
	})
}

func (d Deps) handleMemoryGraphUpdateEdgeWeight(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	weightValue, exists := payload["weight"]
	if !exists {
		writeValidationError(w, "weight 不能为空")
		return
	}
	weight, ok := anyFloat(weightValue)
	if !ok || weight < 0 || weight > 1 {
		writeValidationError(w, "weight 必须在 0 到 1 之间")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_graph_admin", "update_edge_weight", map[string]any{
		"hash": anyTrimmed(payload["hash"]), "subject": anyTrimmed(payload["subject"]),
		"object": anyTrimmed(payload["object"]), "weight": weight,
		"reason": anyTrimmed(payload["reason"]), "updated_by": defaultUpdatedBy(payload),
	})
}

func defaultUpdatedBy(payload map[string]any) string {
	if value := strings.TrimSpace(anyTrimmed(payload["updated_by"])); value != "" {
		return value
	}
	return "webui"
}

func (d Deps) handleMemorySourcesList(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_source_admin", "list", map[string]any{})
}

func (d Deps) handleMemorySourceDelete(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	source, ok := bodyRequiredString(w, payload, "source")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_source_admin", "delete", map[string]any{"source": source})
}

func (d Deps) handleMemorySourceBatchDelete(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	raw, exists := payload["sources"]
	if !exists {
		writeValidationError(w, "sources 不能为空")
		return
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		writeValidationError(w, "sources 必须是非空字符串数组")
		return
	}
	sources := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(anyTrimmed(item)); text != "" {
			sources = append(sources, text)
		}
	}
	d.invokeMemoryAdmin(w, r, "memory_source_admin", "batch_delete", map[string]any{"sources": sources})
}

func (d Deps) handleMemoryEpisodesList(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 20, 1, 200)
	if !ok {
		return
	}
	payload := map[string]any{
		"query": mqString(r, "query"), "limit": limit, "source": mqString(r, "source"),
		"person_id": mqString(r, "person_id"), "platform": mqString(r, "platform"),
		"user_id": mqString(r, "user_id"),
	}
	if raw := mqString(r, "time_start"); raw != "" {
		payload["time_start"] = raw
	}
	if raw := mqString(r, "time_end"); raw != "" {
		payload["time_end"] = raw
	}
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "list", payload)
}

func (d Deps) handleMemoryEpisodesStatus(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 20, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "status", map[string]any{"limit": limit})
}

func (d Deps) handleMemoryEpisodeGet(w http.ResponseWriter, r *http.Request) {
	episodeID := strings.TrimSpace(chiURLParam(r, "episode_id"))
	if episodeID == "" {
		writeAPIError(w, http.StatusBadRequest, "episode_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "get", map[string]any{"episode_id": episodeID})
}

func (d Deps) handleMemoryEpisodeRebuild(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	all, _ := payload["all"].(bool)
	sources := []string{}
	if raw, exists := payload["sources"]; exists {
		if items, ok := raw.([]any); ok {
			for _, item := range items {
				if text := strings.TrimSpace(anyTrimmed(item)); text != "" {
					sources = append(sources, text)
				}
			}
		}
	}
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "rebuild", map[string]any{
		"source": anyTrimmed(payload["source"]), "sources": sources, "all": all,
	})
}

func (d Deps) handleMemoryEpisodeMigrationBackfill(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "discard_migration_backfill", map[string]any{"dry_run": true})
}

func (d Deps) handleMemoryEpisodeMigrationBackfillDiscard(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "discard_migration_backfill", map[string]any{"dry_run": false})
}

func (d Deps) handleMemoryEpisodeProcessPending(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	limit := 20
	if value, exists := payload["limit"]; exists && value != nil {
		parsed := anyInt(value)
		if parsed < 1 || parsed > 200 {
			writeValidationError(w, "limit 必须在 1 到 200 之间")
			return
		}
		limit = parsed
	}
	maxRetry := 3
	if value, exists := payload["max_retry"]; exists && value != nil {
		parsed := anyInt(value)
		if parsed < 1 || parsed > 20 {
			writeValidationError(w, "max_retry 必须在 1 到 20 之间")
			return
		}
		maxRetry = parsed
	}
	d.invokeMemoryAdmin(w, r, "memory_episode_admin", "process_sources", map[string]any{
		"limit": limit, "max_retry": maxRetry,
	})
}
