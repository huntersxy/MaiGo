package webuiapi

// WebUI 人物画像、事实、反馈、运行时与配置文件路由

import (
	"fmt"
	"net/http"
	"strings"

	"maibot-go/internal/A_memorix"
	personinfo "maibot-go/internal/person_info"
)

func (d Deps) handleMemoryProfileQuery(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 12, 1, 100)
	if !ok {
		return
	}
	forceRefresh, ok := mqBool(w, r, "force_refresh", false)
	if !ok {
		return
	}
	// 未给 person_id 时按 platform + user_id 解析，对齐 resolve_person_id_for_memory
	personID := mqString(r, "person_id")
	platform := mqString(r, "platform")
	userID := mqString(r, "user_id")
	if personID == "" && platform != "" && userID != "" {
		personID = personinfo.DefaultRegistry().ResolvePersonID(r.Context(), platform, userID, "", false)
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "query", map[string]any{
		"person_id": personID, "person_keyword": mqString(r, "person_keyword"),
		"platform": platform, "user_id": userID,
		"limit": limit, "force_refresh": forceRefresh,
	})
}

func (d Deps) handleMemoryProfileList(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	result, err := callMemoryAdmin(r, "memory_profile_admin", "list", map[string]any{"limit": limit})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	payload, ok := result.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_profile_admin/list")
		return
	}
	writeJSON(w, http.StatusOK, d.decorateMemoryProfileIdentities(r.Context(), payload))
}

func (d Deps) handleMemoryProfileSearch(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	payload := map[string]any{
		"person_keyword": mqString(r, "person_keyword"), "limit": 200,
		"person_id": mqString(r, "person_id"),
	}
	if raw := mqString(r, "person_id"); raw != "" {
		payload["person_id"] = raw
	}
	result, err := callMemoryAdmin(r, "memory_profile_admin", "list", payload)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_profile_admin/list")
		return
	}
	items := filterProfileSearchItems(mapped, strings.ToLower(mqString(r, "person_keyword")), limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "items": items, "count": len(items), "limit": limit,
		"query": mqString(r, "person_keyword"),
	})
}

func filterProfileSearchItems(result map[string]any, keyword string, limit int) []map[string]any {
	rawItems, _ := result["items"].([]any)
	if rawItems == nil {
		if typed, ok := result["items"].([]map[string]any); ok {
			rawItems = make([]any, len(typed))
			for index, item := range typed {
				rawItems[index] = item
			}
		}
	}
	out := make([]map[string]any, 0)
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if keyword != "" && !profileSearchMatches(item, keyword) {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	if len(rawItems) == 0 {
		if items, ok := result["items"].([]map[string]any); ok {
			return items
		}
	}
	return out
}

func profileSearchMatches(item map[string]any, keyword string) bool {
	candidates := []string{"person_id", "person_name", "user_nickname", "profile_text", "source_note", "override_text"}
	for _, key := range candidates {
		if strings.Contains(strings.ToLower(anyTrimmed(item[key])), keyword) {
			return true
		}
	}
	for _, key := range []string{"group_cardname_list", "cardnames"} {
		if raw, ok := item[key].([]any); ok {
			for _, one := range raw {
				if strings.Contains(strings.ToLower(anyTrimmed(one)), keyword) {
					return true
				}
			}
		}
	}
	return false
}

func (d Deps) handleMemoryProfileSetOverride(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	personID, ok := bodyRequiredString(w, payload, "person_id")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "set_override", map[string]any{
		"person_id": personID, "override_text": anyTrimmed(payload["override_text"]),
		"updated_by": defaultStringField(payload, "updated_by", ""), "source": defaultStringField(payload, "source", "webui"),
	})
}

func defaultStringField(payload map[string]any, key, fallback string) string {
	if value := strings.TrimSpace(anyTrimmed(payload[key])); value != "" {
		return value
	}
	return fallback
}

