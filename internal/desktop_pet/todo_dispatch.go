package desktoppet

// 桌宠事项与聊天日程共用命令入口，对齐 desktop_pet/todo_dispatch.py。

import (
	"MaiGo/internal/services/schedules"
)

// ExecuteTodo 执行一条待办命令
func ExecuteTodo(store *TodoStore, command map[string]any) (map[string]any, error) {
	return schedules.ExecuteTodo(store, command)
}
