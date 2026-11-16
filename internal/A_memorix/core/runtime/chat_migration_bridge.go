package memruntime

// 在线聊天流迁移的长期记忆复制桥接。

import (
	"fmt"

	runtimeservices "MaiGo/internal/A_memorix/core/runtime/services"
)

// ChatMemoryMigration 执行记忆迁移动作
func (k *Kernel) ChatMemoryMigration(action string, args map[string]any) (map[string]any, error) {
	if k == nil || k.metadataStore == nil {
		return nil, fmt.Errorf("A_Memorix 元数据存储未就绪")
	}
	service := runtimeservices.ChatMemoryMigration{Store: k.metadataStore}
	switch action {
	case "snapshot":
		return service.Snapshot(argumentString(args, "source"), argumentString(args, "target"))
	case "apply":
		return service.Apply(argumentString(args, "job_id"), argumentString(args, "source"),
			argumentString(args, "target"), argumentString(args, "expected"))
	case "restore":
		return service.Restore(argumentString(args, "job_id"))
	default:
		return nil, fmt.Errorf("未知记忆迁移动作: %s", action)
	}
}

func argumentString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}
