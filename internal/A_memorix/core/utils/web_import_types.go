package utils

// Web 导入任务管理的数据结构与状态常量，对齐 web_import_manager.py

import (
	"fmt"
	"sort"
	"strings"
)

// Web 导入任务状态集合
const (
	WebImportTaskQueued              = "queued"
	WebImportTaskPreparing           = "preparing"
	WebImportTaskRunning             = "running"
	WebImportTaskCancelRequested     = "cancel_requested"
	WebImportTaskCancelled           = "cancelled"
	WebImportTaskCompleted           = "completed"
	WebImportTaskCompletedWithErrors = "completed_with_errors"
	WebImportTaskFailed              = "failed"
)

// Web 导入文件状态集合
const (
	WebImportFileQueued     = "queued"
	WebImportFilePreparing  = "preparing"
	WebImportFileSplitting  = "splitting"
	WebImportFileExtracting = "extracting"
	WebImportFileWriting    = "writing"
	WebImportFileSaving     = "saving"
	WebImportFileCompleted  = "completed"
	WebImportFileFailed     = "failed"
	WebImportFileCancelled  = "cancelled"
)

// Web 导入分块状态集合
const (
	WebImportChunkQueued     = "queued"
	WebImportChunkExtracting = "extracting"
	WebImportChunkWriting    = "writing"
	WebImportChunkCompleted  = "completed"
	WebImportChunkFailed     = "failed"
	WebImportChunkCancelled  = "cancelled"
)

// WebImportFileWarningKeepLimit 文件告警保留上限
const WebImportFileWarningKeepLimit = 50

// WebImportChunkRecord 导入分块记录
type WebImportChunkRecord struct {
	ChunkID        string
	Index          int
	ChunkType      string
	Status         string
	Step           string
	FailedAt       string
	Retryable      bool
	Error          string
	Progress       float64
	ContentPreview string
	UpdatedAt      float64
}

// NewWebImportChunkRecord 构造默认分块记录
func NewWebImportChunkRecord(chunkID string, index int, chunkType string, now float64) *WebImportChunkRecord {
	return &WebImportChunkRecord{
		ChunkID:   chunkID,
		Index:     index,
		ChunkType: chunkType,
		Status:    WebImportChunkQueued,
		Step:      "queued",
		UpdatedAt: now,
	}
}

// ToDict 对齐 ImportChunkRecord.to_dict
func (c *WebImportChunkRecord) ToDict() map[string]any {
	return map[string]any{
		"chunk_id":        c.ChunkID,
		"index":           c.Index,
		"chunk_type":      c.ChunkType,
		"status":          c.Status,
		"step":            c.Step,
		"failed_at":       c.FailedAt,
		"retryable":       c.Retryable,
		"error":           c.Error,
		"progress":        c.Progress,
		"content_preview": c.ContentPreview,
		"updated_at":      c.UpdatedAt,
	}
}

// WebImportFileRecord 导入文件记录
type WebImportFileRecord struct {
	FileID               string
	Name                 string
	SourceKind           string
	InputMode            string
	Status               string
	CurrentStep          string
	DetectedStrategyType string
	TotalChunks          int
	DoneChunks           int
	FailedChunks         int
	CancelledChunks      int
	Progress             float64
	Error                string
	Chunks               []*WebImportChunkRecord
	CreatedAt            float64
	UpdatedAt            float64
	TempPath             string
	SourcePath           string
	InlineContent        *string
	ContentHash          string
	ImportedSources      []string
	RetryChunkIndexes    []int
	RetryMode            string
	WarningCount         int
	Warnings             []string
}

// NewWebImportFileRecord 构造默认文件记录
func NewWebImportFileRecord(fileID string, name string, sourceKind string, inputMode string, now float64) *WebImportFileRecord {
	return &WebImportFileRecord{
		FileID:               fileID,
		Name:                 name,
		SourceKind:           sourceKind,
		InputMode:            inputMode,
		Status:               WebImportFileQueued,
		CurrentStep:          "queued",
		DetectedStrategyType: "unknown",
		Chunks:               []*WebImportChunkRecord{},
		CreatedAt:            now,
		UpdatedAt:            now,
		ImportedSources:      []string{},
		RetryChunkIndexes:    []int{},
		Warnings:             []string{},
	}
}

