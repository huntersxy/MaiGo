package webuiapi

// WebUI 导入、Bundle、图片记忆与检索调优路由

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"MaiGo/internal/webui/routers/chat"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"

	"MaiGo/internal/A_memorix"
	imagememory "MaiGo/internal/A_memorix/core/image"
	"MaiGo/internal/chat/session"
)

func memoryRandomHex() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().Format("20060102150405")
	}
	return hex.EncodeToString(buf)
}

func (d Deps) handleMemoryImportSettings(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "get_settings", map[string]any{})
}

func (d Deps) handleMemoryImportPathAliases(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "get_path_aliases", map[string]any{})
}

func (d Deps) handleMemoryImportChatTargets(w http.ResponseWriter, r *http.Request) {
	db := d.chatDeps().ChatDB()
	if db == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "会话数据库尚未就绪")
		return
	}
	var records []chatsession.Record
	if err := db.WithContext(r.Context()).Model(&chatsession.Record{}).
		Order("CASE WHEN last_active_timestamp IS NULL THEN 1 ELSE 0 END").
		Order("last_active_timestamp DESC").Order("created_timestamp DESC").Order("id DESC").
		Find(&records).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			records = nil
		} else {
			writeAPIError(w, http.StatusInternalServerError, "获取导入聊天流失败: "+err.Error())
			return
		}
	}
	targets := make([]map[string]any, 0, len(records))
	for index := range records {
		rec := &records[index]
		sessionID := strings.TrimSpace(rec.SessionID)
		if sessionID == "" {
			continue
		}
		var latest *chat.LatestChatMessage
		if (rec.IsGroup() && (rec.GroupName == nil || strings.TrimSpace(*rec.GroupName) == "")) ||
			(!rec.IsGroup() && rec.UserNickname == nil && rec.UserCardname == nil) {
			latest = d.chatDeps().LatestChatMessage(r.Context(), sessionID)
		}
		var lastActive any
		if rec.LastActiveTimestamp != nil {
			lastActive = float64(rec.LastActiveTimestamp.UnixNano()) / 1e9
		}
		targets = append(targets, map[string]any{
			"chat_id":        sessionID,
			"chat_name":      chat.ChatDisplayName(rec, latest),
			"platform":       rec.Platform,
			"group_id":       chatTargetIDGroup(rec),
			"user_id":        rec.UserID,
			"account_id":     rec.AccountID,
			"scope":          rec.Scope,
			"is_group":       rec.IsGroup(),
			"last_active_at": lastActive,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": targets})
}

func chatTargetIDGroup(rec *chatsession.Record) string {
	if rec == nil || rec.GroupID == nil {
		return ""
	}
	return strings.TrimSpace(*rec.GroupID)
}

func (d Deps) handleMemoryImportGuide(w http.ResponseWriter, r *http.Request) {
	result, err := callMemoryAdmin(r, "memory_import_admin", "get_guide", map[string]any{})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	payload, ok := result.(map[string]any)
	if !ok {
		payload = map[string]any{"success": false, "error": "invalid_payload"}
	}
	if _, ok := payload["content"].(string); ok {
		writeJSON(w, http.StatusOK, payload)
		return
	}
	// 对齐 _import_guide：没有 content 时按 settings 生成说明并补齐 source/path
	settings, _ := payload["settings"].(map[string]any)
	if settings == nil {
		if settingsPayload, settingsErr := callMemoryAdmin(r, "memory_import_admin", "get_settings", map[string]any{}); settingsErr == nil {
			if mapped, ok := settingsPayload.(map[string]any); ok {
				settings, _ = mapped["settings"].(map[string]any)
			}
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"source":   "local",
		"path":     "generated://memory_import_guide",
		"content":  buildImportGuideMarkdown(settings),
		"settings": settings,
	})
}

// buildImportGuideMarkdown 对齐 _build_import_guide_markdown
func buildImportGuideMarkdown(settings map[string]any) string {
	aliases, _ := settings["path_aliases"].(map[string]any)
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	aliasLines := make([]string, 0, len(names))
	for _, name := range names {
		path := strings.TrimSpace(anyTrimmed(aliases[name]))
		if strings.TrimSpace(name) == "" || path == "" {
			continue
		}
		aliasLines = append(aliasLines, "- `"+name+"` -> `"+path+"`")
	}
	if len(aliasLines) == 0 {
		aliasLines = []string{"- 当前未配置路径别名"}
	}
	lines := []string{
		"# 长期记忆导入说明",
		"",
		"支持的导入方式：",
		"- 上传文件：适合零散文档、日志、聊天导出文本。",
		"- 粘贴文本：适合一次性导入少量整理好的内容。",
		"- Raw Scan：扫描白名单目录内的原始文本文件。",
		"- LPMM OpenIE / Convert：处理既有 LPMM 数据。",
		"- Temporal Backfill：补回已有数据中的时间信息。",
		"- MaiBot Migration：从宿主数据库迁移历史聊天记忆。",
		"",
		"当前路径别名：",
	}
	lines = append(lines, aliasLines...)
	lines = append(lines,
		"",
		"执行建议：",
		"- 首次导入先小批量试跑，确认切分和抽取结果正常。",
		"- 大批量导入时优先关注任务状态、失败块与重试结果。",
		"- 若路径解析失败，请先检查路径别名与相对路径是否仍然有效。",
	)
	return strings.Join(lines, "\n")
}

func (d Deps) handleMemoryImportResolvePath(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "resolve_path", unwrapMemoryImportPayload(payload))
}

func (d Deps) handleMemoryImportCreate(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeMemoryJSON(w, r)
		if !ok {
			return
		}
		normalized := unwrapMemoryImportPayload(payload)
		if !d.validateBundleImportChatID(w, r.Context(), normalized) {
			return
		}
		d.invokeMemoryAdmin(w, r, "memory_import_admin", action, normalized)
	}
}

