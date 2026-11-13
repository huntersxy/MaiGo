package utils

// Web 导入任务创建与查询，对齐 web_import_manager.py 的任务入口

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WebImportUpload 上传文件输入
type WebImportUpload struct {
	Filename   string
	StagedPath string
	Content    []byte
}

// webImportNewID 生成 uuid4().hex 风格标识
func webImportNewID() string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

// pendingTaskCount 对齐 _pending_task_count
func (m *WebImportManager) pendingTaskCount() int {
	count := 0
	for _, task := range m.tasks {
		switch task.Status {
		case WebImportTaskQueued, WebImportTaskPreparing, WebImportTaskRunning, WebImportTaskCancelRequested:
			count++
		}
	}
	return count
}

// registerTaskLocked 注册任务并加入队列
func (m *WebImportManager) registerTaskLocked(task *WebImportTaskRecord) {
	m.tasks[task.TaskID] = task
	m.taskOrder = append([]string{task.TaskID}, m.taskOrder...)
	m.queue = append(m.queue, task.TaskID)
}

// uploadStagingRoot 对齐 _resolve_upload_staging_root
func (m *WebImportManager) uploadStagingRoot() string {
	return filepath.Join(m.importRoot, "staging")
}

// CreateUploadTask 对齐 create_upload_task
func (m *WebImportManager) CreateUploadTask(files []WebImportUpload, payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	if err := m.ensureReady(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("至少需要上传一个文件")
	}
	params, err := m.normalizeParams(payload)
	if err != nil {
		return nil, err
	}
	if len(files) > m.maxFilesPerTask() {
		return nil, fmt.Errorf("单任务文件数超过上限: %d", m.maxFilesPerTask())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	task := NewWebImportTaskRecord(webImportNewID(), "upload", params, m.now())
	taskDir := filepath.Join(m.tempRoot, task.TaskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, err
	}
	maxSize := m.maxFileSizeBytes()
	stagingRoot, _ := filepath.Abs(m.uploadStagingRoot())
	for index, uploaded := range files {
		fileID := webImportNewID()
		name := ""
		tempPath := ""
		if uploaded.StagedPath != "" {
			stagedPath, err := filepath.Abs(uploaded.StagedPath)
			if err != nil {
				return nil, err
			}
			relative, relErr := filepath.Rel(stagingRoot, stagedPath)
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("上传暂存文件必须位于导入目录: %s", stagingRoot)
			}
			info, statErr := os.Stat(stagedPath)
			if statErr != nil || info.IsDir() {
				return nil, fmt.Errorf("上传暂存文件不存在: %s", stagedPath)
			}
			name = webImportSafeFilename(uploaded.Filename)
			if name == "" || uploaded.Filename == "" {
				name = webImportSafeFilename(filepath.Base(stagedPath))
			}
			ext := strings.ToLower(filepath.Ext(name))
			if ext != ".txt" && ext != ".md" && ext != ".json" {
				return nil, fmt.Errorf("不支持的文件类型: %s", name)
			}
			if info.Size() > maxSize {
				return nil, fmt.Errorf("文件超过大小限制: %s", name)
			}
			tempPath = filepath.Join(taskDir, fileID+"_"+name)
			data, readErr := os.ReadFile(stagedPath)
			if readErr != nil {
				return nil, readErr
			}
			if err := os.WriteFile(tempPath, data, 0o644); err != nil {
				return nil, err
			}
		} else {
			name = webImportSafeFilename(uploaded.Filename)
			if name == "" {
				name = webImportSafeFilename(fmt.Sprintf("file_%d.txt", index))
			}
			ext := strings.ToLower(filepath.Ext(name))
			if ext != ".txt" && ext != ".md" && ext != ".json" {
				return nil, fmt.Errorf("不支持的文件类型: %s", name)
			}
			if int64(len(uploaded.Content)) > maxSize {
				return nil, fmt.Errorf("文件超过大小限制: %s", name)
			}
			tempPath = filepath.Join(taskDir, fileID+"_"+name)
			if err := os.WriteFile(tempPath, uploaded.Content, 0o644); err != nil {
				return nil, err
			}
		}
		mode := importValueText(params["input_mode"])
		if strings.ToLower(filepath.Ext(name)) == ".json" {
			mode = "json"
		}
		file := NewWebImportFileRecord(fileID, name, "upload", mode, m.now())
		file.TempPath = tempPath
		task.Files = append(task.Files, file)
	}
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// CreatePasteTask 对齐 create_paste_task
func (m *WebImportManager) CreatePasteTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	if err := m.ensureReady(); err != nil {
		return nil, err
	}
	params, err := m.normalizeParams(payload)
	if err != nil {
		return nil, err
	}
	params["task_kind"] = "paste"
	content := ""
	if isTruthy(payload["content"]) {
		content = pyStr(payload["content"])
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("content 不能为空")
	}
	if len([]rune(content)) > m.maxPasteChars() {
		return nil, fmt.Errorf("粘贴内容超过限制: %d 字符", m.maxPasteChars())
	}
	name := ""
	if isTruthy(payload["name"]) {
		name = webImportSafeFilename(payload["name"])
	} else {
		name = fmt.Sprintf("paste_%d.txt", int64(m.now()))
	}
	if importValueText(params["input_mode"]) == "json" && strings.ToLower(filepath.Ext(name)) != ".json" {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".json"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	task := NewWebImportTaskRecord(webImportNewID(), "paste", params, m.now())
	inline := content
	file := NewWebImportFileRecord(webImportNewID(), name, "paste", importValueText(params["input_mode"]), m.now())
	file.InlineContent = &inline
	task.Files = append(task.Files, file)
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// CreateRawScanTask 对齐 create_raw_scan_task
func (m *WebImportManager) CreateRawScanTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	if err := m.ensureReady(); err != nil {
		return nil, err
	}
	params, err := m.normalizeRawScanParams(payload)
	if err != nil {
		return nil, err
	}
	sourcePath, err := m.ResolvePathAlias(importValueText(params["alias"]), importValueText(params["relative_path"]), true)
	if err != nil {
		return nil, err
	}
	files, err := m.scanFiles(sourcePath, webImportCoerceBool(params["recursive"], true), importValueText(params["glob"]), map[string]struct{}{".txt": {}, ".md": {}, ".json": {}})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("未找到可导入文件")
	}
	if len(files) > m.maxFilesPerTask() {
		return nil, fmt.Errorf("单任务文件数超过上限: %d", m.maxFilesPerTask())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	task := NewWebImportTaskRecord(webImportNewID(), "raw_scan", params, m.now())
	for _, path := range files {
		mode := importValueText(params["input_mode"])
		if strings.ToLower(filepath.Ext(path)) == ".json" {
			mode = "json"
		}
		file := NewWebImportFileRecord(webImportNewID(), filepath.Base(path), "raw_scan", mode, m.now())
		file.SourcePath = path
		task.Files = append(task.Files, file)
	}
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// CreateLpmmOpenieTask 对齐 create_lpmm_openie_task
func (m *WebImportManager) CreateLpmmOpenieTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	if err := m.ensureReady(); err != nil {
		return nil, err
	}
	params, err := m.normalizeLpmmOpenieParams(payload)
	if err != nil {
		return nil, err
	}
	sourcePath, err := m.ResolvePathAlias(importValueText(params["alias"]), importValueText(params["relative_path"]), true)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, err
	}
	files := []string{}
	if !info.IsDir() {
		files = []string{sourcePath}
	} else {
		files, err = m.scanFiles(sourcePath, true, "*-openie.json", map[string]struct{}{".json": {}})
		if err != nil {
			return nil, err
		}
		if len(files) == 0 && webImportCoerceBool(params["include_all_json"], false) {
			files, err = m.scanFiles(sourcePath, true, "*.json", map[string]struct{}{".json": {}})
			if err != nil {
				return nil, err
			}
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("未找到 LPMM OpenIE JSON 文件")
	}
	if len(files) > m.maxFilesPerTask() {
		return nil, fmt.Errorf("单任务文件数超过上限: %d", m.maxFilesPerTask())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	task := NewWebImportTaskRecord(webImportNewID(), "lpmm_openie", params, m.now())
	task.SchemaDetected = "lpmm_openie"
	for _, path := range files {
		file := NewWebImportFileRecord(webImportNewID(), filepath.Base(path), "lpmm_openie", "json", m.now())
		file.SourcePath = path
		task.Files = append(task.Files, file)
	}
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// CreateTemporalBackfillTask 对齐 create_temporal_backfill_task
func (m *WebImportManager) CreateTemporalBackfillTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	params, err := m.normalizeTemporalBackfillParams(payload)
	if err != nil {
		return nil, err
	}
	targetPath := m.resolveDataDir()
	info, err := os.Stat(targetPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("temporal_backfill 目标路径必须为目录")
	}
	if metadataInfo, statErr := os.Stat(filepath.Join(targetPath, "metadata")); statErr != nil || !metadataInfo.IsDir() {
		return nil, fmt.Errorf("活动 A_Memorix Store 缺少 metadata 目录")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	task := NewWebImportTaskRecord(webImportNewID(), "temporal_backfill", params, m.now())
	file := NewWebImportFileRecord(webImportNewID(), fmt.Sprintf("temporal_backfill_%d", int64(m.now())), "temporal_backfill", "json", m.now())
	file.SourcePath = targetPath
	task.Files = append(task.Files, file)
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// CreateLpmmConvertTask 对齐 create_lpmm_convert_task
func (m *WebImportManager) CreateLpmmConvertTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	params, err := m.normalizeLpmmConvertParams(payload)
	if err != nil {
		return nil, err
	}
	sourcePath, err := m.ResolvePathAlias(importValueText(params["alias"]), importValueText(params["relative_path"]), true)
	if err != nil {
		return nil, err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil || !sourceInfo.IsDir() {
		return nil, fmt.Errorf("lpmm_convert 输入路径必须为目录")
	}
	targetPath, err := m.ResolvePathAlias(importValueText(params["target_alias"]), importValueText(params["target_relative_path"]), false)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		return nil, err
	}
	targetInfo, err := os.Stat(targetPath)
	if err != nil || !targetInfo.IsDir() {
		return nil, fmt.Errorf("lpmm_convert 目标路径必须为目录")
	}
	entries, _ := os.ReadDir(targetPath)
	if len(entries) > 0 {
		return nil, fmt.Errorf("lpmm_convert 目标目录必须为空，不会覆盖已有存储")
	}
	relative, relErr := filepath.Rel(sourcePath, targetPath)
	if targetPath == sourcePath || (relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return nil, fmt.Errorf("lpmm_convert 目标目录不能位于输入目录内")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	taskParams := map[string]any{}
	for key, value := range params {
		taskParams[key] = value
	}
	taskParams["source_path"] = sourcePath
	taskParams["target_path"] = targetPath
	task := NewWebImportTaskRecord(webImportNewID(), "lpmm_convert", taskParams, m.now())
	file := NewWebImportFileRecord(webImportNewID(), fmt.Sprintf("lpmm_convert_%d", int64(m.now())), "lpmm_convert", "json", m.now())
	file.SourcePath = sourcePath
	task.Files = append(task.Files, file)
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// CreateMaibotMigrationTask 对齐 create_maibot_migration_task
func (m *WebImportManager) CreateMaibotMigrationTask(payload map[string]any) (map[string]any, error) {
	if !m.isEnabled() {
		return nil, fmt.Errorf("导入功能已禁用")
	}
	if err := m.ensureReady(); err != nil {
		return nil, err
	}
	params, err := m.normalizeMigrationParams(payload)
	if err != nil {
		return nil, err
	}
	scriptPath := m.resolveMigrationScript()
	if !m.hasNativeScript("migrate_maibot_memory") {
		if _, err := os.Stat(scriptPath); err != nil {
			return nil, fmt.Errorf("迁移脚本不存在: %s", scriptPath)
		}
	}
	inline, _ := json.Marshal(params)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	task := NewWebImportTaskRecord(webImportNewID(), "maibot_migration", params, m.now())
	file := NewWebImportFileRecord(webImportNewID(), fmt.Sprintf("maibot_migration_%d", int64(m.now())), "maibot_migration", "text", m.now())
	content := string(inline)
	file.InlineContent = &content
	task.Files = append(task.Files, file)
	m.registerTaskLocked(task)
	m.ensureWorkerLocked()
	return task.ToSummary(), nil
}

// ListTasks 对齐 list_tasks
func (m *WebImportManager) ListTasks(limit int) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := limit
	if max < 1 {
		max = 1
	}
	out := []map[string]any{}
	for index, taskID := range m.taskOrder {
		if index >= max {
			break
		}
		if task, ok := m.tasks[taskID]; ok {
			out = append(out, task.ToSummary())
		}
	}
	return out
}

// GetTask 对齐 get_task
func (m *WebImportManager) GetTask(taskID string, includeChunks bool) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return nil
	}
	return task.ToDetail(includeChunks)
}

// GetChunks 对齐 get_chunks
func (m *WebImportManager) GetChunks(taskID string, fileID string, offset int, limit int) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return nil
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return nil
	}
	start := offset
	if start < 0 {
		start = 0
	}
	size := limit
	if size < 1 {
		size = 1
	}
	if size > 500 {
		size = 500
	}
	end := start + size
	if end > len(file.Chunks) {
		end = len(file.Chunks)
	}
	items := []any{}
	if start < len(file.Chunks) {
		for _, chunk := range file.Chunks[start:end] {
			items = append(items, chunk.ToDict())
		}
	}
	return map[string]any{
		"task_id": taskID,
		"file_id": fileID,
		"offset":  start,
		"limit":   size,
		"total":   len(file.Chunks),
		"file":    file.ToDict(false),
		"items":   items,
	}
}