func (d Deps) handleMemoryProfileDeleteOverride(w http.ResponseWriter, r *http.Request) {
	personID := strings.TrimSpace(chiURLParam(r, "person_id"))
	if personID == "" {
		writeAPIError(w, http.StatusBadRequest, "person_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "delete_override", map[string]any{"person_id": personID})
}

func (d Deps) handleMemoryProfileAliasesGet(w http.ResponseWriter, r *http.Request) {
	personID := strings.TrimSpace(chiURLParam(r, "person_id"))
	if personID == "" {
		writeAPIError(w, http.StatusBadRequest, "person_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "get_aliases", map[string]any{"person_id": personID})
}

func (d Deps) handleMemoryProfileAliasesSet(w http.ResponseWriter, r *http.Request) {
	personID := strings.TrimSpace(chiURLParam(r, "person_id"))
	if personID == "" {
		writeAPIError(w, http.StatusBadRequest, "person_id 不能为空")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	raw, exists := payload["aliases"]
	if !exists {
		writeValidationError(w, "aliases 不能为空")
		return
	}
	items, ok := raw.([]any)
	if !ok || len(items) < 1 || len(items) > 100 {
		writeValidationError(w, "aliases 必须包含 1 到 100 个字符串")
		return
	}
	aliases := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(anyTrimmed(item)); text != "" {
			aliases = append(aliases, text)
		}
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "set_aliases", map[string]any{
		"person_id": personID, "aliases": aliases,
		"updated_by": defaultStringField(payload, "updated_by", ""), "source": defaultStringField(payload, "source", "webui"),
	})
}

func (d Deps) handleMemoryProfileAliasesDelete(w http.ResponseWriter, r *http.Request) {
	personID := strings.TrimSpace(chiURLParam(r, "person_id"))
	if personID == "" {
		writeAPIError(w, http.StatusBadRequest, "person_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "delete_aliases", map[string]any{"person_id": personID})
}

func (d Deps) handleMemoryProfileEvidence(w http.ResponseWriter, r *http.Request) {
	personID := strings.TrimSpace(chiURLParam(r, "person_id"))
	if personID == "" {
		writeAPIError(w, http.StatusBadRequest, "person_id 不能为空")
		return
	}
	limit, ok := mqInt(w, r, "limit", 12, 1, 100)
	if !ok {
		return
	}
	forceRefresh, ok := mqBool(w, r, "force_refresh", false)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "evidence", map[string]any{
		"person_id": personID, "limit": limit, "force_refresh": forceRefresh,
	})
}

func (d Deps) handleMemoryProfileEvidenceCorrect(w http.ResponseWriter, r *http.Request) {
	personID := strings.TrimSpace(chiURLParam(r, "person_id"))
	if personID == "" {
		writeAPIError(w, http.StatusBadRequest, "person_id 不能为空")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	evidenceType, ok := bodyRequiredString(w, payload, "evidence_type")
	if !ok {
		return
	}
	hash, ok := bodyRequiredString(w, payload, "hash")
	if !ok {
		return
	}
	refresh := true
	if value, exists := payload["refresh"]; exists && value != nil {
		if typed, ok := value.(bool); ok {
			refresh = typed
		}
	}
	limit := 12
	if value, exists := payload["limit"]; exists && value != nil {
		parsed := anyInt(value)
		if parsed < 1 || parsed > 100 {
			writeValidationError(w, "limit 必须在 1 到 100 之间")
			return
		}
		limit = parsed
	}
	d.invokeMemoryAdmin(w, r, "memory_profile_admin", "correct_evidence", map[string]any{
		"person_id": personID, "evidence_type": evidenceType, "hash": hash,
		"requested_by": defaultStringField(payload, "requested_by", "webui"),
		"reason":       defaultStringField(payload, "reason", "profile_evidence_correction"),
		"refresh":      refresh, "limit": limit,
	})
}

func (d Deps) handleMemoryFactsList(w http.ResponseWriter, r *http.Request) {
	scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type"))
	if scopeType == "" {
		scopeType = "person"
	}
	scopeID, ok := mqRequiredString(w, r, "scope_id")
	if !ok {
		return
	}
	statuses := mqCSV(r.URL.Query().Get("statuses"))
	if len(statuses) == 0 {
		statuses = []string{"active"}
	}
	limit, ok := mqInt(w, r, "limit", 200, 1, 500)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_fact_admin", "list", map[string]any{
		"scope_type": scopeType, "scope_id": scopeID, "statuses": statuses, "limit": limit,
	})
}

func (d Deps) handleMemoryFactGet(w http.ResponseWriter, r *http.Request) {
	claimID := strings.TrimSpace(chiURLParam(r, "claim_id"))
	if claimID == "" {
		writeAPIError(w, http.StatusBadRequest, "claim_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_fact_admin", "get", map[string]any{"claim_id": claimID})
}

func (d Deps) handleMemoryFactCreate(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	scopeID, ok := bodyRequiredString(w, payload, "scope_id")
	if !ok {
		return
	}
	factKey, ok := bodyRequiredString(w, payload, "fact_key")
	if !ok {
		return
	}
	valueText, ok := bodyRequiredString(w, payload, "value_text")
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
	args := map[string]any{
		"scope_type": defaultStringField(payload, "scope_type", "person"), "scope_id": scopeID,
		"fact_key": factKey, "value_text": valueText,
		"polarity":        defaultStringField(payload, "polarity", "positive"),
		"cardinality":     defaultStringField(payload, "cardinality", "set"),
		"stability":       defaultStringField(payload, "stability", "stable"),
		"profile_section": defaultStringField(payload, "profile_section", "stable_facts"),
		"authority":       defaultStringField(payload, "authority", "manual"),
		"confidence":      confidence,
		"reason":          defaultStringField(payload, "reason", "webui_fact_create"),
		"updated_by":      defaultStringField(payload, "updated_by", "webui"),
	}
	for _, key := range []string{"valid_from", "valid_to"} {
		if value, exists := payload[key]; exists {
			args[key] = value
		}
	}
	d.invokeMemoryAdmin(w, r, "memory_fact_admin", "create", args)
}

func (d Deps) handleMemoryFactUpdate(w http.ResponseWriter, r *http.Request) {
	claimID := strings.TrimSpace(chiURLParam(r, "claim_id"))
	if claimID == "" {
		writeAPIError(w, http.StatusBadRequest, "claim_id 不能为空")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	args := map[string]any{
		"claim_id":   claimID,
		"reason":     defaultStringField(payload, "reason", "webui_fact_update"),
		"updated_by": defaultStringField(payload, "updated_by", "webui"),
	}
	allowed := []string{"fact_key", "value_text", "polarity", "cardinality", "stability",
		"profile_section", "authority", "confidence", "valid_from", "valid_to"}
	for _, key := range allowed {
		if value, exists := payload[key]; exists {
			args[key] = value
		}
	}
	if value, exists := args["confidence"]; exists && value != nil {
		parsed, ok := anyFloat(value)
		if !ok || parsed < 0 || parsed > 1 {
			writeValidationError(w, "confidence 必须在 0 到 1 之间")
			return
		}
	}
	d.invokeMemoryAdmin(w, r, "memory_fact_admin", "update", args)
}

func (d Deps) handleMemoryFactStatus(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claimID := strings.TrimSpace(chiURLParam(r, "claim_id"))
		if claimID == "" {
			writeAPIError(w, http.StatusBadRequest, "claim_id 不能为空")
			return
		}
		payload, ok := decodeMemoryJSON(w, r)
		if !ok {
			return
		}
		d.invokeMemoryAdmin(w, r, "memory_fact_admin", action, map[string]any{
			"claim_id": claimID, "reason": anyTrimmed(payload["reason"]),
			"requested_by": defaultStringField(payload, "requested_by", "webui"),
		})
	}
}

func (d Deps) handleMemoryFeedbackList(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_feedback_admin", "list", map[string]any{
		"limit": limit, "statuses": mqCSV(r.URL.Query().Get("status")),
		"rollback_statuses": mqCSV(r.URL.Query().Get("rollback_status")),
		"query":             mqString(r, "query"),
	})
}

func (d Deps) handleMemoryFeedbackGet(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_feedback_admin", "get", map[string]any{"task_id": taskID})
}

func (d Deps) handleMemoryFeedbackRollback(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_feedback_admin", "rollback", map[string]any{
		"task_id": taskID, "requested_by": defaultStringField(payload, "requested_by", "webui"),
		"reason": anyTrimmed(payload["reason"]),
	})
}

func (d Deps) handleMemoryRuntimeSave(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_runtime_admin", "save", map[string]any{})
}

func (d Deps) handleMemoryRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	result, err := callMemoryAdmin(r, "memory_runtime_admin", "get_config", map[string]any{})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_runtime_admin/get_config")
		return
	}
	if nested, ok := mapped["config"].(map[string]any); ok {
		if integration, ok := nested["integration"].(map[string]any); ok {
			if value, exists := integration["fuzzy_modify_candidate_limit"]; exists {
				mapped["fuzzy_modify_candidate_limit"] = value
			}
		}
	}
	writeJSON(w, http.StatusOK, mapped)
}

