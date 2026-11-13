package memorix

// A_memorix 宿主服务 Host 门面与结果归一化
// 对照 memory_service.py 的 search/ingest_text/read/admin_list/health 映射

import (
	"context"
	"strings"
	"time"
)

var _ Host = (*AMemorixHostService)(nil)

// IngestMessage 映射 memory_service.ingest_text
func (s *AMemorixHostService) IngestMessage(ctx context.Context, req IngestMessageRequest) (IngestResult, error) {
	payload := buildIngestTextPayload(req)
	result, err := s.Invoke(ctx, "ingest_text", payload, nil)
	if err != nil {
		return IngestResult{}, err
	}
	return coerceWriteResult(result), nil
}

// Search 映射 memory_service.search
func (s *AMemorixHostService) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" && isEmptyTimeValue(req.TimeStart) && isEmptyTimeValue(req.TimeEnd) {
		return SearchResult{Success: true, Hits: []Hit{}}, nil
	}
	limit := req.Limit
	if limit < 1 {
		limit = 1
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "search"
	}
	payload := map[string]any{
		"query":          query,
		"limit":          limit,
		"mode":           mode,
		"chat_id":        req.ChatID,
		"person_id":      req.PersonID,
		"time_start":     req.TimeStart,
		"time_end":       req.TimeEnd,
		"respect_filter": req.RespectFilter,
		"user_id":        req.UserID,
		"group_id":       req.GroupID,
	}
	result, err := s.Invoke(ctx, "search_memory", payload, nil)
	if err != nil {
		return SearchResult{Success: false, Error: err.Error()}, nil
	}
	return coerceSearchResult(result), nil
}

// Read 读取单条记忆；A_memorix 侧只支持 episode_id，其余显式报错不伪造
func (s *AMemorixHostService) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	episodeID := strings.TrimSpace(req.EpisodeID)
	if episodeID == "" {
		if req.ID != 0 {
			return ReadResult{Success: false, Error: "A_Memorix 宿主服务仅支持 episode_id 读取；item_id 读取请走 Bridge"}, nil
		}
		return ReadResult{Success: false, Error: "episode_id 不能为空"}, nil
	}
	result, err := s.Invoke(ctx, "memory_episode_admin", map[string]any{
		"action":     "get",
		"episode_id": episodeID,
	}, nil)
	if err != nil {
		return ReadResult{Success: false, Error: err.Error()}, nil
	}
	payload, ok := result.(map[string]any)
	if !ok {
		return ReadResult{Success: false, Error: "A_Memorix episode_admin 返回非对象"}, nil
	}
	read := ReadResult{
		Success:  payloadBool(payload, "success", true),
		Error:    anyToString(payload["error"]),
		Disabled: payloadBool(payload, "disabled", false),
		Reason:   anyToString(payload["reason"]),
		Content:  firstNonEmptyString(payload, "content", "text", "summary"),
		Label:    firstNonEmptyString(payload, "label", "title"),
		Metadata: payload,
	}
	return read, nil
}

// AdminList 映射 memory_<domain>_admin 的 list 动作
func (s *AMemorixHostService) AdminList(ctx context.Context, req AdminListRequest) (AdminListResult, error) {
	domain := trimLower(req.Domain)
	if domain == "" {
		return AdminListResult{Success: false, Error: "domain 不能为空"}, nil
	}
	component := adminComponentName(domain)
	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "list"
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	payload := map[string]any{"action": action, "limit": limit}
	if strings.TrimSpace(req.Cursor) != "" {
		payload["cursor"] = req.Cursor
	}
	if strings.TrimSpace(req.Status) != "" {
		payload["status"] = req.Status
	}
	result, err := s.Invoke(ctx, component, payload, nil)
	if err != nil {
		return AdminListResult{Success: false, Error: err.Error()}, nil
	}
	raw, ok := result.(map[string]any)
	if !ok {
		return AdminListResult{Success: false, Error: "A_Memorix 管理列表返回非对象"}, nil
	}
	out := AdminListResult{
		Success:    payloadBool(raw, "success", true),
		Error:      anyToString(raw["error"]),
		Disabled:   payloadBool(raw, "disabled", false),
		Reason:     anyToString(raw["reason"]),
		NextCursor: anyToString(raw["next_cursor"]),
		HasMore:    payloadBool(raw, "has_more", false),
	}
	for _, item := range anySlice(raw["items"]) {
		mapped := anyToMap(item)
		if mapped == nil {
			continue
		}
		out.Items = append(out.Items, adminListItemFromMap(mapped))
	}
	return out, nil
}

// Health 对齐 memory_runtime_admin + _startup_status_payload
func (s *AMemorixHostService) Health(ctx context.Context) (HealthResult, error) {
	if !s.IsEnabled() {
		return disabledHealth(), nil
	}
	return healthFromStartupStatus(s.startupStatusPayload()), nil
}

