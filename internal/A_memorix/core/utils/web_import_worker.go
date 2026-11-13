package utils

// Web 导入 worker 与状态流转，对齐 web_import_manager.py 的调度部分

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ensureWorker 对齐 _ensure_worker
func (m *WebImportManager) ensureWorker() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureWorkerLocked()
}

// ensureWorkerLocked 在持锁状态下启动 worker，避免调用方重复加锁
func (m *WebImportManager) ensureWorkerLocked() {
	if m.workerActive || m.stopping {
		return
	}
	m.workerActive = true
	go m.workerLoop(context.Background())
}

// workerLoop 对齐 _worker_loop
func (m *WebImportManager) workerLoop(ctx context.Context) {
	m.deps.Logger.Info("Web 导入任务 worker 已启动")
	defer func() {
		m.mu.Lock()
		m.workerActive = false
		m.mu.Unlock()
	}()
	for {
		m.mu.Lock()
		if m.stopping {
			m.mu.Unlock()
			break
		}
		taskID := ""
		for len(m.queue) > 0 {
			candidate := m.queue[0]
			m.queue = m.queue[1:]
			task, ok := m.tasks[candidate]
			if !ok || task.Status == WebImportTaskCancelled {
				continue
			}
			taskID = candidate
			m.activeTaskID = candidate
			break
		}
		m.mu.Unlock()
		if taskID == "" {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		m.runTask(ctx, taskID)
		m.mu.Lock()
		if m.activeTaskID == taskID {
			m.activeTaskID = ""
		}
		m.mu.Unlock()
	}
	m.deps.Logger.Info("Web 导入任务 worker 已停止")
}

// IsWriteBlocked 对齐 is_write_blocked
func (m *WebImportManager) IsWriteBlocked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	taskID := m.activeTaskID
	if taskID == "" {
		return false
	}
	task, ok := m.tasks[taskID]
	if !ok {
		return false
	}
	return task.Status == WebImportTaskPreparing || task.Status == WebImportTaskRunning || task.Status == WebImportTaskCancelRequested
}

// ProcessNext 同步执行一个排队任务，供测试和单次驱动使用
func (m *WebImportManager) ProcessNext(ctx context.Context) bool {
	m.mu.Lock()
	if len(m.queue) == 0 {
		m.mu.Unlock()
		return false
	}
	taskID := m.queue[0]
	m.queue = m.queue[1:]
	m.activeTaskID = taskID
	m.mu.Unlock()
	m.runTask(ctx, taskID)
	m.mu.Lock()
	if m.activeTaskID == taskID {
		m.activeTaskID = ""
	}
	m.mu.Unlock()
	return true
}

