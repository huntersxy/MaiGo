package desktoppet

// 服务器待办命令，始终由工作线程执行文件读写，对齐 desktop_pet/todo_dispatch.py

import "fmt"

// ExecuteTodo 执行一条待办命令
func ExecuteTodo(store *TodoStore, command map[string]any) (map[string]any, error) {
	if store == nil {
		return nil, fmt.Errorf("待办存储未初始化")
	}
	action := asString(command["action"])
	switch action {
	case "list":
		includeCompleted, _ := command["include_completed"].(bool)
		return map[string]any{"items": store.List(includeCompleted)}, nil
	case "image":
		imageID := asString(command["image_id"])
		dataURL, err := store.Image(imageID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"data_url": dataURL}, nil
	case "add":
		options, _ := command["options"].(map[string]any)
		item, err := store.Add(asString(command["text"]), options)
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": item}, nil
	case "update":
		var text *string
		if raw, exists := command["text"]; exists && raw != nil {
			value := asString(raw)
			text = &value
		}
		options, _ := command["options"].(map[string]any)
		item, err := store.Update(asString(command["todo_id"]), text, options)
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": item}, nil
	case "complete":
		todoID := asString(command["todo_id"])
		if err := store.Done(todoID); err != nil {
			return nil, err
		}
		return map[string]any{"completed": todoID}, nil
	case "delete":
		todoID := asString(command["todo_id"])
		if err := store.Delete(todoID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": todoID}, nil
	case "claim_due":
		var kind *string
		if raw, ok := command["kind"].(string); ok && raw != "" {
			kind = &raw
		}
		return map[string]any{"items": store.ClaimDue(kind)}, nil
	case "claim_condition":
		item, err := store.ClaimCondition(
			asString(command["todo_id"]), asString(command["revision"]), asString(command["evidence"]),
		)
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": item}, nil
	case "finish":
		store.Finish(asString(command["todo_id"]), asString(command["revision"]), asString(command["error"]))
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("未知待办操作")
	}
}