// unwrapMemoryImportPayload 对齐 _unwrap_payload：优先取嵌套的 payload 子对象
func unwrapMemoryImportPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	if nested, ok := payload["payload"].(map[string]any); ok {
		out := make(map[string]any, len(nested))
		for key, value := range nested {
			out[key] = value
		}
		return out
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}

// handleMemoryImportUpload 接收 multipart 文件并落到 runtime 数据目录的临时 staging
func (d Deps) handleMemoryImportUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "解析上传失败: "+err.Error())
		return
	}
	payload := map[string]any{}
	if raw := strings.TrimSpace(r.FormValue("payload_json")); raw != "" {
		var decoded map[string]any
		if err := sonic.Unmarshal([]byte(raw), &decoded); err != nil {
			writeAPIError(w, http.StatusBadRequest, "payload_json 必须是 JSON 对象")
			return
		}
		payload = decoded
	}
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	stagingRoot := filepath.Join(service.GetRuntimeDataDir(), "imports", "staging")
	stagingDir := filepath.Join(stagingRoot, memoryRandomHex())
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "创建导入暂存目录失败: "+err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		_ = os.RemoveAll(stagingDir)
		writeValidationError(w, "至少需要上传一个文件")
		return
	}
	staged := make([]map[string]any, 0, len(files))
	for index, header := range files {
		source, err := header.Open()
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			writeAPIError(w, http.StatusBadRequest, "读取上传文件失败: "+err.Error())
			return
		}
		name := fmt.Sprintf("%03d_%s", index, filepath.Base(header.Filename))
		target := filepath.Join(stagingDir, name)
		destination, err := os.Create(target)
		if err != nil {
			_ = source.Close()
			_ = os.RemoveAll(stagingDir)
			writeAPIError(w, http.StatusInternalServerError, "写入导入暂存文件失败: "+err.Error())
			return
		}
		written, copyErr := io.Copy(destination, source)
		_ = destination.Close()
		_ = source.Close()
		if copyErr != nil {
			_ = os.RemoveAll(stagingDir)
			writeAPIError(w, http.StatusInternalServerError, "写入导入暂存文件失败: "+copyErr.Error())
			return
		}
		staged = append(staged, map[string]any{
			"filename": filepath.Base(header.Filename), "staged_path": target, "size": written,
		})
	}
	payload["staged_files"] = staged
	payload["staging_dir"] = stagingDir
	// 对齐 Python：登记任务后无论成败都清理暂存目录
	defer func() { _ = os.RemoveAll(stagingDir) }()
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "create_upload", payload)
}