// runTask 对齐 _run_task
func (m *WebImportManager) runTask(ctx context.Context, taskID string) {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	if !ok || task.Status == WebImportTaskCancelled {
		m.mu.Unlock()
		return
	}
	task.Status = WebImportTaskPreparing
	task.CurrentStep = "preparing"
	now := m.now()
	task.StartedAt = &now
	task.UpdatedAt = now
	clearManifest := webImportCoerceBool(task.Params["clear_manifest"], false)
	m.mu.Unlock()
	if clearManifest {
		m.clearManifest()
	}
	m.mu.Lock()
	task = m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return
	}
	if task.Status == WebImportTaskCancelRequested {
		m.markTaskCancelledLocked(task, "任务已取消")
		m.mu.Unlock()
		m.tryWriteTaskReport(task)
		return
	}
	task.Status = WebImportTaskRunning
	task.CurrentStep = "running"
	task.UpdatedAt = m.now()
	taskKind := strings.ToLower(strings.TrimSpace(importValueText(task.Params["task_kind"])))
	if taskKind == "" {
		taskKind = strings.ToLower(task.Source)
	}
	m.mu.Unlock()

	var runErr error
	if taskKind == "maibot_migration" || taskKind == "temporal_backfill" || taskKind == "lpmm_convert" {
		m.mu.Lock()
		current := m.tasks[taskID]
		var firstFile *WebImportFileRecord
		if current != nil && len(current.Files) > 0 {
			firstFile = current.Files[0]
		}
		m.mu.Unlock()
		switch {
		case firstFile == nil && taskKind == "maibot_migration":
			runErr = fmt.Errorf("迁移任务缺少文件记录")
		case firstFile == nil && taskKind == "temporal_backfill":
			runErr = fmt.Errorf("回填任务缺少文件记录")
		case firstFile == nil:
			runErr = fmt.Errorf("转换任务缺少文件记录")
		case taskKind == "maibot_migration":
			m.processMaibotMigration(ctx, taskID, firstFile)
		case taskKind == "temporal_backfill":
			m.processTemporalBackfill(ctx, taskID, firstFile)
		default:
			m.processLpmmConvert(ctx, taskID, firstFile)
		}
	} else {
		runErr = m.processTaskFiles(ctx, taskID)
	}
	if runErr != nil {
		m.deps.Logger.Error(fmt.Sprintf("导入任务执行失败 task=%s: %v", taskID, runErr))
	}

	m.mu.Lock()
	task = m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return
	}
	if runErr != nil && task.Status != WebImportTaskCancelled && task.Status != WebImportTaskCompleted && task.Status != WebImportTaskCompletedWithErrors {
		task.Status = WebImportTaskFailed
		task.CurrentStep = "failed"
		task.Error = runErr.Error()
		finished := m.now()
		task.FinishedAt = &finished
		task.UpdatedAt = finished
	}
	m.webImportRecomputeTaskProgress(task)
	hasFailed := false
	hasCancelled := false
	hasCompleted := false
	for _, file := range task.Files {
		if file.Status == WebImportFileFailed || file.FailedChunks > 0 || strings.TrimSpace(file.Error) != "" {
			hasFailed = true
		}
		if file.Status == WebImportFileCancelled {
			hasCancelled = true
		}
		if file.Status == WebImportFileCompleted {
			hasCompleted = true
		}
	}
	if task.Status != WebImportTaskFailed {
		switch {
		case hasFailed && !hasCancelled:
			task.Status = WebImportTaskCompletedWithErrors
			task.CurrentStep = "completed_with_errors"
		case hasCancelled && !hasCompleted:
			task.Status = WebImportTaskCancelled
			task.CurrentStep = "cancelled"
		case hasCancelled && hasCompleted:
			task.Status = WebImportTaskCancelled
			task.CurrentStep = "cancelled"
		default:
			task.Status = WebImportTaskCompleted
			task.CurrentStep = "completed"
		}
	}
	finished := m.now()
	task.FinishedAt = &finished
	task.UpdatedAt = finished
	taskKind = strings.ToLower(strings.TrimSpace(importValueText(task.Params["task_kind"])))
	if taskKind == "" {
		taskKind = strings.ToLower(task.Source)
	}
	writeKinds := map[string]struct{}{"upload": {}, "paste": {}, "raw_scan": {}, "lpmm_openie": {}, "maibot_migration": {}, "lpmm_convert": {}}
	hasWritten := task.DoneChunks > 0
	for _, file := range task.Files {
		if file.DoneChunks > 0 {
			hasWritten = true
		}
	}
	var notify map[string]any
	if _, ok := writeKinds[taskKind]; ok && hasWritten {
		notify = map[string]any{
			"task_id":     task.TaskID,
			"task_kind":   taskKind,
			"status":      task.Status,
			"done_chunks": task.DoneChunks,
			"finished_at": task.FinishedAt,
		}
	}
	m.mu.Unlock()
	m.tryWriteTaskReport(task)
	if notify != nil && m.deps.WriteChanged != nil {
		m.deps.WriteChanged(notify)
	}
}

// processTaskFiles 对齐普通文件任务的并发调度
func (m *WebImportManager) processTaskFiles(ctx context.Context, taskID string) error {
	m.mu.Lock()
	task := m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return nil
	}
	files := append([]*WebImportFileRecord{}, task.Files...)
	fileLimit := webImportCoerceInt(task.Params["file_concurrency"], m.defaultFileConcurrency())
	chunkLimit := webImportCoerceInt(task.Params["chunk_concurrency"], m.defaultChunkConcurrency())
	m.mu.Unlock()
	fileSem := make(chan struct{}, webImportMaxInt(1, fileLimit))
	chunkSem := make(chan struct{}, webImportMaxInt(1, chunkLimit))
	var wg sync.WaitGroup
	for _, file := range files {
		wg.Add(1)
		go func(file *WebImportFileRecord) {
			defer wg.Done()
			fileSem <- struct{}{}
			defer func() { <-fileSem }()
			if err := m.processFile(ctx, taskID, file, chunkSem); err != nil {
				m.setFileFailed(taskID, file.FileID, fmt.Sprintf("文件处理失败: %v", err))
			}
		}(file)
	}
	wg.Wait()
	return nil
}

