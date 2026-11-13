package computer

// 桌面动作契约：同时用于模型 schema 与执行前整批校验，对齐 actions.py

import (
	"fmt"
	"math"
)

// DesktopActionName 对齐 actions.action_schemas 中的 action 常量
type DesktopActionName string

const (
	ActionObserve     DesktopActionName = "observe"
	ActionRelease     DesktopActionName = "release"
	ActionMove        DesktopActionName = "move"
	ActionClick       DesktopActionName = "click"
	ActionDoubleClick DesktopActionName = "double_click"
	ActionDrag        DesktopActionName = "drag"
	ActionMouseDown   DesktopActionName = "mouse_down"
	ActionMouseUp     DesktopActionName = "mouse_up"
	ActionScroll      DesktopActionName = "scroll"
	ActionKeys        DesktopActionName = "keys"
	ActionKeyDown     DesktopActionName = "key_down"
	ActionKeyUp       DesktopActionName = "key_up"
	ActionType        DesktopActionName = "type"
	ActionOpen        DesktopActionName = "open"
	ActionShell       DesktopActionName = "shell"
	ActionWait        DesktopActionName = "wait"
	ActionWaitWindow  DesktopActionName = "wait_window"
	ActionWaitProcess DesktopActionName = "wait_process"
	ActionInspect     DesktopActionName = "inspect"
	ActionUIA         DesktopActionName = "uia"
	ActionBrowser     DesktopActionName = "browser"
)

// KnownDesktopActions 返回 MaiBot 已定义的桌面动作名（不含校验逻辑）
func KnownDesktopActions() []DesktopActionName {
	return []DesktopActionName{
		ActionObserve, ActionRelease, ActionMove, ActionClick, ActionDoubleClick,
		ActionDrag, ActionMouseDown, ActionMouseUp, ActionScroll,
		ActionKeys, ActionKeyDown, ActionKeyUp, ActionType, ActionOpen, ActionShell,
		ActionWait, ActionWaitWindow, ActionWaitProcess, ActionInspect, ActionUIA, ActionBrowser,
	}
}

func actionNumberSchema() map[string]any {
	return map[string]any{"type": "number", "minimum": 0}
}

func actionKeysSchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}
}

func actionButtonSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []any{"left", "right", "middle"}}
}

func actionSchema(name DesktopActionName, fields map[string]any, required []any) map[string]any {
	properties := map[string]any{"action": map[string]any{"type": "string", "const": string(name)}}
	for key, value := range fields {
		properties[key] = value
	}
	allRequired := make([]any, 0, 1+len(required))
	allRequired = append(allRequired, "action")
	allRequired = append(allRequired, required...)
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             allRequired,
		"additionalProperties": false,
	}
}

