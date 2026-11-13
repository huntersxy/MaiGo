package memruntime

// A_memorix admin 组件契约，对齐 Python core/runtime/admin_contracts.py
// 语义：component/action 归一化为小写去空白，payload 去掉 action 后按原样交给内核方法

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// AdminContractError 对齐 Python AdminContractError
type AdminContractError struct {
	Message string
}

// Error 实现 error
func (e *AdminContractError) Error() string { return e.Message }

// ToResponse 返回统一失败响应
func (e *AdminContractError) ToResponse() map[string]any {
	return map[string]any{"success": false, "error": e.Message}
}

// AdminCommand 对齐 Python AdminCommand
type AdminCommand struct {
	ComponentName string
	Action        string
	Payload       map[string]any
}

// AdminKernel 是 admin 命令最终落到的内核方法集合
// 对齐 Python 中 kernel.memory_*_admin(action=..., **payload) 的调用面
type AdminKernel interface {
	MemoryGraphAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemorySourceAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryEpisodeAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryProfileAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryFeedbackAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryFactAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryRuntimeAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryImportAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryBundleAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryTuningAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryV5Admin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryDeleteAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryCorrectionAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
	MemoryFuzzyModifyAdmin(ctx context.Context, action string, payload map[string]any) (any, error)
}

// AdminDispatcher 对齐 Python AdminDispatcher
type AdminDispatcher func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error)

// AdminComponentSpec 对齐 Python AdminComponentSpec
type AdminComponentSpec struct {
	ComponentName string
	Actions       map[string]struct{}
	Dispatcher    AdminDispatcher
}

var (
	graphAdminActions = newActionSet(
		"get_graph", "search", "node_detail", "edge_detail", "create_node", "delete_node",
		"rename_node", "create_edge", "delete_edge", "update_edge_weight",
	)
	sourceAdminActions     = newActionSet("list", "delete", "batch_delete")
	episodeAdminActions    = newActionSet("query", "list", "get", "status", "rebuild", "process_sources", "discard_migration_backfill")
	profileAdminActions    = newActionSet("observe_identity", "resolve_identity", "query", "evidence", "correct_evidence", "status", "process_pending", "list", "get_aliases", "set_aliases", "delete_aliases", "set_override", "delete_override")
	feedbackAdminActions   = newActionSet("list", "get", "rollback")
	factAdminActions       = newActionSet("get", "list", "create", "update", "retract", "restore")
	runtimeAdminActions    = newActionSet("save", "get_config", "self_check", "refresh_self_check", "set_auto_save", "recover_embedding", "rebuild_all_vectors", "paragraph_backfill_once")
	importAdminActions     = newActionSet("settings", "get_settings", "get_guide", "path_aliases", "get_path_aliases", "resolve_path", "resolve", "create_upload", "create_paste", "create_raw_scan", "create_lpmm_openie", "create_lpmm_convert", "create_temporal_backfill", "create_maibot_migration", "list", "get", "chunks", "get_chunks", "cancel", "retry_failed")
	bundleAdminActions     = newActionSet("export", "inspect", "import", "list", "resolve_file", "uninstall")
	tuningAdminActions     = newActionSet("settings", "get_settings", "get_profile", "apply_profile", "rollback_profile", "export_profile", "create_task", "list_tasks", "get_task", "get_rounds", "cancel", "apply_best", "get_report")
	v5AdminActions         = newActionSet("recycle_bin", "status", "restore", "reinforce", "weaken", "remember_forever", "forget")
	deleteAdminActions     = newActionSet("preview", "execute", "restore", "get_operation", "list_operations", "purge")
	correctionAdminActions = newActionSet("preview", "plan", "execute", "get", "list", "rollback")
)

// AdminComponentSpecs 对齐 Python ADMIN_COMPONENT_SPECS，保持同样的 14 个组件
var AdminComponentSpecs = map[string]AdminComponentSpec{}