func (d Deps) handleMemoryImportTasks(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	result, err := callMemoryAdmin(r, "memory_import_admin", "list", map[string]any{"limit": limit})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	listing, ok := result.(map[string]any)
	if !ok {
		listing = map[string]any{"success": false, "items": []any{}}
	}
	if _, exists := listing["success"]; !exists {
		listing["success"] = true
	}
	if _, exists := listing["items"]; !exists {
		listing["items"] = []any{}
	}
	// 对齐 _import_list：列表额外附带 settings
	settingsPayload, settingsErr := callMemoryAdmin(r, "memory_import_admin", "get_settings", map[string]any{})
	if settingsErr == nil {
		if mapped, ok := settingsPayload.(map[string]any); ok {
			if settings, ok := mapped["settings"].(map[string]any); ok {
				listing["settings"] = settings
			}
		}
	}
	writeJSON(w, http.StatusOK, listing)
}

func (d Deps) handleMemoryImportTaskGet(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	includeChunks, ok := mqBool(w, r, "include_chunks", false)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "get", map[string]any{
		"task_id": taskID, "include_chunks": includeChunks,
	})
}

func (d Deps) handleMemoryImportTaskChunks(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	fileID := strings.TrimSpace(chiURLParam(r, "file_id"))
	if taskID == "" || fileID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 与 file_id 不能为空")
		return
	}
	offset, ok := mqInt(w, r, "offset", 0, 0, 1<<30)
	if !ok {
		return
	}
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "get_chunks", map[string]any{
		"task_id": taskID, "file_id": fileID, "offset": offset, "limit": limit,
	})
}

func (d Deps) handleMemoryImportTaskCancel(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "cancel", map[string]any{"task_id": taskID})
}

func (d Deps) handleMemoryImportTaskRetry(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	args := map[string]any{"task_id": taskID}
	if overrides, exists := payload["overrides"]; exists {
		args["overrides"] = overrides
	}
	d.invokeMemoryAdmin(w, r, "memory_import_admin", "retry_failed", args)
}

func (d Deps) handleMemoryBundleExport(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	timeout := 600000
	d.invokeMemoryAdminTimeout(w, r, "memory_bundle_admin", "export", payload, &timeout)
}

func (d Deps) handleMemoryBundleList(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_bundle_admin", "list", map[string]any{"limit": limit})
}

func (d Deps) handleMemoryBundleUninstall(w http.ResponseWriter, r *http.Request) {
	installationID := strings.TrimSpace(chiURLParam(r, "installation_id"))
	if installationID == "" {
		writeAPIError(w, http.StatusBadRequest, "installation_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_bundle_admin", "uninstall", map[string]any{"installation_id": installationID})
}

func (d Deps) handleMemoryBundleImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "解析上传失败: "+err.Error())
		return
	}
	preview, previewErr := parseBundleImportPreview(r)
	if previewErr != nil {
		writeRequestValidationError(w, *previewErr)
		return
	}
	service := memoryHostService()
	if service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 宿主服务尚未就绪")
		return
	}
	payload := map[string]any{}
	if raw := strings.TrimSpace(r.FormValue("payload_json")); raw != "" {
		if err := sonic.Unmarshal([]byte(raw), &payload); err != nil {
			writeAPIError(w, http.StatusBadRequest, "payload_json 必须是 JSON 对象")
			return
		}
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeValidationError(w, "缺少 bundle 文件")
		return
	}
	defer file.Close()
	stagingDir := filepath.Join(service.GetRuntimeDataDir(), "imports", "staging", memoryRandomHex())
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "创建导入暂存目录失败: "+err.Error())
		return
	}
	target := filepath.Join(stagingDir, filepath.Base(header.Filename))
	destination, err := os.Create(target)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "写入暂存文件失败: "+err.Error())
		return
	}
	if _, err := io.Copy(destination, file); err != nil {
		_ = destination.Close()
		writeAPIError(w, http.StatusInternalServerError, "写入暂存文件失败: "+err.Error())
		return
	}
	_ = destination.Close()
	payload = unwrapMemoryBundlePayload(payload)
	if !d.validateBundleImportChatID(w, r.Context(), payload) {
		return
	}
	if preview {
		d.invokeMemoryAdmin(w, r, "memory_bundle_admin", "inspect", map[string]any{"path": target})
		return
	}
	args := cloneMemoryPayload(payload)
	if args == nil {
		args = map[string]any{}
	}
	args["path"] = target
	d.invokeMemoryAdmin(w, r, "memory_bundle_admin", "import", args)
}

