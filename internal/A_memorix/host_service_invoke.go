package memorix

// A_memorix 宿主服务调用面：对齐 host_service.py 的 _invoke/_disabled_response/
// _unavailable_response/_enqueue_startup_write/_dispatch_ingest_write 与 memory_service 的结果归一化

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	reasonInitializing = "a_memorix_initializing"
	reasonInitFailed   = "a_memorix_initialization_failed"
	reasonQueued       = "a_memorix_initializing_queued"
)

// Invoke 对齐 host_service.invoke；timeout_ms 语义与 Python 一致
func (s *AMemorixHostService) Invoke(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeoutMs == nil {
		return s.invoke(ctx, component, args)
	}
	if *timeoutMs <= 0 {
		return nil, errors.New("timeout_ms 必须是正整数")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(*timeoutMs)*time.Millisecond)
	defer cancel()

	result, err := s.invoke(timeoutCtx, component, args)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("A_Memorix 调用超时: component=%s, timeout_ms=%d", component, *timeoutMs)
	}
	return result, err
}

func (s *AMemorixHostService) invoke(ctx context.Context, component string, args map[string]any) (any, error) {
	payload := args
	if payload == nil {
		payload = map[string]any{}
	}
	if !s.IsEnabled() {
		return s.disabledResponse(component), nil
	}

	kernel, state := s.kernelSnapshot()
	if kernel == nil || state != hostStateReady {
		if state == hostStateStopped {
			if err := s.ensureStartupTask(ctx); err != nil {
				return nil, err
			}
		}
		return s.unavailableResponse(ctx, component, payload)
	}

	switch component {
	case "search_memory":
		return s.invokeSearchMemory(ctx, kernel, payload)
	case "enqueue_feedback_task":
		return kernel.EnqueueFeedbackTask(ctx, map[string]any{
			"query_tool_id":      payloadString(payload, "query_tool_id"),
			"session_id":         payloadString(payload, "session_id"),
			"query_timestamp":    payload["query_timestamp"],
			"structured_content": mapOrEmpty(payloadMap(payload, "structured_content")),
		})
	case "ingest_summary", "ingest_text":
		return s.dispatchIngestWrite(ctx, kernel, component, payload)
	case "get_person_profile":
		limit := payloadInt(payload, "limit", 10)
		if limit == 0 {
			limit = 10
		}
		if limit < 1 {
			limit = 1
		}
		return kernel.GetPersonProfile(ctx, payloadString(payload, "person_id"), payloadString(payload, "chat_id"), limit)
	case "maintain_memory":
		limit := payloadInt(payload, "limit", 50)
		if limit == 0 {
			limit = 50
		}
		if limit < 1 {
			limit = 1
		}
		return kernel.MaintainMemory(ctx, payloadString(payload, "action"), payloadString(payload, "target"), payload["hours"], payloadString(payload, "reason"), limit)
	case "memory_stats":
		return kernel.MemoryStats()
	case "image_memory":
		return s.invokeImageMemory(ctx, kernel, payload)
	}

	if !kernel.IsAdminComponent(component) {
		return nil, fmt.Errorf("不支持的 A_Memorix 调用: %s", component)
	}
	result, err := kernel.DispatchAdmin(ctx, component, payload)
	if err != nil {
		var responder interface{ ToResponse() map[string]any }
		if errors.As(err, &responder) {
			return responder.ToResponse(), nil
		}
		return nil, err
	}
	if component == "memory_runtime_admin" {
		if mapped, ok := result.(map[string]any); ok {
			return mergeStartupStatus(mapped, s.startupStatusPayload()), nil
		}
	}
	return result, nil
}

func (s *AMemorixHostService) invokeSearchMemory(ctx context.Context, kernel HostKernel, payload map[string]any) (any, error) {
	chatID := strings.TrimSpace(payloadString(payload, "chat_id"))
	config := s.readConfig()
	global := payloadBool(config, "global_memory_sharing_enabled", false)

	searchChatID := chatID
	var sharedChatIDs []string
	if global {
		searchChatID = ""
	} else {
		sharedChatIDs = s.sharedSessionIDs(chatID)
	}
	limit := payloadInt(payload, "limit", 5)
	if limit == 0 {
		limit = 5
	}
	mode := payloadString(payload, "mode")
	if mode == "" {
		mode = "search"
	}
	req := HostKernelSearchRequest{
		Query:         payloadString(payload, "query"),
		Limit:         limit,
		Mode:          mode,
		ChatID:        searchChatID,
		SharedChatIDs: sharedChatIDs,
		PersonID:      payloadString(payload, "person_id"),
		TimeStart:     payload["time_start"],
		TimeEnd:       payload["time_end"],
		RespectFilter: payloadBool(payload, "respect_filter", true),
		UserID:        payloadTrimmed(payload, "user_id"),
		GroupID:       payloadTrimmed(payload, "group_id"),
	}
	return kernel.SearchMemory(ctx, req)
}