// healthFromStartupStatus 把启动状态快照映射为健康结果，供宿主服务与 FailedHost 共用
func healthFromStartupStatus(status map[string]any) HealthResult {
	result := HealthResult{
		Success:              true,
		Enabled:              payloadBool(status, "enabled", true),
		MemoryEnabled:        payloadBool(status, "memory_enabled", true),
		RuntimeReady:         payloadBool(status, "runtime_ready", false),
		RetrievalReady:       payloadBool(status, "retrieval_ready", false),
		Degraded:             payloadBool(status, "degraded", false),
		DegradedReasons:      anyToStringSliceOrEmpty(status["degraded_reasons"]),
		MissingServices:      anyToStringSliceOrEmpty(status["missing_services"]),
		RetrievalMode:        anyToString(status["retrieval_mode"]),
		AvailableChannels:    anyToStringSliceOrEmpty(status["available_channels"]),
		UnavailableChannels:  anyToStringSliceOrEmpty(status["unavailable_channels"]),
		VectorHealth:         anyToMap(status["vector_health"]),
		Backend:              "memorix",
		StartupState:         anyToString(status["startup_state"]),
		Initializing:         payloadBool(status, "initializing", false),
		InitializationFailed: payloadBool(status, "initialization_failed", false),
		Error:                anyToString(status["error"]),
		ErrorStage:           anyToString(status["error_stage"]),
		StartupStartedAt:     status["startup_started_at"],
		StartupFinishedAt:    status["startup_finished_at"],
		StartupQueuePending:  payloadInt(status, "startup_queue_pending", 0),
		DataDir:              anyToString(status["data_dir"]),
	}
	if result.RetrievalMode == "" {
		result.RetrievalMode = "unavailable"
	}
	if result.VectorHealth == nil {
		result.VectorHealth = map[string]any{}
	}
	if result.InitializationFailed {
		result.Message = "A_Memorix 初始化失败: " + anyToString(status["error"])
	} else if result.Initializing {
		result.Message = "A_Memorix 正在初始化"
	}
	return result
}

func buildIngestTextPayload(req IngestMessageRequest) map[string]any {
	payload := map[string]any{
		"external_id":    req.ExternalID,
		"source_type":    req.SourceType,
		"text":           req.Text,
		"chat_id":        req.ChatID,
		"person_ids":     req.PersonIDs,
		"participants":   req.Participants,
		"tags":           req.Tags,
		"metadata":       req.Metadata,
		"timestamp":      nil,
		"time_start":     nil,
		"time_end":       nil,
		"respect_filter": req.RespectFilter,
		"user_id":        req.UserID,
		"group_id":       req.GroupID,
	}
	if req.Timestamp != nil {
		payload["timestamp"] = timeToUnixFloat(req.Timestamp)
	}
	if req.TimeStart != nil {
		payload["time_start"] = timeToUnixFloat(req.TimeStart)
	}
	if req.TimeEnd != nil {
		payload["time_end"] = timeToUnixFloat(req.TimeEnd)
	}
	return payload
}

func timeToUnixFloat(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return float64(value.UnixNano()) / float64(time.Second)
}

func isEmptyTimeValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func firstNonEmptyString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(anyToString(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

func adminComponentName(domain string) string {
	if strings.HasPrefix(domain, "memory_") {
		return domain
	}
	return "memory_" + domain + "_admin"
}

func anySlice(value any) []any {
	switch items := value.(type) {
	case []any:
		return items
	case []map[string]any:
		out := make([]any, len(items))
		for index, item := range items {
			out[index] = item
		}
		return out
	default:
		return nil
	}
}

func adminListItemFromMap(payload map[string]any) AdminListItem {
	item := AdminListItem{
		ID:            uint(payloadInt(payload, "id", 0)),
		SubjectType:   anyToString(payload["subject_type"]),
		SubjectTarget: anyToString(payload["subject_target"]),
		MemoryType:    anyToString(payload["memory_type"]),
		Label:         anyToString(payload["label"]),
		Preview:       anyToString(payload["preview"]),
		Status:        anyToString(payload["status"]),
		EpisodeID:     anyToString(payload["episode_id"]),
		EpisodeKind:   anyToString(payload["episode_kind"]),
		HonestNote:    anyToString(payload["honest_note"]),
	}
	if updated := parseAdminTime(payload["updated_at"]); !updated.IsZero() {
		item.UpdatedAt = updated
	}
	return item
}

func parseAdminTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed
	case string:
		text := strings.TrimSpace(typed)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed
			}
		}
		return time.Time{}
	default:
		if seconds := recordTimeValue(value); seconds > 0 {
			return time.Unix(int64(seconds), 0)
		}
		return time.Time{}
	}
}