// CancelTask 对齐 cancel_task
func (m *WebImportManager) CancelTask(taskID string) map[string]any {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	now := m.now()
	if task.Status == WebImportTaskQueued {
		task.CancelRequestedAt = &now
		task.CancelOrigin = "user_request"
		m.markTaskCancelledLocked(task, "任务已取消")
		filtered := []string{}
		for _, id := range m.queue {
			if id != taskID {
				filtered = append(filtered, id)
			}
		}
		m.queue = filtered
	} else if task.Status == WebImportTaskPreparing || task.Status == WebImportTaskRunning {
		task.CancelRequestedAt = &now
		task.CancelOrigin = "user_request"
		task.Status = WebImportTaskCancelRequested
		task.CurrentStep = "cancel_requested"
		task.UpdatedAt = now
	}
	summary := task.ToSummary()
	m.mu.Unlock()
	m.tryWriteTaskReport(task)
	return summary
}

// markTaskCancelledLocked 对齐 _mark_task_cancelled_locked
func (m *WebImportManager) markTaskCancelledLocked(task *WebImportTaskRecord, reason string) {
	for _, file := range task.Files {
		if file.Status == WebImportFileCompleted || file.Status == WebImportFileFailed || file.Status == WebImportFileCancelled {
			continue
		}
		file.Status = WebImportFileCancelled
		file.CurrentStep = "cancelled"
		file.Error = reason
		additional := 0
		for _, chunk := range file.Chunks {
			if chunk.Status == WebImportChunkCompleted || chunk.Status == WebImportChunkFailed || chunk.Status == WebImportChunkCancelled {
				continue
			}
			chunk.Status = WebImportChunkCancelled
			chunk.Step = "cancelled"
			chunk.Retryable = false
			chunk.Error = reason
			chunk.Progress = 1
			chunk.UpdatedAt = m.now()
			additional++
		}
		file.CancelledChunks += additional
		webImportRecomputeFileProgress(file)
		file.UpdatedAt = m.now()
	}
	task.Status = WebImportTaskCancelled
	task.CurrentStep = "cancelled"
	finished := m.now()
	task.FinishedAt = &finished
	task.UpdatedAt = finished
	m.webImportRecomputeTaskProgress(task)
}

// Shutdown 对齐 shutdown
func (m *WebImportManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.stopping = true
	for _, task := range m.tasks {
		switch task.Status {
		case WebImportTaskQueued, WebImportTaskPreparing, WebImportTaskRunning, WebImportTaskCancelRequested:
			now := m.now()
			task.CancelRequestedAt = &now
			task.CancelOrigin = "runtime_shutdown"
			m.markTaskCancelledLocked(task, "服务关闭")
		}
	}
	m.queue = nil
	m.mu.Unlock()
	m.cleanupTempRoot()
}