func (s *AMemorixHostService) invokeImageMemory(ctx context.Context, kernel HostKernel, payload map[string]any) (any, error) {
	action := trimLower(payloadString(payload, "action"))
	scoped := cloneMap(payload)
	if action == "search" || action == "get" {
		if _, has := scoped["chat_ids"]; !has {
			chatID := strings.TrimSpace(payloadString(payload, "chat_id"))
			config := s.readConfig()
			if payloadBool(config, "global_memory_sharing_enabled", false) {
				scoped["chat_ids"] = nil
			} else {
				shared := s.sharedSessionIDs(chatID)
				chatIDs := make([]string, 0, 1+len(shared))
				if chatID != "" {
					chatIDs = append(chatIDs, chatID)
				}
				for _, item := range shared {
					if strings.TrimSpace(item) != "" {
						chatIDs = append(chatIDs, item)
					}
				}
				scoped["chat_ids"] = chatIDs
			}
		}
	}
	delete(scoped, "action")
	return kernel.ImageMemory(ctx, action, scoped)
}

func (s *AMemorixHostService) sharedSessionIDs(sessionID string) []string {
	cleaned := strings.TrimSpace(sessionID)
	if s.opts.SharedSessionIDs != nil {
		return s.opts.SharedSessionIDs(cleaned)
	}
	if cleaned == "" {
		return nil
	}
	return []string{cleaned}
}

func (s *AMemorixHostService) disabledResponse(component string) map[string]any {
	return disabledComponentResponse(component)
}

// disabledComponentResponse 对齐 Python _disabled_response：未启用时按组件返回可消费的空结果
func disabledComponentResponse(component string) map[string]any {
	reason := reasonDisabled
	message := "A_Memorix 未启用，请在长期记忆配置中开启后再使用。"
	switch component {
	case "search_memory":
		return map[string]any{
			"success": true, "disabled": true, "reason": reason,
			"summary": "", "hits": []any{}, "filtered": false,
		}
	case "ingest_summary", "ingest_text":
		return map[string]any{
			"success": true, "disabled": true, "reason": reason,
			"stored_ids": []any{}, "skipped_ids": []string{reason}, "detail": reason,
		}
	case "get_person_profile":
		return map[string]any{
			"success": true, "disabled": true, "reason": reason,
			"summary": "", "traits": []any{}, "evidence": []any{},
		}
	case "memory_stats":
		return map[string]any{
			"success": true, "enabled": false, "memory_enabled": false, "disabled": true,
			"reason": reason, "message": message,
			"paragraph_count": 0, "relation_count": 0, "episode_count": 0,
		}
	case "memory_runtime_admin":
		return map[string]any{
			"success": true, "enabled": false, "memory_enabled": false, "disabled": true,
			"reason": reason, "message": message,
			"runtime_ready": false, "retrieval_ready": false, "degraded": false,
			"retrieval_mode": "disabled", "available_channels": []any{}, "unavailable_channels": []any{},
			"vector_health":      map[string]any{"state": "disabled"},
			"embedding_degraded": false, "embedding_dimension": 0, "auto_save": false, "data_dir": "",
		}
	case "enqueue_feedback_task":
		return map[string]any{"success": true, "queued": false, "disabled": true, "reason": reason}
	default:
		return map[string]any{
			"success": false, "enabled": false, "disabled": true,
			"reason": reason, "error": message,
		}
	}
}

