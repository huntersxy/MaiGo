package webuiapi

// WebUI 维护、V5 生命周期、删除与纠错路由

import (
	"net/http"
	"strings"
)

func (d Deps) handleMemoryMaintenanceRecycleBin(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "recycle_bin", map[string]any{"limit": limit})
}

func (d Deps) handleMemoryMaintenanceRestore(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	target, ok := bodyRequiredString(w, payload, "target")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "restore", map[string]any{"target": target})
}

func (d Deps) handleMemoryMaintenanceReinforce(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	target, ok := bodyRequiredString(w, payload, "target")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "reinforce", map[string]any{
		"target": target, "reason": "WebUI 手动强化", "updated_by": "webui",
	})
}

func (d Deps) handleMemoryMaintenanceFreeze(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	target, ok := bodyRequiredString(w, payload, "target")
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "freeze", map[string]any{
		"target": target, "reason": "WebUI 手动冻结", "updated_by": "webui",
	})
}

func (d Deps) handleMemoryMaintenanceProtect(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	target, ok := bodyRequiredString(w, payload, "target")
	if !ok {
		return
	}
	args := map[string]any{"target": target, "reason": "WebUI 手动保护", "updated_by": "webui"}
	if hours, exists := payload["hours"]; exists && hours != nil {
		parsed, ok := anyFloat(hours)
		if !ok || parsed < 0 {
			writeValidationError(w, "hours 必须是非负数字")
			return
		}
		args["hours"] = parsed
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "protect", args)
}

func (d Deps) handleMemoryV5Status(w http.ResponseWriter, r *http.Request) {
	target, ok := mqRequiredString(w, r, "target")
	if !ok {
		return
	}
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "status", map[string]any{"target": target, "limit": limit})
}

func (d Deps) handleMemoryV5RecycleBin(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_v5_admin", "recycle_bin", map[string]any{"limit": limit})
}

func (d Deps) handleMemoryV5Action(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeMemoryJSON(w, r)
		if !ok {
			return
		}
		target, ok := bodyRequiredString(w, payload, "target")
		if !ok {
			return
		}
		args := map[string]any{
			"target": target, "reason": anyTrimmed(payload["reason"]),
			"updated_by": defaultStringField(payload, "updated_by", "webui"),
		}
		if value, exists := payload["strength"]; exists && value != nil {
			parsed, ok := anyFloat(value)
			if !ok || parsed < 0 {
				writeValidationError(w, "strength 必须是非负数")
				return
			}
			args["strength"] = parsed
		}
		d.invokeMemoryAdmin(w, r, "memory_v5_admin", action, args)
	}
}

func (d Deps) handleMemoryDeletePreview(w http.ResponseWriter, r *http.Request) {
	d.memoryDeleteAction(w, r, "preview", false)
}

func (d Deps) handleMemoryDeleteExecute(w http.ResponseWriter, r *http.Request) {
	d.memoryDeleteAction(w, r, "execute", true)
}

func (d Deps) memoryDeleteAction(w http.ResponseWriter, r *http.Request, action string, withReason bool) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	mode, ok := bodyRequiredString(w, payload, "mode")
	if !ok {
		return
	}
	args := map[string]any{"mode": mode, "selector": payload["selector"]}
	if args["selector"] == nil {
		args["selector"] = map[string]any{}
	}
	if withReason {
		args["reason"] = anyTrimmed(payload["reason"])
		args["requested_by"] = defaultStringField(payload, "requested_by", "webui")
	}
	d.invokeMemoryAdmin(w, r, "memory_delete_admin", action, args)
}

func (d Deps) handleMemoryDeleteRestore(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	mode, ok := bodyRequiredString(w, payload, "mode")
	if !ok {
		return
	}
	args := map[string]any{
		"mode": mode, "selector": payload["selector"],
		"operation_id": anyTrimmed(payload["operation_id"]),
		"reason":       anyTrimmed(payload["reason"]),
		"requested_by": defaultStringField(payload, "requested_by", "webui"),
	}
	if args["selector"] == nil {
		args["selector"] = map[string]any{}
	}
	d.invokeMemoryAdmin(w, r, "memory_delete_admin", "restore", args)
}

