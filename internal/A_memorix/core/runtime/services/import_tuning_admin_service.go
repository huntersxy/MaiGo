package services

// 导入与检索调优管理服务，对齐 Python core/runtime/services/import_tuning_admin_service.py

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// ImportTuningAdminDeps 导入与调优管理服务依赖的管理器端口
type ImportTuningAdminDeps struct {
	// Initialize 确保运行时已初始化，对齐 await self.initialize()
	Initialize func(ctx context.Context) error
	// ImportManager 返回 Web 导入任务管理器，nil 表示未初始化
	ImportManager func() *utils.WebImportManager
	// TuningManager 返回检索调优管理器，nil 表示未初始化
	TuningManager func() *utils.RetrievalTuningManager
}

// ImportTuningAdminService 对齐 MemoryImportTuningAdminService
type ImportTuningAdminService struct {
	Deps ImportTuningAdminDeps
}

// NewImportTuningAdminService 构造导入与调优管理服务
func NewImportTuningAdminService(deps ImportTuningAdminDeps) *ImportTuningAdminService {
	return &ImportTuningAdminService{Deps: deps}
}

// MemoryImportAdmin 对齐 memory_import_admin
func (s *ImportTuningAdminService) MemoryImportAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	if s.Deps.ImportManager == nil || s.Deps.ImportManager() == nil {
		return map[string]any{"success": false, "error": "import manager 未初始化"}, nil
	}
	manager := s.Deps.ImportManager()
	act := normalizeAction(action)
	switch act {
	case "settings", "get_settings", "get_guide":
		return map[string]any{"success": true, "settings": manager.GetRuntimeSettings()}, nil
	case "path_aliases", "get_path_aliases":
		return map[string]any{"success": true, "path_aliases": manager.GetPathAliases()}, nil
	case "resolve_path", "resolve":
		result, err := manager.ResolvePathRequest(payload)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "create_upload":
		files, err := importUploadFiles(importPayloadFiles(payload))
		if err != nil {
			return nil, err
		}
		task, err := manager.CreateUploadTask(files, payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "create_paste":
		task, err := manager.CreatePasteTask(payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "create_raw_scan":
		task, err := manager.CreateRawScanTask(payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "create_lpmm_openie":
		task, err := manager.CreateLpmmOpenieTask(payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "create_lpmm_convert":
		task, err := manager.CreateLpmmConvertTask(payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "create_temporal_backfill":
		task, err := manager.CreateTemporalBackfillTask(payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "create_maibot_migration":
		task, err := manager.CreateMaibotMigrationTask(payload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "list":
		items := manager.ListTasks(pyIntOr(payload["limit"], 50, 1))
		if items == nil {
			items = []map[string]any{}
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	case "get":
		task := manager.GetTask(asString(payload["task_id"]), pyTruthy(payload["include_chunks"]))
		return importTaskResult(task), nil
	case "chunks", "get_chunks":
		result := manager.GetChunks(
			asString(payload["task_id"]),
			asString(payload["file_id"]),
			intOr(payload["offset"], 0, 0),
			pyIntOr(payload["limit"], 50, 1),
		)
		return importMergedResult(result, "任务或文件不存在"), nil
	case "cancel":
		task := manager.CancelTask(asString(payload["task_id"]))
		return importTaskResult(task), nil
	case "retry_failed":
		overrides := payload
		if typed := asMap(payload["overrides"]); typed != nil {
			overrides = typed
		}
		task, err := manager.RetryFailed(asString(payload["task_id"]), overrides)
		if err != nil {
			return nil, err
		}
		return importTaskResult(task), nil
	}
	return map[string]any{"success": false, "error": "不支持的 import action: " + act}, nil
}

// MemoryTuningAdmin 对齐 memory_tuning_admin
func (s *ImportTuningAdminService) MemoryTuningAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	if s.Deps.TuningManager == nil || s.Deps.TuningManager() == nil {
		return map[string]any{"success": false, "error": "tuning manager 未初始化"}, nil
	}
	manager := s.Deps.TuningManager()
	act := normalizeAction(action)
	switch act {
	case "settings", "get_settings":
		return map[string]any{"success": true, "settings": manager.GetRuntimeSettings()}, nil
	case "get_profile", "export_profile":
		profile := manager.GetProfileSnapshot()
		persistable := manager.GetPersistableProfile(profile)
		return map[string]any{
			"success":             true,
			"profile":             profile,
			"runtime_profile":     profile,
			"persistable_profile": persistable,
			"toml":                manager.ExportTOMLSnippet(persistable),
		}, nil
	case "apply_profile":
		profilePayload := map[string]any{}
		if typed := asMap(payload["profile"]); typed != nil {
			profilePayload = cloneAnyMap(typed)
		} else {
			for key, value := range payload {
				if key == "reason" || key == "profile" {
					continue
				}
				profilePayload[key] = value
			}
		}
		reason := "manual"
		if text := asString(payload["reason"]); strings.TrimSpace(text) != "" {
			reason = text
		}
		validate := true
		if raw, ok := payload["validate"]; ok {
			validate = pyTruthy(raw)
		}
		outcome, err := manager.ApplyProfile(ctx, profilePayload, reason, validate)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"success": true}
		for key, value := range outcome {
			result[key] = value
		}
		return result, nil
	case "rollback_profile":
		outcome, err := manager.RollbackProfile(ctx)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"success": true}
		for key, value := range outcome {
			result[key] = value
		}
		return result, nil
	case "create_task":
		taskPayload := payload
		if typed := asMap(payload["payload"]); typed != nil {
			taskPayload = typed
		}
		task, err := manager.CreateTask(taskPayload)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "task": task}, nil
	case "list_tasks":
		items := manager.ListTasks(pyIntOr(payload["limit"], 50, 1))
		if items == nil {
			items = []map[string]any{}
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	case "get_task":
		task := manager.GetTask(asString(payload["task_id"]), pyTruthy(payload["include_rounds"]))
		return importTaskResult(task), nil
	case "get_rounds":
		result := manager.GetRounds(
			asString(payload["task_id"]),
			intOr(payload["offset"], 0, 0),
			pyIntOr(payload["limit"], 50, 1),
		)
		return importMergedResult(result, "任务不存在"), nil
	case "cancel":
		task := manager.CancelTask(asString(payload["task_id"]))
		return importTaskResult(task), nil
	case "apply_best":
		validate := true
		if raw, ok := payload["validate"]; ok {
			validate = pyTruthy(raw)
		}
		outcome, err := manager.ApplyBest(ctx, asString(payload["task_id"]), validate)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"success": true}
		for key, value := range outcome {
			result[key] = value
		}
		return result, nil
	case "get_report":
		format := "md"
		if text := asString(payload["format"]); strings.TrimSpace(text) != "" {
			format = text
		}
		report := manager.GetReport(asString(payload["task_id"]), format)
		message := ""
		if report == nil {
			message = "任务不存在"
		}
		return map[string]any{"success": report != nil, "report": report, "error": message}, nil
	}
	return map[string]any{"success": false, "error": "不支持的 tuning action: " + act}, nil
}

// importTaskResult 对齐 {"success": task is not None, "task": task, "error": ...}
func importTaskResult(task map[string]any) map[string]any {
	message := ""
	if task == nil {
		message = "任务不存在"
	}
	return map[string]any{"success": task != nil, "task": task, "error": message}
}

// importMergedResult 对齐 {"success": payload is not None, **payload, "error": ...}
func importMergedResult(payload map[string]any, missing string) map[string]any {
	result := map[string]any{"success": payload != nil}
	for key, value := range payload {
		result[key] = value
	}
	message := ""
	if payload == nil {
		message = missing
	}
	result["error"] = message
	return result
}

// importPayloadFiles 对齐 staged_files | files | uploads 的顺序取值
func importPayloadFiles(payload map[string]any) any {
	for _, key := range []string{"staged_files", "files", "uploads"} {
		if value, ok := payload[key]; ok && pyTruthy(value) {
			return value
		}
	}
	return nil
}

// importUploadFiles 把载荷中的文件描述转换为上传记录
// 支持暂存路径字典与字节内容两种形式，对齐 Python 对 dict 与文件对象的处理
func importUploadFiles(value any) ([]utils.WebImportUpload, error) {
	if value == nil {
		return nil, nil
	}
	items := asList(value)
	if items == nil {
		return nil, fmt.Errorf("上传文件必须是列表")
	}
	out := make([]utils.WebImportUpload, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, utils.WebImportUpload{StagedPath: text})
			continue
		}
		entry := asMap(item)
		if entry == nil {
			return nil, fmt.Errorf("上传文件项必须是对象或暂存路径")
		}
		upload := utils.WebImportUpload{
			Filename:   asString(entry["filename"]),
			StagedPath: asString(entry["staged_path"]),
		}
		if upload.Filename == "" {
			upload.Filename = asString(entry["name"])
		}
		if upload.StagedPath == "" {
			upload.StagedPath = asString(entry["path"])
		}
		if content, ok := entry["content"].([]byte); ok {
			upload.Content = content
		}
		out = append(out, upload)
	}
	return out, nil
}
