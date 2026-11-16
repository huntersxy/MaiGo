package desktoppet

// 桌宠事项与聊天日程共用主程序存储，对齐 desktop_pet/todos.py。

import (
	"encoding/json"
	"fmt"

	"MaiGo/internal/services/schedules"
)

// TodoStore 桌宠事项存储
type TodoStore = schedules.TodoStore

// NewTodoStore 加载既有事项与附件路径
func NewTodoStore(path string) *TodoStore {
	return schedules.NewTodoStore(path)
}

// 以下数值转换供桌宠事件与探索结果读取 JSON 值
func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func asFloat(value any) float64 {
	number, _ := asNumber(value)
	return number
}

func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
