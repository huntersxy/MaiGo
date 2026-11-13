package utils

// Web 导入文件与分块状态流转，对齐 web_import_manager.py 的状态辅助

import (
	"strings"
)

// setFileStrategy 对齐 _set_file_strategy
func (m *WebImportManager) setFileStrategy(taskID string, fileID string, strategy string) {
	strategyType := strategy
	switch strategy {
	case "chat_log", "narrative", "factual", "quote":
	default:
		if strategy != "json" && strategy != "maibot_migration" && strategy != "lpmm_convert" && strategy != "temporal_backfill" {
			strategyType = "unknown"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	file.DetectedStrategyType = strategyType
	file.UpdatedAt = m.now()
	task.UpdatedAt = m.now()
}

// setFileState 对齐 _set_file_state
func (m *WebImportManager) setFileState(taskID string, fileID string, status string, step string) {
	switch status {
	case WebImportFileQueued, WebImportFilePreparing, WebImportFileSplitting, WebImportFileExtracting, WebImportFileWriting, WebImportFileSaving, WebImportFileCompleted, WebImportFileFailed, WebImportFileCancelled:
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	file.Status = status
	file.CurrentStep = step
	file.UpdatedAt = m.now()
	task.UpdatedAt = m.now()
	if step == "preparing" || step == "splitting" || step == "extracting" || step == "writing" || step == "saving" {
		if task.Status == WebImportTaskQueued || task.Status == WebImportTaskPreparing {
			task.Status = WebImportTaskRunning
			task.CurrentStep = "running"
		}
	}
}

// appendFileWarning 对齐 _append_file_warning
func (m *WebImportManager) appendFileWarning(taskID string, fileID string, warning string) {
	text := strings.TrimSpace(warning)
	if text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	file.WarningCount++
	file.Warnings = append(file.Warnings, text)
	if len(file.Warnings) > WebImportFileWarningKeepLimit {
		file.Warnings = file.Warnings[len(file.Warnings)-WebImportFileWarningKeepLimit:]
	}
	file.UpdatedAt = m.now()
	task.UpdatedAt = m.now()
}

// appendFileWarnings 对齐 _append_file_warnings
func (m *WebImportManager) appendFileWarnings(taskID string, fileID string, warnings []string) {
	for _, warning := range warnings {
		m.appendFileWarning(taskID, fileID, warning)
	}
}

// setFileFailed 对齐 _set_file_failed
func (m *WebImportManager) setFileFailed(taskID string, fileID string, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	file.Status = WebImportFileFailed
	file.CurrentStep = "failed"
	file.Error = errText
	file.UpdatedAt = m.now()
	task.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// setFileCancelled 对齐 _set_file_cancelled
func (m *WebImportManager) setFileCancelled(taskID string, fileID string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
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
	if additional > 0 {
		file.CancelledChunks += additional
	}
	webImportRecomputeFileProgress(file)
	file.UpdatedAt = m.now()
	task.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// setChunkState 对齐 _set_chunk_state
func (m *WebImportManager) setChunkState(taskID string, fileID string, chunkID string, status string, step string, progress float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	chunk := webImportFindChunk(file, chunkID)
	if chunk == nil {
		return
	}
	chunk.Status = status
	chunk.Step = step
	if status == WebImportChunkQueued || status == WebImportChunkExtracting || status == WebImportChunkWriting {
		chunk.Error = ""
		chunk.FailedAt = ""
		chunk.Retryable = false
	}
	chunk.Progress = progress
	if chunk.Progress < 0 {
		chunk.Progress = 0
	}
	if chunk.Progress > 1 {
		chunk.Progress = 1
	}
	chunk.UpdatedAt = m.now()
	if file.Status != WebImportFileFailed && file.Status != WebImportFileCancelled {
		if status == WebImportChunkExtracting {
			file.Status = WebImportFileExtracting
		} else {
			file.Status = WebImportFileWriting
		}
		file.CurrentStep = step
	}
	file.UpdatedAt = m.now()
	task.UpdatedAt = m.now()
}

// setChunkCompleted 对齐 _set_chunk_completed
func (m *WebImportManager) setChunkCompleted(taskID string, fileID string, chunkID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	chunk := webImportFindChunk(file, chunkID)
	if chunk == nil || chunk.Status == WebImportChunkCompleted {
		return
	}
	chunk.Status = WebImportChunkCompleted
	chunk.Step = "completed"
	chunk.FailedAt = ""
	chunk.Retryable = false
	chunk.Progress = 1
	chunk.UpdatedAt = m.now()
	file.DoneChunks++
	webImportRecomputeFileProgress(file)
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// setChunkFailed 对齐 _set_chunk_failed
func (m *WebImportManager) setChunkFailed(taskID string, fileID string, chunkID string, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	chunk := webImportFindChunk(file, chunkID)
	if chunk == nil || chunk.Status == WebImportChunkFailed {
		return
	}
	failedStage := strings.ToLower(strings.TrimSpace(chunk.Step))
	if failedStage == "" || failedStage == "queued" || failedStage == "failed" || failedStage == "completed" || failedStage == "cancelled" {
		failedStage = strings.ToLower(strings.TrimSpace(file.CurrentStep))
	}
	if failedStage == "" || failedStage == "queued" || failedStage == "failed" || failedStage == "completed" || failedStage == "cancelled" {
		failedStage = "unknown"
	}
	chunk.Status = WebImportChunkFailed
	chunk.Step = "failed"
	chunk.FailedAt = failedStage
	chunk.Retryable = file.InputMode == "text" && failedStage == "extracting"
	chunk.Error = errText
	chunk.Progress = 1
	chunk.UpdatedAt = m.now()
	file.FailedChunks++
	webImportRecomputeFileProgress(file)
	if file.Error == "" {
		file.Error = errText
	}
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// setChunkCancelled 对齐 _set_chunk_cancelled
func (m *WebImportManager) setChunkCancelled(taskID string, fileID string, chunkID string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return
	}
	chunk := webImportFindChunk(file, chunkID)
	if chunk == nil || chunk.Status == WebImportChunkCancelled {
		return
	}
	chunk.Status = WebImportChunkCancelled
	chunk.Step = "cancelled"
	chunk.Retryable = false
	chunk.Error = reason
	chunk.Progress = 1
	chunk.UpdatedAt = m.now()
	file.CancelledChunks++
	webImportRecomputeFileProgress(file)
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}