// unwrapMemoryBundlePayload 对齐 ../MaiBot/src/webui/routers/memory.py:370-377 的 _unwrap_payload
// 前端偶尔把安装参数放在 payload 子对象里，A_memorix 服务契约只认扁平字段
func unwrapMemoryBundlePayload(payload map[string]any) map[string]any {
	if nested, ok := payload["payload"].(map[string]any); ok {
		return nested
	}
	return payload
}

// parseBundleImportPreview 按 FastAPI Form(bool) 的 Pydantic 宽松规则解析 preview
// 空字符串按未提供处理（FastAPI 表单实测返回默认值 false），非法值在请求校验阶段返回 422
// loc 与 FastAPI 一致为 ["body","preview"]
func parseBundleImportPreview(r *http.Request) (bool, *fastAPIValidationError) {
	if r.MultipartForm == nil {
		return false, nil
	}
	values := r.MultipartForm.Value["preview"]
	if len(values) == 0 {
		return false, nil
	}
	raw := values[len(values)-1]
	// FastAPI Form(bool) 把空字符串视为未提供，使用默认值 false
	if raw == "" {
		return false, nil
	}
	parsed, ok := parsePydanticBool(raw)
	if ok {
		return parsed, nil
	}
	return false, &fastAPIValidationError{
		Type:  "bool_parsing",
		Loc:   []any{"body", "preview"},
		Msg:   "Input should be a valid boolean, unable to interpret input",
		Input: raw,
	}
}

// validateBundleImportChatID 对齐 ../MaiBot/src/webui/routers/memory.py:452-469
// chat_id 非空时必须命中已有 chat_sessions，否则返回 400 聊天流不存在
func (d Deps) validateBundleImportChatID(w http.ResponseWriter, ctx context.Context, payload map[string]any) bool {
	chatID := strings.TrimSpace(anyTrimmed(payload["chat_id"]))
	if chatID == "" {
		delete(payload, "chat_id")
		return true
	}
	db := d.chatDeps().ChatDB()
	if db == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "会话数据库尚未就绪")
		return false
	}
	if _, err := chatsession.NewManager(db).GetExistingBySessionID(ctx, chatID); err != nil {
		if errors.Is(err, chatsession.ErrNotFound) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "聊天流不存在: " + chatID})
			return false
		}
		writeAPIError(w, http.StatusInternalServerError, "查询聊天流失败: "+err.Error())
		return false
	}
	payload["chat_id"] = chatID
	return true
}

func (d Deps) handleMemoryBundleDownload(w http.ResponseWriter, r *http.Request) {
	// 对齐 Python Path(file_name).name：只取 basename，避免路径穿越
	fileName := filepath.Base(strings.TrimSpace(chiURLParam(r, "file_name")))
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		writeAPIError(w, http.StatusBadRequest, "file_name 不能为空")
		return
	}
	result, err := callMemoryAdmin(r, "memory_bundle_admin", "resolve_file", map[string]any{"file_name": fileName})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: memory_bundle_admin/resolve_file")
		return
	}
	path := firstNonEmptyText(mapped["path"], firstNonEmptyText(mapped["file_path"], anyTrimmed(mapped["resolved_path"])))
	if path == "" {
		writeAPIError(w, http.StatusNotFound, "未找到 bundle 文件: "+fileName)
		return
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		writeAPIError(w, http.StatusNotFound, "未找到 bundle 文件: "+fileName)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.a-memorix.bundle+zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	http.ServeFile(w, r, path)
}