func (s *AMemorixHostService) unavailableResponse(ctx context.Context, component string, payload map[string]any) (any, error) {
	status := s.startupStatusPayload()
	initializing := payloadBool(status, "initializing", false)
	failed := payloadBool(status, "initialization_failed", false)
	action := trimLower(payloadString(payload, "action"))

	imageWrite := component == "image_memory" && (action == "ingest" || action == "describe")
	identityWrite := component == "memory_profile_admin" && action == "observe_identity"
	if initializing && (component == "ingest_summary" || component == "ingest_text" || imageWrite || identityWrite) {
		return s.enqueueStartupWrite(ctx, component, payload)
	}

	reason := reasonInitializing
	if failed {
		reason = reasonInitFailed
	}
	message := "A_Memorix 正在初始化"
	if failed {
		message = "A_Memorix 初始化失败: " + anyToString(status["error"])
	}

	success := component == "memory_runtime_admin" ||
		(!failed && (component == "search_memory" || component == "get_person_profile" || component == "memory_stats"))
	base := map[string]any{"success": success, "reason": reason, "message": message}
	for key, value := range status {
		base[key] = value
	}

	switch {
	case component == "search_memory":
		base["summary"] = ""
		base["hits"] = []any{}
		base["filtered"] = false
	case component == "get_person_profile":
		base["summary"] = ""
		base["traits"] = []any{}
		base["evidence"] = []any{}
	case component == "memory_stats":
		base["paragraph_count"] = 0
		base["relation_count"] = 0
		base["episode_count"] = 0
	case component == "image_memory":
		base["success"] = false
		base["hits"] = []any{}
		base["error"] = message
	case component == "memory_runtime_admin":
		// base 即完整响应
	case component == "ingest_summary" || component == "ingest_text" || imageWrite:
		base["success"] = false
		base["queued"] = false
		base["stored_ids"] = []any{}
		base["skipped_ids"] = []string{reason}
		base["detail"] = message
	case component == "enqueue_feedback_task":
		base["queued"] = false
	default:
		base["success"] = false
		base["error"] = message
	}
	return base, nil
}

func (s *AMemorixHostService) enqueueStartupWrite(ctx context.Context, component string, payload map[string]any) (any, error) {
	queued := cloneMap(payload)
	if raw, ok := queued["image_bytes"]; ok {
		delete(queued, "image_bytes")
		if raw != nil {
			imageBytes, ok := raw.([]byte)
			if !ok {
				return nil, errors.New("启动队列中的 image_bytes 必须是字节数据")
			}
			queued["image_base64"] = base64.StdEncoding.EncodeToString(imageBytes)
		}
	}

	record := map[string]any{
		"record_id":      newUUIDHex(),
		"component_name": component,
		"payload":        queued,
		"created_at":     nowUnixFloat(),
	}

	s.queueMu.Lock()
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	enqueued := state == hostStateStarting || state == hostStateMigrating
	if enqueued {
		if err := s.appendJSONLSync(s.startupQueuePath(), record); err != nil {
			s.queueMu.Unlock()
			return nil, err
		}
		s.queuePending++
	}
	s.queueMu.Unlock()

	if !enqueued {
		return s.invoke(ctx, component, payload)
	}
	return map[string]any{
		"success": true, "queued": true, "initializing": true, "reason": reasonQueued,
		"record_id": record["record_id"], "stored_ids": []any{}, "skipped_ids": []any{},
		"detail": "A_Memorix 正在初始化，写入已进入启动队列",
	}, nil
}

func (s *AMemorixHostService) dispatchIngestWrite(ctx context.Context, kernel HostKernel, component string, payload map[string]any) (any, error) {
	switch component {
	case "memory_profile_admin":
		action, err := kernel.ParseAdminCommand(component, payload)
		if err != nil {
			return nil, err
		}
		if action != "observe_identity" {
			return nil, fmt.Errorf("不支持的启动队列身份写入操作: %s", action)
		}
		return kernel.DispatchAdmin(ctx, component, payload)
	case "ingest_summary":
		return kernel.IngestSummary(ctx, HostIngestSummaryParams{
			ExternalID:    payloadString(payload, "external_id"),
			ChatID:        payloadString(payload, "chat_id"),
			Text:          payloadString(payload, "text"),
			Participants:  payloadStrings(payload, "participants"),
			TimeStart:     payload["time_start"],
			TimeEnd:       payload["time_end"],
			Tags:          payloadStrings(payload, "tags"),
			Metadata:      mapOrEmpty(payloadMap(payload, "metadata")),
			RespectFilter: payloadBool(payload, "respect_filter", true),
			UserID:        payloadTrimmed(payload, "user_id"),
			GroupID:       payloadTrimmed(payload, "group_id"),
		})
	case "ingest_text":
		return kernel.IngestText(ctx, HostIngestTextParams{
			ExternalID:    payloadString(payload, "external_id"),
			SourceType:    payloadString(payload, "source_type"),
			Text:          payloadString(payload, "text"),
			ChatID:        payloadString(payload, "chat_id"),
			PersonIDs:     payloadStrings(payload, "person_ids"),
			Participants:  payloadStrings(payload, "participants"),
			Timestamp:     payload["timestamp"],
			TimeStart:     payload["time_start"],
			TimeEnd:       payload["time_end"],
			Tags:          payloadStrings(payload, "tags"),
			Metadata:      mapOrEmpty(payloadMap(payload, "metadata")),
			Entities:      payloadStrings(payload, "entities"),
			Relations:     payloadRelations(payload, "relations"),
			RespectFilter: payloadBool(payload, "respect_filter", true),
			UserID:        payloadTrimmed(payload, "user_id"),
			GroupID:       payloadTrimmed(payload, "group_id"),
		})
	case "image_memory":
		action := trimLower(payloadString(payload, "action"))
		if action != "ingest" && action != "describe" {
			return nil, fmt.Errorf("不支持的启动队列图片写入操作: %s", action)
		}
		scoped := cloneMap(payload)
		delete(scoped, "action")
		return kernel.ImageMemory(ctx, action, scoped)
	default:
		return nil, fmt.Errorf("不支持的启动队列写入类型: %s", component)
	}
}

