package monitor

import "time"

// PlannerRequest 请求区块输入，对齐 _serialize_request_block 的参数
type PlannerRequest struct {
	Messages             []map[string]any
	SelectedHistoryCount int
	ToolCount            int
	ContextSections      []map[string]any
}

// PlannerSnapshotInput 一次 Planner 快照的全部输入
type PlannerSnapshotInput struct {
	SessionID        string
	RunID            string
	CycleID          int
	ScopeID          string
	SessionIDs       []string
	Request          *PlannerRequest
	Content          string
	Reasoning        string
	ModelName        string
	ToolCalls        []map[string]any
	NativeToolCalls  []map[string]any
	PromptTokens     int
	CompletionTokens int
	PromptCacheHit   int
	PromptCacheMiss  int
	DurationMS       float64
	PromptHTMLURI    string
	StartedAt        *float64
	EndedAt          *float64
	Tools            []map[string]any
	TimeRecords      map[string]any
	AgentState       string
	EndReason        string
	EndDetail        string
	ActiveToolCallID string
	Interrupted      bool
}

// BuildPlannerSnapshotPayload 构造 emit_planner_snapshot 的载荷
//
// 顶层会话/轮次标识 + request/planner/tools/final_state 四个区块；progress 不携带
// 请求区块与最终状态，与 Python 传 None 的效果一致。
func BuildPlannerSnapshotPayload(in PlannerSnapshotInput, finalized bool) map[string]any {
	payload := map[string]any{
		"session_id":          in.SessionID,
		"cycle_id":            in.CycleID,
		"run_id":              in.RunID,
		"planner_started_at":  in.StartedAt,
		"planner_ended_at":    in.EndedAt,
		"timestamp":           NowSeconds(),
		"request":             nil,
		"planner":             nil,
		"tools":               SerializeToolResults(in.Tools),
		"active_tool_call_id": in.ActiveToolCallID,
		"interrupted":         in.Interrupted,
		"final_state": map[string]any{
			"time_records": normalizeTimeRecords(in.TimeRecords),
			"agent_state":  "",
			"end_reason":   "",
			"end_detail":   "",
		},
	}
	if in.ScopeID != "" {
		payload["focus_scope_id"] = in.ScopeID
		payload["focus_session_ids"] = in.SessionIDs
	}
	payload["planner"] = map[string]any{
		"content":                  in.Content,
		"reasoning":                in.Reasoning,
		"model_name":               in.ModelName,
		"tool_calls":               SerializeToolCallObjects(in.ToolCalls),
		"native_tool_calls":        SerializeNativeToolCalls(in.NativeToolCalls),
		"prompt_tokens":            in.PromptTokens,
		"completion_tokens":        in.CompletionTokens,
		"total_tokens":             in.PromptTokens + in.CompletionTokens,
		"prompt_cache_hit_tokens":  in.PromptCacheHit,
		"prompt_cache_miss_tokens": in.PromptCacheMiss,
		"duration_ms":              in.DurationMS,
		"prompt_html_uri":          in.PromptHTMLURI,
	}
	if finalized {
		payload["final_state"] = map[string]any{
			"time_records": normalizeTimeRecords(in.TimeRecords),
			"agent_state":  in.AgentState,
			"end_reason":   in.EndReason,
			"end_detail":   in.EndDetail,
		}
		if in.Request != nil {
			payload["request"] = map[string]any{
				"messages":               SerializeMessages(in.Request.Messages),
				"selected_history_count": in.Request.SelectedHistoryCount,
				"tool_count":             in.Request.ToolCount,
				"context_sections":       in.Request.ContextSections,
			}
			if payload["request"].(map[string]any)["context_sections"] == nil {
				payload["request"].(map[string]any)["context_sections"] = []map[string]any{}
			}
		}
	}
	return payload
}

func normalizeTimeRecords(records map[string]any) map[string]any {
	if records == nil {
		return map[string]any{}
	}
	normalized := make(map[string]any, len(records))
	for key, value := range records {
		normalized[key] = NormalizePayloadValue(value)
	}
	return normalized
}

// EmitPlannerProgress 广播 planner.progress 快照
func EmitPlannerProgress(payload map[string]any) {
	Publish(EventPlannerProgress, payload)
}

// EmitPlannerFinalized 广播 planner.finalized 快照
func EmitPlannerFinalized(payload map[string]any) {
	Publish(EventPlannerFinalized, payload)
}

// Seconds 把时间转成 Unix 秒，零值返回 nil
func Seconds(value time.Time) *float64 {
	if value.IsZero() {
		return nil
	}
	seconds := float64(value.UnixNano()) / 1e9
	return &seconds
}