func (d Deps) handleMemoryImageInvoke(action string, build func(r *http.Request) (map[string]any, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{"action": action}
		if build != nil {
			extra, ok := build(r)
			if !ok {
				return
			}
			for key, value := range extra {
				payload[key] = value
			}
		}
		caller := memorixHostCaller()
		result, err := caller.invoke(r, "image_memory", action, payload)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if result == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: image_memory/"+action)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (d Deps) handleMemoryImagesList(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	offset, ok := mqInt(w, r, "offset", 0, 0, 1<<30)
	if !ok {
		return
	}
	d.handleMemoryImageInvoke("list", func(r *http.Request) (map[string]any, bool) {
		return map[string]any{"limit": limit, "offset": offset, "chat_id": mqString(r, "chat_id")}, true
	})(w, r)
}

func (d Deps) handleMemoryImagesGet(w http.ResponseWriter, r *http.Request) {
	assetID := strings.TrimSpace(chiURLParam(r, "asset_id"))
	if assetID == "" {
		writeAPIError(w, http.StatusBadRequest, "asset_id 不能为空")
		return
	}
	result, err := memorixHostCaller().invoke(r, "image_memory", "get", map[string]any{
		"action": "get", "asset_id": assetID, "chat_ids": nil,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	payload, _ := result.(map[string]any)
	if payload == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: image_memory/get")
		return
	}
	// 对齐 get_image_memory：为每条出现补 chat_name，空 chat_id 视为全局记忆
	occurrences, _ := payload["occurrences"].([]any)
	chatIDs := make([]string, 0, len(occurrences))
	for _, raw := range occurrences {
		occurrence, _ := raw.(map[string]any)
		if occurrence == nil {
			continue
		}
		if chatID := anyTrimmed(occurrence["chat_id"]); chatID != "" {
			chatIDs = append(chatIDs, chatID)
		}
	}
	names := d.memorySourceChatNames(r.Context(), chatIDs)
	for _, raw := range occurrences {
		occurrence, _ := raw.(map[string]any)
		if occurrence == nil {
			continue
		}
		chatID := anyTrimmed(occurrence["chat_id"])
		if chatID == "" {
			occurrence["chat_name"] = "全局记忆"
			continue
		}
		name := names[chatID]
		if name == "" {
			name = chatID
		}
		occurrence["chat_name"] = name
	}
	writeJSON(w, http.StatusOK, payload)
}

func (d Deps) handleMemoryImagesStatus(w http.ResponseWriter, r *http.Request) {
	d.handleMemoryImageInvoke("status", nil)(w, r)
}

func (d Deps) handleMemoryImagesChats(w http.ResponseWriter, r *http.Request) {
	result, err := memorixHostCaller().invoke(r, "image_memory", "list_chats", map[string]any{"action": "list_chats"})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	payload, _ := result.(map[string]any)
	if payload == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: image_memory/list_chats")
		return
	}
	rows, _ := payload["items"].([]any)
	chatIDs := make([]string, 0, len(rows))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		if chatID := anyTrimmed(row["chat_id"]); chatID != "" {
			chatIDs = append(chatIDs, chatID)
		}
	}
	names := d.memorySourceChatNames(r.Context(), chatIDs)
	items := make([]map[string]any, 0, len(rows))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		chatID := anyTrimmed(row["chat_id"])
		if chatID == "" {
			continue
		}
		name := names[chatID]
		if name == "" {
			name = chatID
		}
		items = append(items, map[string]any{
			"chat_id": chatID, "chat_name": name, "asset_count": anyInt(row["asset_count"]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "items": items})
}

func (d Deps) handleMemoryImageJobs(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	offset, ok := mqInt(w, r, "offset", 0, 0, 1<<30)
	if !ok {
		return
	}
	d.handleMemoryImageInvoke("jobs", func(r *http.Request) (map[string]any, bool) {
		return map[string]any{"limit": limit, "offset": offset, "status": mqString(r, "status")}, true
	})(w, r)
}

func (d Deps) handleMemoryImageJobsRetry(w http.ResponseWriter, r *http.Request) {
	d.handleMemoryImageInvoke("retry_failed_jobs", nil)(w, r)
}

func (d Deps) handleMemoryImagesReindex(w http.ResponseWriter, r *http.Request) {
	d.handleMemoryImageInvoke("process_jobs", nil)(w, r)
}

func (d Deps) handleMemoryImageObservation(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	occurrenceID, ok := bodyRequiredString(w, payload, "occurrence_id")
	if !ok {
		return
	}
	text, ok := bodyRequiredString(w, payload, "text")
	if !ok {
		return
	}
	confirmStatus := defaultStringField(payload, "confirm_status", "confirmed")
	supersedes := anyTrimmed(payload["supersedes_id"])
	d.handleMemoryImageInvoke("observe", func(r *http.Request) (map[string]any, bool) {
		return map[string]any{
			"occurrence_id": occurrenceID, "text": text,
			"confirm_status": confirmStatus, "supersedes_id": supersedes,
			"source_kind": "manual", "evidence": map[string]any{"source": "webui"},
		}, true
	})(w, r)
}

func (d Deps) handleMemoryImageOccurrenceDelete(w http.ResponseWriter, r *http.Request) {
	occurrenceID := strings.TrimSpace(chiURLParam(r, "occurrence_id"))
	if occurrenceID == "" {
		writeAPIError(w, http.StatusBadRequest, "occurrence_id 不能为空")
		return
	}
	d.handleMemoryImageInvoke("delete_occurrence", func(r *http.Request) (map[string]any, bool) {
		return map[string]any{"occurrence_id": occurrenceID}, true
	})(w, r)
}

func (d Deps) handleMemoryImageLinkDelete(w http.ResponseWriter, r *http.Request) {
	linkID := strings.TrimSpace(chiURLParam(r, "link_id"))
	if linkID == "" {
		writeAPIError(w, http.StatusBadRequest, "link_id 不能为空")
		return
	}
	d.handleMemoryImageInvoke("unlink", func(r *http.Request) (map[string]any, bool) {
		return map[string]any{"link_id": linkID}, true
	})(w, r)
}

func (d Deps) handleMemoryImageSearch(w http.ResponseWriter, r *http.Request) {
	assetID := strings.TrimSpace(chiURLParam(r, "asset_id"))
	if assetID == "" {
		writeAPIError(w, http.StatusBadRequest, "asset_id 不能为空")
		return
	}
	limit, ok := mqInt(w, r, "limit", 8, 1, 100)
	if !ok {
		return
	}
	threshold, ok := mqFloat(w, r, "threshold", 0.72, -1, 1)
	if !ok {
		return
	}
	chatID := strings.TrimSpace(mqString(r, "chat_id"))
	if chatID != "" {
		db := d.chatDeps().ChatDB()
		if db == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "会话数据库尚未就绪")
			return
		}
		if _, err := chatsession.NewManager(db).GetExistingBySessionID(r.Context(), chatID); err != nil {
			if errors.Is(err, chatsession.ErrNotFound) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "聊天流不存在"})
				return
			}
			writeAPIError(w, http.StatusInternalServerError, "查询聊天流失败: "+err.Error())
			return
		}
	}

	caller := memorixHostCaller()
	asset, err := caller.invoke(r, "image_memory", "get", map[string]any{
		"action": "get", "asset_id": assetID, "chat_ids": nil,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if asset == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: image_memory/get")
		return
	}
	assetPayload, _ := asset.(map[string]any)
	if assetPayload == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: image_memory/get")
		return
	}
	if success, _ := assetPayload["success"].(bool); !success {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "图片不存在"})
		return
	}
	assetBody, _ := assetPayload["asset"].(map[string]any)
	contentHash := anyTrimmed(assetBody["content_hash"])
	if contentHash == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "图片不存在"})
		return
	}

	payload := map[string]any{
		"action":               "search",
		"content_hash":         contentHash,
		"candidate_limit":      limit,
		"similarity_threshold": threshold,
	}
	if chatID != "" {
		payload["chat_ids"] = []string{chatID}
	}
	result, err := caller.invoke(r, "image_memory", "search", payload)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	resultPayload, _ := result.(map[string]any)
	if resultPayload == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 服务未接线: image_memory/search")
		return
	}
	if success, _ := resultPayload["success"].(bool); !success {
		detail := anyTrimmed(resultPayload["error"])
		if detail == "" {
			detail = "图片检索失败"
		}
		writeAPIError(w, http.StatusBadGateway, detail)
		return
	}
	d.decorateMemoryImageSearchHits(r.Context(), resultPayload)
	resultPayload["success"] = true
	writeJSON(w, http.StatusOK, resultPayload)
}