// ActionSchemas 返回 computer_desktop actions 的 oneOf schema，顺序与 Python 一致
func ActionSchemas() []map[string]any {
	return []map[string]any{
		actionSchema(ActionObserve, nil, nil),
		actionSchema(ActionRelease, nil, nil),
		actionSchema(ActionMove, map[string]any{
			"x": actionNumberSchema(), "y": actionNumberSchema(),
		}, []any{"x", "y"}),
		actionSchema(ActionClick, map[string]any{
			"x": actionNumberSchema(), "y": actionNumberSchema(), "button": actionButtonSchema(),
		}, []any{"x", "y"}),
		actionSchema(ActionDoubleClick, map[string]any{
			"x": actionNumberSchema(), "y": actionNumberSchema(), "button": actionButtonSchema(),
		}, []any{"x", "y"}),
		actionSchema(ActionDrag, map[string]any{
			"x": actionNumberSchema(), "y": actionNumberSchema(),
			"end_x": actionNumberSchema(), "end_y": actionNumberSchema(),
			"seconds": actionNumberSchema(), "button": actionButtonSchema(),
		}, []any{"x", "y", "end_x", "end_y"}),
		actionSchema(ActionMouseDown, map[string]any{"button": actionButtonSchema()}, nil),
		actionSchema(ActionMouseUp, map[string]any{"button": actionButtonSchema()}, nil),
		actionSchema(ActionScroll, map[string]any{"delta": map[string]any{"type": "integer"}}, []any{"delta"}),
		actionSchema(ActionKeys, map[string]any{
			"keys": actionKeysSchema(), "seconds": actionNumberSchema(),
		}, []any{"keys"}),
		actionSchema(ActionKeyDown, map[string]any{"keys": actionKeysSchema()}, []any{"keys"}),
		actionSchema(ActionKeyUp, map[string]any{"keys": actionKeysSchema()}, []any{"keys"}),
		actionSchema(ActionType, map[string]any{"text": map[string]any{"type": "string"}}, []any{"text"}),
		actionSchema(ActionOpen, map[string]any{
			"path":      map[string]any{"type": "string"},
			"arguments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, []any{"path"}),
		actionSchema(ActionShell, map[string]any{"command": map[string]any{"type": "string"}}, []any{"command"}),
		actionSchema(ActionWait, map[string]any{"seconds": actionNumberSchema()}, []any{"seconds"}),
		actionSchema(ActionWaitWindow, map[string]any{
			"title": map[string]any{"type": "string"}, "seconds": actionNumberSchema(),
		}, []any{"title", "seconds"}),
		actionSchema(ActionWaitProcess, map[string]any{
			"pid": map[string]any{"type": "integer", "minimum": 1}, "seconds": actionNumberSchema(),
		}, []any{"pid", "seconds"}),
		actionSchema(ActionInspect, map[string]any{
			"depth": map[string]any{"type": "integer", "minimum": 0},
			"limit": map[string]any{"type": "integer", "minimum": 0},
		}, nil),
		actionSchema(ActionUIA, map[string]any{
			"runtime_id": map[string]any{"type": "string"},
			"operation":  map[string]any{"type": "string", "enum": []any{"invoke", "focus", "set_value", "select"}},
			"value":      map[string]any{"type": "string"},
		}, []any{"runtime_id", "operation"}),
		actionSchema(ActionBrowser, map[string]any{
			"method": map[string]any{"type": "string"},
			"target": map[string]any{"type": "string"},
			"params": map[string]any{"type": "object"},
		}, []any{"method"}),
	}
}

func validateActionField(index int, name string, value any, schema map[string]any) error {
	kind, _ := schema["type"].(string)
	valid := false
	switch kind {
	case "string":
		_, valid = value.(string)
	case "number":
		valid = isFiniteNumber(value)
	case "integer":
		valid = isIntegerValue(value)
	case "array":
		valid = isStringArray(value, schemaMinItems(schema))
	case "object":
		_, valid = value.(map[string]any)
	}
	if !valid {
		return fmt.Errorf("actions[%d].%s 必须为 %s", index, name, kind)
	}
	if kind == "number" || kind == "integer" {
		number, ok := numberValue(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("actions[%d].%s 数值无效", index, name)
		}
		if minimum, ok := numberValue(schema["minimum"]); ok && number < minimum {
			return fmt.Errorf("actions[%d].%s 数值无效", index, name)
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if value == candidate {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("actions[%d].%s 取值无效", index, name)
		}
	}
	return nil
}

func schemaMinItems(schema map[string]any) int {
	if raw, ok := schema["minItems"]; ok {
		if value, ok := numberValue(raw); ok {
			return int(value)
		}
	}
	return 0
}

func isFiniteNumber(value any) bool {
	number, ok := numberValue(value)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func isIntegerValue(value any) bool {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return typed == float32(math.Trunc(float64(typed)))
	case float64:
		return typed == math.Trunc(typed) && !math.IsNaN(typed) && !math.IsInf(typed, 0)
	default:
		return false
	}
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func isStringArray(value any, minItems int) bool {
	var items []any
	switch typed := value.(type) {
	case []string:
		if len(typed) < minItems {
			return false
		}
		return true
	case []any:
		items = typed
	default:
		return false
	}
	if len(items) < minItems {
		return false
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

// ValidateActions 整批校验后才发送；错误批次不会产生部分输入，对齐 validate_actions
func ValidateActions(actions []map[string]any) error {
	schemas := make(map[DesktopActionName]map[string]any, len(ActionSchemas()))
	for _, schema := range ActionSchemas() {
		properties, _ := schema["properties"].(map[string]any)
		action, _ := properties["action"].(map[string]any)
		name, _ := action["const"].(string)
		schemas[DesktopActionName(name)] = schema
	}
	for index, action := range actions {
		if action == nil {
			return fmt.Errorf("actions[%d] 不是已定义的桌面动作", index)
		}
		rawName, _ := action["action"].(string)
		name := DesktopActionName(rawName)
		schema, ok := schemas[name]
		if !ok {
			return fmt.Errorf("actions[%d] 不是已定义的桌面动作", index)
		}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, raw := range required {
			field, _ := raw.(string)
			if _, exists := action[field]; !exists {
				return fmt.Errorf("actions[%d] 缺少 %s", index, field)
			}
		}
		for field, value := range action {
			fieldSchema, exists := properties[field].(map[string]any)
			if !exists {
				return fmt.Errorf("actions[%d] 未知参数 %s", index, field)
			}
			if err := validateActionField(index, field, value, fieldSchema); err != nil {
				return err
			}
		}
		if name == ActionUIA && action["operation"] == "set_value" {
			if _, exists := action["value"]; !exists {
				return fmt.Errorf("actions[%d] set_value 缺少 value", index)
			}
		}
		if name == ActionWaitWindow {
			if _, exists := action["seconds"]; !exists {
				return fmt.Errorf("actions[%d] 缺少 seconds", index)
			}
		}
	}
	return nil
}
