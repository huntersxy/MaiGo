package monitor

import (
	"fmt"
	"reflect"
)

// MaxPlannerBases 每条连接保留的 Planner 快照基准数，对齐 MAX_PLANNER_BASES
const MaxPlannerBases = 128

// PlannerEventTypes 需要增量编码的事件
var PlannerEventTypes = map[string]struct{}{
	"planner.progress":  {},
	"planner.finalized": {},
}

// PlannerIdentityFields 区分不同 Planner 快照的字段
var PlannerIdentityFields = []string{"session_id", "run_id", "cycle_id"}

// ProjectPlannerSnapshot 监控只传展示字段：请求去掉完整 messages，工具去掉 detail
func ProjectPlannerSnapshot(payload map[string]any) map[string]any {
	projected := make(map[string]any, len(payload))
	for key, value := range payload {
		projected[key] = value
	}
	if request, ok := payload["request"].(map[string]any); ok {
		trimmed := make(map[string]any, len(request))
		for key, value := range request {
			if key == "messages" {
				continue
			}
			trimmed[key] = value
		}
		projected["request"] = trimmed
	}
	if tools, ok := payload["tools"].([]any); ok {
		projected["tools"] = projectTools(tools)
	} else if tools, ok := payload["tools"].([]map[string]any); ok {
		entries := make([]any, 0, len(tools))
		for _, tool := range tools {
			entries = append(entries, tool)
		}
		projected["tools"] = projectTools(entries)
	}
	return projected
}

func projectTools(tools []any) []any {
	projected := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			projected = append(projected, NormalizePayloadValue(raw))
			continue
		}
		trimmed := make(map[string]any, len(tool))
		for key, value := range tool {
			if key == "detail" {
				continue
			}
			trimmed[key] = value
		}
		projected = append(projected, trimmed)
	}
	return projected
}

// PlannerDeltaEncoder 每条连接独立记录已发送的基准；只在串行发送协程中调用
type PlannerDeltaEncoder struct {
	bases map[string]map[string]any
	order []string
}

// NewPlannerDeltaEncoder 创建增量编码器
func NewPlannerDeltaEncoder() *PlannerDeltaEncoder {
	return &PlannerDeltaEncoder{bases: map[string]map[string]any{}}
}

// Encode 把快照编码成完整内容或与基准的差分
//
// 基准缺失或事件非法时返回错误：增量帧依赖上一帧，跳过会让前端永久失步。
func (e *PlannerDeltaEncoder) Encode(message map[string]any) (map[string]any, error) {
	if e == nil {
		return message, nil
	}
	if domain, _ := message["domain"].(string); domain != "" && domain != Domain {
		return message, nil
	}
	event, _ := message["event"].(string)
	if _, ok := PlannerEventTypes[event]; !ok {
		return message, nil
	}
	payload, _ := message["data"].(map[string]any)
	if payload == nil {
		return message, nil
	}
	projected := ProjectPlannerSnapshot(payload)
	encoded := make(map[string]any, len(message))
	for key, value := range message {
		encoded[key] = value
	}
	encoded["data"] = projected

	runID, _ := projected["run_id"].(string)
	eventID, ok := payloadInt64Value(projected["event_id"])
	if runID == "" || !ok || eventID <= 0 {
		return nil, fmt.Errorf("Planner 快照缺少有效的 run_id/event_id")
	}
	key := NormalizeKeyPart(projected["session_id"]) + "\x00" + runID + "\x00" + NormalizeKeyPart(projected["cycle_id"])
	previous := e.bases[key]
	if previous != nil {
		delta := map[string]any{
			"session_id":     projected["session_id"],
			"run_id":         runID,
			"cycle_id":       projected["cycle_id"],
			"base_event_id":  previous["event_id"],
			"event_type":     event,
			"changes":        plannerChanges(previous, projected, eventID),
			"removed_fields": plannerRemovedFields(previous, projected),
		}
		oldTools := toolList(previous["tools"])
		newTools := toolList(projected["tools"])
		if !reflect.DeepEqual(oldTools, newTools) {
			from := 0
			for from < len(oldTools) && from < len(newTools) && reflect.DeepEqual(oldTools[from], newTools[from]) {
				from++
			}
			delta["tools_from"] = from
			delta["tools"] = newTools[from:]
		}
		encoded["event"] = "planner.delta"
		encoded["data"] = delta
	}

	if event == "planner.finalized" {
		e.drop(key)
	} else {
		e.store(key, projected)
	}
	return encoded, nil
}

// Reset 清空全部基准，对齐前端收到 planner.reset 后的行为
func (e *PlannerDeltaEncoder) Reset() {
	if e == nil {
		return
	}
	e.bases = map[string]map[string]any{}
	e.order = nil
}

func plannerChanges(previous, current map[string]any, eventID int64) map[string]any {
	identity := make(map[string]struct{}, len(PlannerIdentityFields))
	for _, field := range PlannerIdentityFields {
		identity[field] = struct{}{}
	}
	changes := make(map[string]any, len(current))
	for key, value := range current {
		if _, skip := identity[key]; skip || key == "tools" {
			continue
		}
		if oldValue, exists := previous[key]; exists && reflect.DeepEqual(oldValue, value) {
			continue
		}
		changes[key] = value
	}
	// 即使重复回放同一个事件，也明确传递本次快照版本
	changes["event_id"] = eventID
	return changes
}

func plannerRemovedFields(previous, current map[string]any) []string {
	removed := make([]string, 0)
	for key := range previous {
		if _, exists := current[key]; !exists {
			removed = append(removed, key)
		}
	}
	return removed
}

func toolList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func (e *PlannerDeltaEncoder) store(key string, payload map[string]any) {
	if _, exists := e.bases[key]; !exists {
		e.order = append(e.order, key)
	}
	e.bases[key] = payload
	for len(e.bases) > MaxPlannerBases && len(e.order) > 0 {
		oldest := e.order[0]
		e.order = e.order[1:]
		delete(e.bases, oldest)
	}
}

func (e *PlannerDeltaEncoder) drop(key string) {
	if _, exists := e.bases[key]; !exists {
		return
	}
	delete(e.bases, key)
	for index, item := range e.order {
		if item == key {
			e.order = append(e.order[:index], e.order[index+1:]...)
			break
		}
	}
}

// NormalizeKeyPart 把标识字段拼成稳定键片段
func NormalizeKeyPart(value any) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprint(NormalizePayloadValue(value))
}

func payloadInt64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case float32:
		return int64(typed), true
	default:
		return 0, false
	}
}
