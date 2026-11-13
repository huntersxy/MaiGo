package utils

// Web 导入失败重试与取消收敛，对齐 web_import_manager.py 的重试构建

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// webImportRetryPlan 重试计划
type webImportRetryPlan struct {
	ChunkCandidates []webImportChunkRetryCandidate
	FileCandidates  []*WebImportFileRecord
	Skipped         []map[string]any
}

// webImportChunkRetryCandidate 分块级重试候选
type webImportChunkRetryCandidate struct {
	File    *WebImportFileRecord
	Indexes []int
}

// buildRetryPlan 对齐 _build_retry_plan
func (m *WebImportManager) buildRetryPlan(task *WebImportTaskRecord) webImportRetryPlan {
	plan := webImportRetryPlan{}
	for _, file := range task.Files {
		if file.Status == WebImportFileCancelled {
			continue
		}
		failedChunks := []*WebImportChunkRecord{}
		for _, chunk := range file.Chunks {
			if chunk.Status == WebImportChunkFailed {
				failedChunks = append(failedChunks, chunk)
			}
		}
		if file.Status == WebImportFileFailed && len(failedChunks) == 0 {
			plan.FileCandidates = append(plan.FileCandidates, file)
			continue
		}
		if len(failedChunks) == 0 {
			continue
		}
		indexes := []int{}
		hasNonRetryable := false
		for _, chunk := range failedChunks {
			failedAt := strings.ToLower(strings.TrimSpace(chunk.FailedAt))
			retryable := chunk.Retryable || (file.InputMode == "text" && failedAt == "extracting")
			if retryable {
				indexes = append(indexes, chunk.Index)
			} else {
				hasNonRetryable = true
			}
		}
		if hasNonRetryable {
			plan.FileCandidates = append(plan.FileCandidates, file)
			continue
		}
		sort.Ints(indexes)
		unique := []int{}
		for index, value := range indexes {
			if index > 0 && indexes[index-1] == value {
				continue
			}
			unique = append(unique, value)
		}
		if len(unique) > 0 {
			plan.ChunkCandidates = append(plan.ChunkCandidates, webImportChunkRetryCandidate{File: file, Indexes: unique})
		} else {
			plan.Skipped = append(plan.Skipped, map[string]any{"file_name": file.Name, "source_kind": file.SourceKind, "reason": "no_retryable_failed_chunks"})
		}
	}
	seen := map[string]struct{}{}
	uniqueFiles := []*WebImportFileRecord{}
	for _, file := range plan.FileCandidates {
		if _, ok := seen[file.FileID]; ok {
			continue
		}
		seen[file.FileID] = struct{}{}
		uniqueFiles = append(uniqueFiles, file)
	}
	plan.FileCandidates = uniqueFiles
	return plan
}

// cloneFailedFileForRetry 对齐 _clone_failed_file_for_retry
func (m *WebImportManager) cloneFailedFileForRetry(retryTask *WebImportTaskRecord, failedFile *WebImportFileRecord, taskDir string, retryMode string, retryIndexes []int) (bool, string) {
	sourceKind := strings.ToLower(strings.TrimSpace(failedFile.SourceKind))
	retryIndexesCopy := append([]int{}, retryIndexes...)
	now := m.now()
	switch sourceKind {
	case "upload":
		candidates := []string{}
		if failedFile.TempPath != "" {
			candidates = append(candidates, failedFile.TempPath)
		}
		if failedFile.SourcePath != "" {
			candidates = append(candidates, failedFile.SourcePath)
		}
		sourcePath := ""
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				sourcePath = candidate
				break
			}
		}
		if sourcePath == "" {
			return false, "upload_source_missing"
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return false, "upload_source_missing"
		}
		fileID := webImportNewID()
		name := webImportSafeFilename(failedFile.Name)
		destination := filepath.Join(taskDir, fileID+"_"+name)
		if err := os.WriteFile(destination, data, 0o644); err != nil {
			return false, "upload_source_missing"
		}
		record := NewWebImportFileRecord(fileID, name, "upload", failedFile.InputMode, now)
		record.TempPath = destination
		record.RetryMode = retryMode
		record.RetryChunkIndexes = retryIndexesCopy
		retryTask.Files = append(retryTask.Files, record)
		return true, ""
	case "paste":
		if failedFile.InlineContent == nil {
			return false, "paste_content_missing"
		}
		record := NewWebImportFileRecord(webImportNewID(), webImportSafeFilename(failedFile.Name), "paste", failedFile.InputMode, now)
		content := *failedFile.InlineContent
		record.InlineContent = &content
		record.RetryMode = retryMode
		record.RetryChunkIndexes = retryIndexesCopy
		retryTask.Files = append(retryTask.Files, record)
		return true, ""
	case "maibot_migration":
		record := NewWebImportFileRecord(webImportNewID(), webImportSafeFilename(failedFile.Name), "maibot_migration", "text", now)
		record.InlineContent = failedFile.InlineContent
		record.RetryMode = "file_fallback"
		record.RetryChunkIndexes = []int{}
		retryTask.Files = append(retryTask.Files, record)
		return true, ""
	case "raw_scan", "lpmm_openie", "lpmm_convert", "temporal_backfill":
		record := NewWebImportFileRecord(webImportNewID(), webImportSafeFilename(failedFile.Name), sourceKind, failedFile.InputMode, now)
		record.SourcePath = failedFile.SourcePath
		record.InlineContent = failedFile.InlineContent
		record.RetryMode = retryMode
		record.RetryChunkIndexes = retryIndexesCopy
		retryTask.Files = append(retryTask.Files, record)
		return true, ""
	default:
		return false, fmt.Sprintf("unsupported_source_kind:%s", func() string {
			if sourceKind == "" {
				return "unknown"
			}
			return sourceKind
		}())
	}
}