// decorateMemoryImageSearchHits 为命中项的每条出现补 chat_name，对齐 Python 搜索返回
func (d Deps) decorateMemoryImageSearchHits(ctx context.Context, result map[string]any) {
	hits, _ := result["hits"].([]any)
	var sessionIDs []string
	seen := map[string]struct{}{}
	for _, rawHit := range hits {
		hit, _ := rawHit.(map[string]any)
		occurrences, _ := hit["occurrences"].([]any)
		for _, rawOccurrence := range occurrences {
			occurrence, _ := rawOccurrence.(map[string]any)
			sessionID := anyTrimmed(occurrence["chat_id"])
			if sessionID == "" {
				continue
			}
			if _, exists := seen[sessionID]; exists {
				continue
			}
			seen[sessionID] = struct{}{}
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	names, err := d.memoryChatDisplayNames(ctx, sessionIDs)
	if err != nil {
		return
	}
	for _, rawHit := range hits {
		hit, _ := rawHit.(map[string]any)
		occurrences, _ := hit["occurrences"].([]any)
		for _, rawOccurrence := range occurrences {
			occurrence, _ := rawOccurrence.(map[string]any)
			sessionID := anyTrimmed(occurrence["chat_id"])
			if name, exists := names[sessionID]; exists && name != "" {
				occurrence["chat_name"] = name
				continue
			}
			occurrence["chat_name"] = "全局或来源已移除"
		}
	}
}

func (d Deps) handleMemoryImageContent(w http.ResponseWriter, r *http.Request) {
	assetID := strings.TrimSpace(chiURLParam(r, "asset_id"))
	if assetID == "" {
		writeValidationError(w, "asset_id 不能为空")
		return
	}
	kernel := runtimeKernel()
	if kernel == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 运行时尚未初始化")
		return
	}
	runtime, ok := kernel.ImageMemoryRuntime().(*imagememory.ImageMemoryRuntime)
	if !ok || runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "图片记忆未启用")
		return
	}
	store := runtime.MetadataStore()
	if store == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "图片记忆未启用")
		return
	}
	asset, err := store.GetImageAsset(assetID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if asset == nil || anyTrimmed(asset["status"]) != "active" {
		writeAPIError(w, http.StatusNotFound, "图片不存在")
		return
	}
	path, err := runtime.AssetStore().PathForRead(anyTrimmed(asset["storage_key"]))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "图片资产文件不存在")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "图片资产文件不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if mimeType := anyTrimmed(asset["mime_type"]); mimeType != "" {
		w.Header().Set("Content-Type", mimeType)
	}
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func (d Deps) handleMemoryTuningSettings(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "get_settings", map[string]any{})
}