func (d Deps) handleMemoryRuntimeSelfCheck(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_runtime_admin", "self_check", map[string]any{})
}

func (d Deps) handleMemoryRuntimeRefreshSelfCheck(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_runtime_admin", "refresh_self_check", map[string]any{})
}

func (d Deps) handleMemoryRuntimeAutoSaveGet(w http.ResponseWriter, r *http.Request) {
	result, err := callMemoryAdmin(r, "memory_runtime_admin", "get_config", map[string]any{})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_runtime_admin/get_config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "auto_save": mapped["auto_save"]})
}

func (d Deps) handleMemoryRuntimeAutoSaveSet(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	raw, exists := payload["enabled"]
	if !exists {
		writeValidationError(w, "enabled 不能为空")
		return
	}
	enabled, ok := raw.(bool)
	if !ok {
		writeValidationError(w, "enabled 必须是布尔值")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_runtime_admin", "set_auto_save", map[string]any{"enabled": enabled})
}

func (d Deps) handleMemoryRuntimeRebuildVectors(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	dryRun := false
	if value, exists := payload["dry_run"]; exists && value != nil {
		dryRun, _ = value.(bool)
	}
	batchSize := 32
	if value, exists := payload["batch_size"]; exists && value != nil {
		parsed := anyInt(value)
		if parsed < 1 || parsed > 512 {
			writeValidationError(w, "batch_size 必须在 1 到 512 之间")
			return
		}
		batchSize = parsed
	}
	args := map[string]any{"dry_run": dryRun, "batch_size": batchSize}
	if value, exists := payload["include_relations"]; exists {
		args["include_relations"] = value
	}
	timeout := 600000
	d.invokeMemoryAdminTimeout(w, r, "memory_runtime_admin", "rebuild_all_vectors", args, &timeout)
}

func (d Deps) handleMemoryVectorSpacesList(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_runtime_admin", "list_vector_spaces", map[string]any{})
}

func (d Deps) handleMemoryVectorSpaceDelete(w http.ResponseWriter, r *http.Request) {
	spaceID := strings.TrimSpace(chiURLParam(r, "space_id"))
	if spaceID == "" {
		writeValidationError(w, "space_id 不能为空")
		return
	}
	result, err := callMemoryAdmin(r, "memory_runtime_admin", "delete_vector_space", map[string]any{"space_id": spaceID})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if result == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_runtime_admin/delete_vector_space")
		return
	}
	payload, _ := result.(map[string]any)
	if payload == nil {
		writeInternalError(w, fmt.Errorf("删除向量库返回非法响应"))
		return
	}
	if !anyBoolInt(payload["success"]) {
		detail := firstNonEmptyText(anyTrimmed(payload["error"]), firstNonEmptyText(anyTrimmed(payload["detail"]), "删除向量库失败"))
		status := http.StatusInternalServerError
		switch detail {
		case "vector_space_in_use":
			status = http.StatusConflict
		case "vector_space_not_found":
			status = http.StatusNotFound
		}
		writeAPIError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (d Deps) handleMemoryConfigSchema(w http.ResponseWriter, r *http.Request) {
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	schema, err := service.GetConfigSchema()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "schema": schema, "path": service.GetConfigPath(),
	})
}