// ToDict 对齐 ImportFileRecord.to_dict
func (f *WebImportFileRecord) ToDict(includeChunks bool) map[string]any {
	sourcePath := f.SourcePath
	contentHash := f.ContentHash
	chunks := make([]any, 0, len(f.Chunks))
	if includeChunks {
		for _, chunk := range f.Chunks {
			chunks = append(chunks, chunk.ToDict())
		}
	}
	retryIndexes := make([]any, 0, len(f.RetryChunkIndexes))
	for _, index := range f.RetryChunkIndexes {
		retryIndexes = append(retryIndexes, index)
	}
	imported := make([]any, 0, len(f.ImportedSources))
	for _, source := range f.ImportedSources {
		imported = append(imported, source)
	}
	warnings := make([]any, 0, len(f.Warnings))
	for _, warning := range f.Warnings {
		warnings = append(warnings, warning)
	}
	payload := map[string]any{
		"file_id":                f.FileID,
		"name":                   f.Name,
		"source_kind":            f.SourceKind,
		"input_mode":             f.InputMode,
		"status":                 f.Status,
		"current_step":           f.CurrentStep,
		"detected_strategy_type": f.DetectedStrategyType,
		"total_chunks":           f.TotalChunks,
		"done_chunks":            f.DoneChunks,
		"failed_chunks":          f.FailedChunks,
		"cancelled_chunks":       f.CancelledChunks,
		"progress":               f.Progress,
		"error":                  f.Error,
		"created_at":             f.CreatedAt,
		"updated_at":             f.UpdatedAt,
		"source_path":            sourcePath,
		"content_hash":           contentHash,
		"imported_sources":       imported,
		"retry_chunk_indexes":    retryIndexes,
		"retry_mode":             f.RetryMode,
		"warning_count":          f.WarningCount,
		"warnings":               warnings,
	}
	if includeChunks {
		payload["chunks"] = chunks
	}
	return payload
}

// WebImportTaskRecord 导入任务记录
type WebImportTaskRecord struct {
	TaskID            string
	Source            string
	Params            map[string]any
	Status            string
	CurrentStep       string
	TotalChunks       int
	DoneChunks        int
	FailedChunks      int
	CancelledChunks   int
	Progress          float64
	Error             string
	Files             []*WebImportFileRecord
	CreatedAt         float64
	StartedAt         *float64
	FinishedAt        *float64
	UpdatedAt         float64
	SchemaDetected    string
	ArtifactPaths     map[string]any
	RollbackInfo      map[string]any
	RetryParentTaskID string
	RetrySummary      map[string]any
	CancelRequestedAt *float64
	CancelOrigin      string
}

// NewWebImportTaskRecord 构造默认任务记录
func NewWebImportTaskRecord(taskID string, source string, params map[string]any, now float64) *WebImportTaskRecord {
	return &WebImportTaskRecord{
		TaskID:        taskID,
		Source:        source,
		Params:        params,
		Status:        WebImportTaskQueued,
		CurrentStep:   "queued",
		Files:         []*WebImportFileRecord{},
		CreatedAt:     now,
		UpdatedAt:     now,
		ArtifactPaths: map[string]any{},
		RollbackInfo:  map[string]any{},
		RetrySummary:  map[string]any{},
	}
}