func (d Deps) handleMemoryTuningProfile(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "get_profile", map[string]any{})
}

func (d Deps) handleMemoryTuningApplyProfile(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	profile, exists := payload["profile"]
	if !exists {
		writeValidationError(w, "profile 不能为空")
		return
	}
	validate := true
	if value, exists := payload["validate"]; exists && value != nil {
		if typed, ok := value.(bool); ok {
			validate = typed
		}
	}
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "apply_profile", map[string]any{
		"profile": profile, "reason": defaultStringField(payload, "reason", "manual"), "validate": validate,
	})
}

func (d Deps) handleMemoryTuningRollbackProfile(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "rollback_profile", map[string]any{})
}

func (d Deps) handleMemoryTuningExportProfile(w http.ResponseWriter, r *http.Request) {
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "export_profile", map[string]any{})
}

func (d Deps) handleMemoryTuningCreateTask(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "create_task", payload)
}

func (d Deps) handleMemoryTuningTasks(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "list_tasks", map[string]any{"limit": limit})
}

func (d Deps) handleMemoryTuningTaskGet(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	includeRounds, ok := mqBool(w, r, "include_rounds", false)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "get_task", map[string]any{
		"task_id": taskID, "include_rounds": includeRounds,
	})
}

func (d Deps) handleMemoryTuningTaskRounds(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	offset, ok := mqInt(w, r, "offset", 0, 0, 1<<30)
	if !ok {
		return
	}
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "get_rounds", map[string]any{
		"task_id": taskID, "offset": offset, "limit": limit,
	})
}