func coerceWriteResult(result any) IngestResult {
	payload := anyToMap(result)
	if payload == nil {
		return IngestResult{Success: false, Detail: "A_Memorix 写入返回非对象"}
	}
	detail := firstNonEmptyString(payload, "detail", "reason")
	storedIDs := anyToStringSlice(payload["stored_ids"])
	skippedIDs := anyToStringSlice(payload["skipped_ids"])

	var success bool
	if len(storedIDs) > 0 || len(skippedIDs) > 0 {
		success = true
	} else if _, ok := payload["success"]; ok {
		success = payloadBool(payload, "success", false)
	} else {
		success = detail == ""
	}
	return IngestResult{
		Success:    success,
		StoredIDs:  nonNilStrings(storedIDs),
		SkippedIDs: nonNilStrings(skippedIDs),
		Detail:     detail,
		Disabled:   payloadBool(payload, "disabled", false),
		Reason:     anyToString(payload["reason"]),
		Queued:     payloadBool(payload, "queued", false),
	}
}

func coerceSearchResult(result any) SearchResult {
	payload := anyToMap(result)
	if payload == nil {
		return SearchResult{Success: false, Error: "A_Memorix 搜索返回非对象"}
	}
	errorText := anyToString(payload["error"])
	success := errorText == ""
	if raw, ok := payload["success"]; ok && raw != nil {
		success = payloadBool(payload, "success", false)
	}
	out := SearchResult{
		Success:  success,
		Error:    errorText,
		Summary:  anyToString(payload["summary"]),
		Filtered: payloadBool(payload, "filtered", false),
		Disabled: payloadBool(payload, "disabled", false),
		Reason:   anyToString(payload["reason"]),
	}
	for _, item := range anySlice(payload["hits"]) {
		mapped := anyToMap(item)
		if mapped == nil {
			continue
		}
		hit := Hit{
			Content:   anyToString(mapped["content"]),
			Score:     toFloat(mapped["score"]),
			HitType:   anyToString(mapped["type"]),
			Source:    anyToString(mapped["source"]),
			Hash:      anyToString(mapped["hash"]),
			Metadata:  mapOrEmpty(anyToMap(mapped["metadata"])),
			EpisodeID: anyToString(mapped["episode_id"]),
			Title:     anyToString(mapped["title"]),
		}
		for _, key := range []string{"source_branches", "rank"} {
			if value, ok := mapped[key]; ok {
				if hit.Metadata == nil {
					hit.Metadata = map[string]any{}
				}
				hit.Metadata[key] = value
			}
		}
		out.Hits = append(out.Hits, hit)
	}
	if out.Hits == nil {
		out.Hits = []Hit{}
	}
	return out
}

func mapOrEmpty(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func mergeStartupStatus(result map[string]any, status map[string]any) map[string]any {
	merged := make(map[string]any, len(result)+len(status))
	for key, value := range result {
		merged[key] = value
	}
	// Python {**result, **status}：启动状态覆盖内核同名字段
	for key, value := range status {
		merged[key] = value
	}
	return merged
}