// ToSummary 对齐 ImportTaskRecord.to_summary
func (t *WebImportTaskRecord) ToSummary() map[string]any {
	taskKind := strings.TrimSpace(importValueText(t.Params["task_kind"]))
	if taskKind == "" {
		taskKind = t.Source
	}
	artifact := map[string]any{}
	for key, value := range t.ArtifactPaths {
		artifact[key] = value
	}
	rollback := map[string]any{}
	for key, value := range t.RollbackInfo {
		rollback[key] = value
	}
	retrySummary := map[string]any{}
	for key, value := range t.RetrySummary {
		retrySummary[key] = value
	}
	return map[string]any{
		"task_id":              t.TaskID,
		"source":               t.Source,
		"status":               t.Status,
		"current_step":         t.CurrentStep,
		"total_chunks":         t.TotalChunks,
		"done_chunks":          t.DoneChunks,
		"failed_chunks":        t.FailedChunks,
		"cancelled_chunks":     t.CancelledChunks,
		"progress":             t.Progress,
		"error":                t.Error,
		"file_count":           len(t.Files),
		"created_at":           t.CreatedAt,
		"started_at":           t.StartedAt,
		"finished_at":          t.FinishedAt,
		"updated_at":           t.UpdatedAt,
		"task_kind":            taskKind,
		"schema_detected":      t.SchemaDetected,
		"artifact_paths":       artifact,
		"rollback_info":        rollback,
		"retry_parent_task_id": t.RetryParentTaskID,
		"retry_summary":        retrySummary,
		"cancel_requested_at":  t.CancelRequestedAt,
		"cancel_origin":        t.CancelOrigin,
	}
}

// ToDetail 对齐 ImportTaskRecord.to_detail
func (t *WebImportTaskRecord) ToDetail(includeChunks bool) map[string]any {
	payload := t.ToSummary()
	params := map[string]any{}
	for key, value := range t.Params {
		params[key] = value
	}
	files := make([]any, 0, len(t.Files))
	for _, file := range t.Files {
		files = append(files, file.ToDict(includeChunks))
	}
	payload["params"] = params
	payload["files"] = files
	return payload
}

// webImportDedupeSources 对齐 _dedupe_sources
func webImportDedupeSources(sources []any) []string {
	normalized := []string{}
	seen := map[string]struct{}{}
	for _, raw := range sources {
		source := strings.TrimSpace(importValueText(raw))
		if source == "" {
			continue
		}
		key := strings.ToLower(source)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, source)
	}
	return normalized
}

// webImportDefaultSourcesForFile 对齐 _default_sources_for_file
func webImportDefaultSourcesForFile(file *WebImportFileRecord) []string {
	if len(file.ImportedSources) > 0 {
		values := make([]any, 0, len(file.ImportedSources))
		for _, source := range file.ImportedSources {
			values = append(values, source)
		}
		return webImportDedupeSources(values)
	}
	if file.SourcePath != "" {
		return []string{file.SourceKind + ":" + file.SourcePath}
	}
	return []string{"web_import:" + file.Name}
}

// webImportFindFile 按 file_id 查找文件
func webImportFindFile(task *WebImportTaskRecord, fileID string) *WebImportFileRecord {
	for _, file := range task.Files {
		if file.FileID == fileID {
			return file
		}
	}
	return nil
}

// webImportFindChunk 按 chunk_id 查找分块
func webImportFindChunk(file *WebImportFileRecord, chunkID string) *WebImportChunkRecord {
	for _, chunk := range file.Chunks {
		if chunk.ChunkID == chunkID {
			return chunk
		}
	}
	return nil
}

// webImportComputeRatio 对齐 _compute_ratio
func webImportComputeRatio(done int, total int) float64 {
	if total <= 0 {
		return 1.0
	}
	value := float64(done) / float64(total)
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// webImportRecomputeFileProgress 对齐 _recompute_file_progress
func webImportRecomputeFileProgress(file *WebImportFileRecord) {
	file.Progress = webImportComputeRatio(file.DoneChunks, file.TotalChunks)
}

// webImportRecomputeTaskProgress 对齐 _recompute_task_progress
func (m *WebImportManager) webImportRecomputeTaskProgress(task *WebImportTaskRecord) {
	total, done, failed, cancelled := 0, 0, 0, 0
	for _, file := range task.Files {
		total += file.TotalChunks
		done += file.DoneChunks
		failed += file.FailedChunks
		cancelled += file.CancelledChunks
	}
	task.TotalChunks = total
	task.DoneChunks = done
	task.FailedChunks = failed
	task.CancelledChunks = cancelled
	task.Progress = webImportComputeRatio(done, total)
	task.UpdatedAt = m.now()
}

// webImportSortedKeys 返回排序后的 map 键
func webImportSortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// webImportFormatInt 对齐 str(int)
func webImportFormatInt(value int) string { return fmt.Sprintf("%d", value) }