func (d Deps) handleMemoryDeleteOperations(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_delete_admin", "list_operations", map[string]any{
		"limit": limit, "mode": mqString(r, "mode"),
	})
}

func (d Deps) handleMemoryDeleteOperationGet(w http.ResponseWriter, r *http.Request) {
	operationID := strings.TrimSpace(chiURLParam(r, "operation_id"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, "operation_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_delete_admin", "get_operation", map[string]any{"operation_id": operationID})
}

func (d Deps) handleMemoryDeletePurge(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	args := map[string]any{}
	if value, exists := payload["grace_hours"]; exists && value != nil {
		parsed, ok := anyFloat(value)
		if !ok || parsed < 0 {
			writeValidationError(w, "grace_hours 必须是非负数")
			return
		}
		args["grace_hours"] = parsed
	}
	limit := 1000
	if value, exists := payload["limit"]; exists && value != nil {
		parsed := anyInt(value)
		if parsed < 1 || parsed > 5000 {
			writeValidationError(w, "limit 必须在 1 到 5000 之间")
			return
		}
		limit = parsed
	}
	args["limit"] = limit
	d.invokeMemoryAdmin(w, r, "memory_delete_admin", "purge", args)
}

func (d Deps) handleMemoryCorrectionPreview(component string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeMemoryJSON(w, r)
		if !ok {
			return
		}
		requestText, ok := bodyRequiredString(w, payload, "request_text")
		if !ok {
			return
		}
		args := map[string]any{
			"request_text": requestText, "scope": defaultStringField(payload, "scope", "person_profile"),
			"person_id":      anyTrimmed(payload["person_id"]),
			"person_keyword": anyTrimmed(payload["person_keyword"]),
			"requested_by":   defaultStringField(payload, "requested_by", "webui"),
			"reason":         anyTrimmed(payload["reason"]),
		}
		if raw, exists := payload["targets"]; exists && raw != nil {
			args["targets"] = raw
		}
		if value, exists := payload["limit"]; exists && value != nil {
			args["limit"] = anyInt(value)
		}
		d.invokeMemoryAdmin(w, r, component, "preview", args)
	}
}

func (d Deps) handleMemoryCorrectionExecute(component string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeMemoryJSON(w, r)
		if !ok {
			return
		}
		planID, ok := bodyRequiredString(w, payload, "plan_id")
		if !ok {
			return
		}
		confirmed := true
		if value, exists := payload["confirmed"]; exists && value != nil {
			if typed, ok := value.(bool); ok {
				confirmed = typed
			}
		}
		d.invokeMemoryAdmin(w, r, component, "execute", map[string]any{
			"plan_id": planID, "confirmed": confirmed,
			"requested_by": defaultStringField(payload, "requested_by", "webui"),
			"reason":       anyTrimmed(payload["reason"]),
		})
	}
}

func (d Deps) handleMemoryCorrectionList(component string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, ok := mqInt(w, r, "limit", 50, 1, 200)
		if !ok {
			return
		}
		d.invokeMemoryAdmin(w, r, component, "list", map[string]any{
			"limit": limit, "status": mqString(r, "status"), "scope": mqString(r, "scope"),
		})
	}
}

func (d Deps) handleMemoryCorrectionGet(component string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID := strings.TrimSpace(chiURLParam(r, "plan_id"))
		if planID == "" {
			writeAPIError(w, http.StatusBadRequest, "plan_id 不能为空")
			return
		}
		d.invokeMemoryAdmin(w, r, component, "get", map[string]any{"plan_id": planID})
	}
}

func (d Deps) handleMemoryCorrectionRollback(component string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID := strings.TrimSpace(chiURLParam(r, "plan_id"))
		if planID == "" {
			writeAPIError(w, http.StatusBadRequest, "plan_id 不能为空")
			return
		}
		payload, ok := decodeMemoryJSON(w, r)
		if !ok {
			return
		}
		d.invokeMemoryAdmin(w, r, component, "rollback", map[string]any{
			"plan_id": planID, "requested_by": defaultStringField(payload, "requested_by", "webui"),
			"reason": anyTrimmed(payload["reason"]),
		})
	}
}