// RetryFailed 对齐 retry_failed
func (m *WebImportManager) RetryFailed(taskID string, overrides map[string]any) (map[string]any, error) {
	m.mu.Lock()
	task := m.tasks[taskID]
	if task == nil {
		m.mu.Unlock()
		return nil, nil
	}
	plan := m.buildRetryPlan(task)
	if len(plan.ChunkCandidates) == 0 && len(plan.FileCandidates) == 0 {
		m.mu.Unlock()
		return nil, fmt.Errorf("当前任务没有可重试失败项")
	}
	baseParams := map[string]any{}
	for key, value := range task.Params {
		baseParams[key] = value
	}
	taskKind := strings.ToLower(strings.TrimSpace(importValueText(task.Params["task_kind"])))
	source := task.Source
	schema := task.SchemaDetected
	retryID := webImportNewID()
	m.mu.Unlock()

	for key, value := range overrides {
		baseParams[key] = value
	}
	params, err := m.normalizeByTaskKind(taskKind, baseParams)
	if err != nil {
		return nil, err
	}
	params["retry_parent_task_id"] = taskID
	params["retry_strategy"] = "chunk_first_auto_file_fallback"

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingTaskCount() >= m.queueLimit() {
		return nil, fmt.Errorf("任务队列已满，请稍后重试")
	}
	retryTask := NewWebImportTaskRecord(retryID, source, params, m.now())
	retryTask.SchemaDetected = schema
	retryTask.RetryParentTaskID = taskID
	taskDir := filepath.Join(m.tempRoot, retryID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, err
	}
	summary := map[string]any{
		"chunk_retry_files":   0,
		"chunk_retry_chunks":  0,
		"file_fallback_files": 0,
		"skipped_files":       0,
		"parent_task_id":      taskID,
	}
	skipped := append([]map[string]any{}, plan.Skipped...)
	for _, candidate := range plan.ChunkCandidates {
		ok, reason := m.cloneFailedFileForRetry(retryTask, candidate.File, taskDir, "chunk", candidate.Indexes)
		if ok {
			summary["chunk_retry_files"] = summary["chunk_retry_files"].(int) + 1
			summary["chunk_retry_chunks"] = summary["chunk_retry_chunks"].(int) + len(candidate.Indexes)
		} else {
			skipped = append(skipped, map[string]any{"file_name": candidate.File.Name, "source_kind": candidate.File.SourceKind, "reason": reason})
		}
	}
	for _, file := range plan.FileCandidates {
		ok, reason := m.cloneFailedFileForRetry(retryTask, file, taskDir, "file_fallback", []int{})
		if ok {
			summary["file_fallback_files"] = summary["file_fallback_files"].(int) + 1
		} else {
			skipped = append(skipped, map[string]any{"file_name": file.Name, "source_kind": file.SourceKind, "reason": reason})
		}
	}
	summary["skipped_files"] = len(skipped)
	if len(skipped) > 0 {
		summary["skipped_details"] = skipped
	}
	retryTask.RetrySummary = summary
	if len(retryTask.Files) == 0 {
		return nil, fmt.Errorf("无可执行的重试输入：失败项均无法构建重试任务")
	}
	m.tasks[retryID] = retryTask
	m.taskOrder = append([]string{retryID}, m.taskOrder...)
	m.queue = append(m.queue, retryID)
	m.deps.Logger.Info(fmt.Sprintf("重试任务已创建 parent=%s retry=%s chunk_files=%d chunk_chunks=%d file_fallback=%d skipped=%d",
		taskID, retryID, summary["chunk_retry_files"], summary["chunk_retry_chunks"], summary["file_fallback_files"], summary["skipped_files"]))
	m.ensureWorkerLocked()
	return retryTask.ToSummary(), nil
}

// FinalizeCancelledTask 对齐 _finalize_cancelled_task
func (m *WebImportManager) FinalizeCancelledTask(taskID string, origin string, reason string) {
	m.mu.Lock()
	task := m.tasks[taskID]
	if task == nil || task.Status == WebImportTaskCompleted || task.Status == WebImportTaskCompletedWithErrors || task.Status == WebImportTaskFailed {
		m.mu.Unlock()
		return
	}
	now := m.now()
	task.CancelRequestedAt = &now
	task.CancelOrigin = origin
	m.markTaskCancelledLocked(task, reason)
	m.tryWriteTaskReport(task)
	m.mu.Unlock()
}
