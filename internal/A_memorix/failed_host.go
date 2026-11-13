package memorix

import (
	"context"
	"strings"
)

// FailedHost 对等 Python A_Memorix 已启用但内核启动失败时的宿主行为
//
// Python 依据：src/A_memorix/host_service.py:528-552 生成 startup status（failed +
// initialization_failed + error），:587-632 _unavailable_response 生成各 component 响应。
// 反直觉语义：ingest 的 skipped_ids 非空会让 memory_service._coerce_write_result
// （src/services/memory_service.py:137-155）强制 success=true；这是 Python 现状，
// 不是 Go 侧伪造成功，禁止改成 success=false。
type FailedHost struct {
	startupError string
	status       map[string]any
}

// NewFailedHost 创建初始化失败 Host；status 可传宿主服务已有的启动状态快照
func NewFailedHost(startupError string, status map[string]any) FailedHost {
	return FailedHost{
		startupError: startupError,
		status:       failedHostStatus(startupError, status),
	}
}

// failedHostStatus 对齐 _startup_status_payload 的 failed 快照
func failedHostStatus(startupError string, status map[string]any) map[string]any {
	out := make(map[string]any, len(status)+24)
	for key, value := range status {
		out[key] = value
	}
	out["enabled"] = true
	out["memory_enabled"] = true
	out["runtime_ready"] = false
	out["retrieval_ready"] = false
	out["degraded"] = false
	if _, ok := out["degraded_reasons"]; !ok {
		out["degraded_reasons"] = []any{}
	}
	if _, ok := out["missing_services"]; !ok {
		out["missing_services"] = []any{}
	}
	if _, ok := out["missing_required_services"]; !ok {
		out["missing_required_services"] = []any{}
	}
	out["retrieval_mode"] = "unavailable"
	out["available_channels"] = []any{}
	out["unavailable_channels"] = []any{}
	if vectorHealth := anyToMap(out["vector_health"]); vectorHealth != nil {
		out["vector_health"] = vectorHealth
	} else {
		out["vector_health"] = map[string]any{}
	}
	out["startup_state"] = "failed"
	out["initializing"] = false
	out["initialization_failed"] = true
	if _, ok := out["error_stage"]; !ok {
		out["error_stage"] = ""
	}
	out["error"] = startupError
	if _, ok := out["startup_started_at"]; !ok {
		out["startup_started_at"] = nil
	}
	if _, ok := out["startup_finished_at"]; !ok {
		out["startup_finished_at"] = nil
	}
	out["startup_queue_pending"] = payloadInt(out, "startup_queue_pending", 0)
	out["data_dir"] = anyToString(out["data_dir"])
	return out
}

func (h FailedHost) failureMessage() string {
	return "A_Memorix 初始化失败: " + h.startupError
}

// response 对齐 _unavailable_response 在 initialization_failed=true 时的分支
func (h FailedHost) response(component string) map[string]any {
	status := make(map[string]any, len(h.status))
	for key, value := range h.status {
		status[key] = value
	}
	base := mergeStartupStatus(map[string]any{
		"success": component == "memory_runtime_admin",
		"reason":  reasonInitFailed,
		"message": h.failureMessage(),
	}, status)
	switch component {
	case "search_memory":
		base["summary"] = ""
		base["hits"] = []any{}
		base["filtered"] = false
	case "ingest_summary", "ingest_text":
		base["success"] = false
		base["queued"] = false
		base["stored_ids"] = []any{}
		base["skipped_ids"] = []string{reasonInitFailed}
		base["detail"] = h.failureMessage()
	case "enqueue_feedback_task":
		base["queued"] = false
	default:
		// episode/source/person/profile 等管理接口：success=false + error
		base["success"] = false
		base["error"] = h.failureMessage()
	}
	return base
}

func (h FailedHost) IngestMessage(context.Context, IngestMessageRequest) (IngestResult, error) {
	return coerceWriteResult(h.response("ingest_text")), nil
}

func (h FailedHost) Search(_ context.Context, req SearchRequest) (SearchResult, error) {
	// 对齐 memory_service.search：空 query 且无时间范围时不触发内核，直接返回空成功
	if strings.TrimSpace(req.Query) == "" && isEmptyTimeValue(req.TimeStart) && isEmptyTimeValue(req.TimeEnd) {
		return SearchResult{Success: true, Hits: []Hit{}}, nil
	}
	return coerceSearchResult(h.response("search_memory")), nil
}

func (h FailedHost) Read(context.Context, ReadRequest) (ReadResult, error) {
	payload := h.response("memory_episode_admin")
	return ReadResult{
		Success:  false,
		Error:    anyToString(payload["error"]),
		Reason:   anyToString(payload["reason"]),
		Metadata: payload,
	}, nil
}

func (h FailedHost) AdminList(context.Context, AdminListRequest) (AdminListResult, error) {
	payload := h.response("memory_source_admin")
	return AdminListResult{
		Success: false,
		Error:   anyToString(payload["error"]),
		Reason:  anyToString(payload["reason"]),
	}, nil
}

func (h FailedHost) Health(context.Context) (HealthResult, error) {
	return healthFromStartupStatus(h.status), nil
}