// processFile 对齐 _process_file
func (m *WebImportManager) processFile(ctx context.Context, taskID string, file *WebImportFileRecord, chunkSem chan struct{}) error {
	m.setFileState(taskID, file.FileID, WebImportFilePreparing, "preparing")
	if m.isCancelRequested(taskID) {
		m.setFileCancelled(taskID, file.FileID, "任务已取消")
		return nil
	}
	content, err := m.readFileContent(file)
	if err != nil {
		return err
	}
	contentHash := webImportMD5(content)
	m.mu.Lock()
	file.ContentHash = contentHash
	task := m.tasks[taskID]
	dedupePolicy := "none"
	force := false
	if task != nil {
		dedupePolicy = importValueText(task.Params["dedupe_policy"])
		if dedupePolicy == "" {
			dedupePolicy = "none"
		}
		force = webImportCoerceBool(task.Params["force"], false)
	}
	m.mu.Unlock()
	if dedupePolicy != "none" && !force && m.isManifestHit(file, contentHash, dedupePolicy) {
		m.mu.Lock()
		if task := m.tasks[taskID]; task != nil {
			if current := webImportFindFile(task, file.FileID); current != nil {
				current.Status = WebImportFileCompleted
				current.CurrentStep = "skipped"
				current.Progress = 1
				current.TotalChunks = 0
				current.DoneChunks = 0
				current.FailedChunks = 0
				current.CancelledChunks = 0
				current.DetectedStrategyType = "skipped"
				current.Error = ""
				current.UpdatedAt = m.now()
			}
			m.webImportRecomputeTaskProgress(task)
		}
		m.mu.Unlock()
		return nil
	}
	if file.InputMode == "json" {
		err = m.processJSONFile(ctx, taskID, file, content, chunkSem)
	} else {
		err = m.processTextFile(ctx, taskID, file, content, chunkSem)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	task = m.tasks[taskID]
	completed := false
	if task != nil {
		if current := webImportFindFile(task, file.FileID); current != nil {
			completed = current.Status == WebImportFileCompleted
		}
	}
	m.mu.Unlock()
	if dedupePolicy != "none" && completed {
		m.recordManifestImport(file, contentHash, dedupePolicy, taskID)
	}
	return nil
}

// readFileContent 对齐 _read_file_content
func (m *WebImportManager) readFileContent(file *WebImportFileRecord) (string, error) {
	if file.InlineContent != nil {
		return *file.InlineContent, nil
	}
	for _, path := range []string{file.SourcePath, file.TempPath} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", readErr
			}
			return string(data), nil
		}
	}
	return "", fmt.Errorf("读取文件失败：输入内容缺失")
}

// isCancelRequested 对齐 _is_cancel_requested
func (m *WebImportManager) isCancelRequested(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return true
	}
	return task.Status == WebImportTaskCancelRequested
}

// shouldCleanupTaskTemp 对齐 _should_cleanup_task_temp
func (m *WebImportManager) shouldCleanupTaskTemp(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return true
	}
	for _, file := range task.Files {
		if file.Status == WebImportFileFailed {
			return false
		}
	}
	return true
}

// cleanupTaskTempFiles 对齐 _cleanup_task_temp_files
func (m *WebImportManager) cleanupTaskTempFiles(taskID string) {
	taskDir := filepath.Join(m.tempRoot, taskID)
	if _, err := os.Stat(taskDir); err != nil {
		return
	}
	if err := os.RemoveAll(taskDir); err != nil {
		m.deps.Logger.Warning(fmt.Sprintf("清理任务临时文件失败 task=%s: %v", taskID, err))
	}
}

// cleanupTempRoot 对齐 _cleanup_temp_root
func (m *WebImportManager) cleanupTempRoot() {
	if _, err := os.Stat(m.tempRoot); err != nil {
		return
	}
	if err := os.RemoveAll(m.tempRoot); err != nil {
		m.deps.Logger.Warning(fmt.Sprintf("清理临时导入目录失败: %v", err))
	}
}

// taskReportPath 对齐 _task_report_path
func (m *WebImportManager) taskReportPath(taskID string) string {
	_ = os.MkdirAll(m.reportsRoot, 0o755)
	return filepath.Join(m.reportsRoot, taskID+"_summary.json")
}

// writeTaskReport 对齐 _write_task_report
func (m *WebImportManager) writeTaskReport(task *WebImportTaskRecord) error {
	path := m.taskReportPath(task.TaskID)
	task.ArtifactPaths["summary"] = path
	payload := task.ToDetail(false)
	payload["generated_at"] = m.now()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tempPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+webImportNewID()+".tmp")
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(tempPath)
	return os.Rename(tempPath, path)
}

// tryWriteTaskReport 对齐 _try_write_task_report
func (m *WebImportManager) tryWriteTaskReport(task *WebImportTaskRecord) {
	if err := m.writeTaskReport(task); err != nil {
		m.deps.Logger.Warning(fmt.Sprintf("写入任务终态报告失败 task=%s status=%s origin=%s: %v", task.TaskID, task.Status, task.CancelOrigin, err))
	}
}