func memoryHostService() *memorix.AMemorixHostService {
	host := memorixDefaultHost()
	if service, ok := host.(*memorix.AMemorixHostService); ok {
		return service
	}
	return nil
}

func (d Deps) handleMemoryConfigGet(w http.ResponseWriter, r *http.Request) {
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "config": service.GetConfig(), "path": service.GetConfigPath(),
	})
}

func (d Deps) handleMemoryConfigGetRaw(w http.ResponseWriter, r *http.Request) {
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	meta := service.GetRawConfigWithMeta()
	meta["success"] = true
	meta["path"] = service.GetConfigPath()
	writeJSON(w, http.StatusOK, meta)
}

func (d Deps) handleMemoryConfigUpdate(w http.ResponseWriter, r *http.Request) {
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	config := map[string]any{}
	if rawConfig, exists := payload["config"]; exists && rawConfig != nil {
		parsed, ok := rawConfig.(map[string]any)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "配置数据验证失败: config 必须是对象")
			return
		}
		config = parsed
	}
	result, err := service.UpdateConfig(r.Context(), config)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "配置数据验证失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d Deps) handleMemoryConfigUpdateRaw(w http.ResponseWriter, r *http.Request) {
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	text := ""
	if rawConfig, exists := payload["config"]; exists && rawConfig != nil {
		parsed, ok := rawConfig.(string)
		if !ok {
			writeValidationError(w, "config 必须是 TOML 字符串")
			return
		}
		text = parsed
	}
	result, err := service.UpdateRawConfig(r.Context(), text)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "TOML 格式错误: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// callMemoryAdmin 供需要读取结果再加工的处理器使用
func callMemoryAdmin(r *http.Request, component, action string, payload map[string]any) (any, error) {
	return memorixHostCaller().invoke(r, component, action, payload)
}