// AdminComponentNames 返回全部组件名，对齐 Python ADMIN_COMPONENT_NAMES
func AdminComponentNames() map[string]struct{} {
	out := make(map[string]struct{}, len(AdminComponentSpecs))
	for name := range AdminComponentSpecs {
		out[name] = struct{}{}
	}
	return out
}

// IsAdminComponent 判断组件名（归一化后）是否受支持
func IsAdminComponent(componentName string) bool {
	_, ok := AdminComponentSpecs[normalizeAdminToken(componentName)]
	return ok
}

// ParseAdminCommand 解析组件与 payload，错误文案对齐 Python
func ParseAdminCommand(componentName string, payload any) (AdminCommand, error) {
	normalizedComponent := normalizeAdminToken(componentName)
	spec, ok := AdminComponentSpecs[normalizedComponent]
	if !ok {
		return AdminCommand{}, &AdminContractError{Message: fmt.Sprintf("不支持的 A_Memorix admin component: %s", componentName)}
	}

	payloadMap := map[string]any{}
	if payload != nil {
		converted, ok := payload.(map[string]any)
		if !ok {
			return AdminCommand{}, &AdminContractError{Message: fmt.Sprintf("%s 参数必须为对象", normalizedComponent)}
		}
		for key, value := range converted {
			payloadMap[key] = value
		}
	}

	action := normalizeAdminToken(stringValue(payloadMap["action"]))
	delete(payloadMap, "action")
	if action == "" {
		return AdminCommand{}, &AdminContractError{Message: fmt.Sprintf("%s action 不能为空", normalizedComponent)}
	}
	if _, ok := spec.Actions[action]; !ok {
		allowed := sortedActionList(spec.Actions)
		return AdminCommand{}, &AdminContractError{Message: fmt.Sprintf("不支持的 %s action: %s。允许值: %s", normalizedComponent, action, allowed)}
	}

	return AdminCommand{ComponentName: normalizedComponent, Action: action, Payload: payloadMap}, nil
}

// DispatchAdminCommand 按组件路由到内核方法
func DispatchAdminCommand(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
	spec, ok := AdminComponentSpecs[command.ComponentName]
	if !ok {
		return nil, &AdminContractError{Message: fmt.Sprintf("不支持的 A_Memorix admin component: %s", command.ComponentName)}
	}
	return spec.Dispatcher(ctx, kernel, command)
}

func newActionSet(actions ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		out[action] = struct{}{}
	}
	return out
}

func normalizeAdminToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sortedActionList(actions map[string]struct{}) string {
	list := make([]string, 0, len(actions))
	for action := range actions {
		list = append(list, action)
	}
	sort.Strings(list)
	return strings.Join(list, ", ")
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func registerAdminComponent(name string, actions map[string]struct{}, dispatcher AdminDispatcher) {
	AdminComponentSpecs[name] = AdminComponentSpec{ComponentName: name, Actions: actions, Dispatcher: dispatcher}
}

func init() {
	registerAdminComponent("memory_graph_admin", graphAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryGraphAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_source_admin", sourceAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemorySourceAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_episode_admin", episodeAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryEpisodeAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_profile_admin", profileAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryProfileAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_feedback_admin", feedbackAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryFeedbackAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_fact_admin", factAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryFactAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_runtime_admin", runtimeAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryRuntimeAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_import_admin", importAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryImportAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_bundle_admin", bundleAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryBundleAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_tuning_admin", tuningAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryTuningAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_v5_admin", v5AdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryV5Admin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_delete_admin", deleteAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryDeleteAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_correction_admin", correctionAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryCorrectionAdmin(ctx, command.Action, command.Payload)
	})
	registerAdminComponent("memory_fuzzy_modify_admin", correctionAdminActions, func(ctx context.Context, kernel AdminKernel, command AdminCommand) (any, error) {
		return kernel.MemoryFuzzyModifyAdmin(ctx, command.Action, command.Payload)
	})
}