func (d Deps) handleMemoryTuningTaskCancel(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "task_id 不能为空")
		return
	}
	d.invokeMemoryAdmin(w, r, "memory_tuning_admin", "cancel", map[string]any{"task_id": taskID})
}

func (d Deps) handleMemoryTuningApplyBest(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	payload, ok := decodeMemoryJSON(w, r)
	if !ok {
		return
	}
	persist := false
	if value, exists := payload["persist"]; exists && value != nil {
		if typed, ok := value.(bool); ok {
			persist = typed
		}
	}
	validate := true
	if value, exists := payload["validate"]; exists && value != nil {
		if typed, ok := value.(bool); ok {
			validate = typed
		}
	}
	result, err := callMemoryAdmin(r, "memory_tuning_admin", "apply_best", map[string]any{
		"task_id": taskID, "validate": validate,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid_payload", "persisted": false})
		return
	}
	if _, exists := mapped["persisted"]; !exists {
		mapped["persisted"] = false
	}
	success, _ := mapped["success"].(bool)
	if !success || !persist {
		writeJSON(w, http.StatusOK, mapped)
		return
	}
	// 对齐 _tuning_apply_best：persist 时取运行时配置回写，并回报 persisted/persist_result
	runtimeResult, runtimeErr := callMemoryAdmin(r, "memory_runtime_admin", "get_config", map[string]any{})
	runtimePayload, _ := runtimeResult.(map[string]any)
	runtimeConfig, _ := runtimePayload["config"].(map[string]any)
	service := memoryHostService()
	if runtimeErr != nil || runtimeConfig == nil || service == nil {
		mapped["persisted"] = false
		mapped["persist_error"] = "runtime_config_unavailable"
		writeJSON(w, http.StatusOK, mapped)
		return
	}
	persistResult, persistErr := service.UpdateConfig(r.Context(), runtimeConfig)
	if persistErr != nil {
		mapped["persisted"] = false
		mapped["persist_error"] = "persist_failed: " + persistErr.Error()
		writeJSON(w, http.StatusOK, mapped)
		return
	}
	persisted, _ := persistResult["success"].(bool)
	mapped["persisted"] = persisted
	mapped["persist_result"] = persistResult
	writeJSON(w, http.StatusOK, mapped)
}

func (d Deps) handleMemoryTuningReport(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "task_id"))
	format := mqString(r, "format")
	if format == "" {
		format = "md"
	}
	result, err := callMemoryAdmin(r, "memory_tuning_admin", "get_report", map[string]any{
		"task_id": taskID, "format": format,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	// 对齐 _tuning_report：把 report 子对象展平到顶层
	payload, _ := result.(map[string]any)
	if payload == nil {
		payload = map[string]any{}
	}
	report, _ := payload["report"].(map[string]any)
	if report == nil {
		report = map[string]any{}
	}
	success, _ := payload["success"].(bool)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": success,
		"format":  firstNonEmptyText(report["format"], format),
		"content": firstNonEmptyText(report["content"], ""),
		"path":    firstNonEmptyText(report["path"], ""),
		"error":   firstNonEmptyText(payload["error"], ""),
	})
}

var _ = memorix.DefaultRegistry
