package storage

// A_memorix 反馈任务/回滚日志/陈旧关系标记的行映射与取值归一
// 对齐 Python metadata_feedback.py 的 _feedback_task_row_to_dict、
// _feedback_action_log_row_to_dict 与 _paragraph_stale_relation_mark_row_to_dict
// Python 侧依赖 sqlite3.Row 的 dict(row)，Go 侧以"列名 → 值"字典承载

import (
	"strconv"
	"strings"
)

// feedbackQueryDictRows 执行查询并把每行转成列名字典，对齐 Python dict(row)
func feedbackQueryDictRows(c *Conn, query string, args ...any) ([]map[string]any, error) {
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, 8)
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		record := make(map[string]any, len(columns))
		for i, name := range columns {
			record[name] = feedbackNormalizeValue(values[i])
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// feedbackQueryDictRow 返回首行字典，无结果返回 nil
func feedbackQueryDictRow(c *Conn, query string, args ...any) (map[string]any, error) {
	records, err := feedbackQueryDictRows(c, query, args...)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0], nil
}

// feedbackNormalizeValue 把驱动返回值归一为 Python sqlite3 的类型风格
func feedbackNormalizeValue(value any) any {
	switch v := value.(type) {
	case int64:
		return int(v)
	case []byte:
		return string(v)
	default:
		return value
	}
}

// feedbackTruthy 对齐 Python 真值判断（None/空串/0/False 为假）
func feedbackTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	default:
		return true
	}
}

// feedbackStrOr 对齐 Python str(value or fallback).strip()
func feedbackStrOr(value any, fallback string) string {
	if !feedbackTruthy(value) {
		return fallback
	}
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return fallback
	}
	return text
}

// feedbackIntValue 对齐 Python int(value or 0)
func feedbackIntValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		token := strings.TrimSpace(v)
		if token == "" {
			return 0
		}
		if parsed, err := strconv.Atoi(token); err == nil {
			return parsed
		}
		if parsed, err := strconv.ParseFloat(token, 64); err == nil {
			return int(parsed)
		}
		return 0
	default:
		return 0
	}
}

// feedbackNullableString 对齐 Python str(value or "").strip() or None
func feedbackNullableString(value any) any {
	token := feedbackStrOr(value, "")
	if token == "" {
		return nil
	}
	return token
}

// feedbackNullableTaskID 对齐 Python int(task_id) if int(task_id or 0) > 0 else None
func feedbackNullableTaskID(taskID int) any {
	if taskID > 0 {
		return taskID
	}
	return nil
}

// feedbackOptionalFloat 对齐 Python optional_float(...)，无法转换返回 None
func feedbackOptionalFloat(value any) any {
	if parsed := OptionalFloat(value); parsed != nil {
		return *parsed
	}
	return nil
}

// feedbackDictJSON 对齐 Python _json_dumps(value or {})，nil 字典按 {} 写出
func feedbackDictJSON(value map[string]any) string {
	if value == nil {
		return "{}"
	}
	return jsonDumps(value)
}

// feedbackTaskRowToDict 对齐 Python _feedback_task_row_to_dict
func feedbackTaskRowToDict(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	data := row
	snapshot := data["query_snapshot_json"]
	delete(data, "query_snapshot_json")
	data["query_snapshot"] = jsonLoads(snapshot, map[string]any{})
	data["decision_payload"] = jsonLoads(data["decision_json"], map[string]any{})
	data["rollback_status"] = strings.ToLower(feedbackStrOr(data["rollback_status"], "none"))

	plan := data["rollback_plan_json"]
	delete(data, "rollback_plan_json")
	data["rollback_plan"] = jsonLoads(plan, map[string]any{})

	result := data["rollback_result_json"]
	delete(data, "rollback_result_json")
	data["rollback_result"] = jsonLoads(result, map[string]any{})

	data["rollback_error"] = feedbackStrOr(data["rollback_error"], "")
	data["rollback_requested_by"] = feedbackStrOr(data["rollback_requested_by"], "")
	data["rollback_reason"] = feedbackStrOr(data["rollback_reason"], "")
	return data
}

// feedbackActionLogRowToDict 对齐 Python _feedback_action_log_row_to_dict
func feedbackActionLogRowToDict(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	data := row
	data["id"] = feedbackIntValue(data["id"])
	data["task_id"] = feedbackIntValue(data["task_id"])
	data["query_tool_id"] = feedbackStrOr(data["query_tool_id"], "")
	data["action_type"] = feedbackStrOr(data["action_type"], "")
	data["target_hash"] = feedbackStrOr(data["target_hash"], "")
	data["reason"] = feedbackStrOr(data["reason"], "")

	before := data["before_json"]
	delete(data, "before_json")
	data["before_payload"] = jsonLoads(before, map[string]any{})

	after := data["after_json"]
	delete(data, "after_json")
	data["after_payload"] = jsonLoads(after, map[string]any{})
	return data
}

// feedbackStaleMarkRowToDict 对齐 Python _paragraph_stale_relation_mark_row_to_dict
func feedbackStaleMarkRowToDict(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	data := row
	data["paragraph_hash"] = feedbackStrOr(data["paragraph_hash"], "")
	data["relation_hash"] = feedbackStrOr(data["relation_hash"], "")
	data["query_tool_id"] = feedbackStrOr(data["query_tool_id"], "")
	if value, ok := data["task_id"]; ok && value != nil {
		data["task_id"] = feedbackIntValue(value)
	} else {
		data["task_id"] = nil
	}
	data["reason"] = feedbackStrOr(data["reason"], "")
	data["source_type"] = feedbackStrOr(data["source_type"], "")
	data["source_id"] = feedbackStrOr(data["source_id"], "")
	data["source_operation_id"] = feedbackStrOr(data["source_operation_id"], "")
	data["created_at"] = feedbackOptionalFloat(data["created_at"])
	data["updated_at"] = feedbackOptionalFloat(data["updated_at"])
	return data
}
