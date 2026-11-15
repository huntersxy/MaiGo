package utils

// Web 导入外部任务执行，对齐 web_import_manager.py 的迁移、转换与时序回填

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// webImportPythonExecutable 解析外部脚本解释器，优先显式配置再交给统一解析器
func (m *WebImportManager) webImportPythonExecutable() (string, error) {
	configured := strings.TrimSpace(m.deps.PythonExecutable)
	if configured == "" {
		configured = strings.TrimSpace(importValueText(m.cfg("web.import.python_executable", "")))
	}
	return ResolvePythonExecutable(configured, m.resolveRepoRoot())
}

// ensureMigrationChunk 对齐 _ensure_maibot_migration_chunk
func (m *WebImportManager) ensureMigrationChunk(taskID string, fileID string, chunkType string, preview string) string {
	chunkID := fileID + "_" + chunkType
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return chunkID
	}
	file := webImportFindFile(task, fileID)
	if file == nil {
		return chunkID
	}
	if len(file.Chunks) == 0 {
		record := NewWebImportChunkRecord(chunkID, 0, chunkType, m.now())
		record.ContentPreview = preview
		file.Chunks = []*WebImportChunkRecord{record}
		file.TotalChunks = 1
		file.DoneChunks = 0
		file.FailedChunks = 0
		file.CancelledChunks = 0
		file.Progress = 0
		file.UpdatedAt = m.now()
		m.webImportRecomputeTaskProgress(task)
		return chunkID
	}
	return file.Chunks[0].ChunkID
}

// refreshMigrationProgress 对齐 _refresh_maibot_progress_from_state
func (m *WebImportManager) refreshMigrationProgress(taskID string, fileID string, chunkID string, statePath string) {
	payload, err := webImportReadJSONFile(statePath)
	if err != nil || payload == nil {
		return
	}
	stats := map[string]any{}
	if raw, ok := payload["stats"].(map[string]any); ok {
		stats = raw
	}
	total := max(0, webImportCoerceInt(stats["source_matched_total"], 0))
	scanned := max(0, webImportCoerceInt(stats["scanned_rows"], 0))
	bad := max(0, webImportCoerceInt(stats["bad_rows"], 0))
	done := max(0, scanned-bad)
	migrated := max(0, webImportCoerceInt(stats["migrated_rows"], 0))
	lastID := max(0, webImportCoerceInt(stats["last_committed_id"], 0))
	if total <= 0 {
		total = max(1, scanned)
	}
	progress := 0.0
	if total > 0 {
		progress = float64(scanned) / float64(total)
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
	}
	preview := fmt.Sprintf("scanned=%d/%d, migrated=%d, bad=%d, last_id=%d", scanned, total, migrated, bad, lastID)
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
	if chunk != nil {
		if chunk.Status != WebImportChunkCompleted && chunk.Status != WebImportChunkFailed && chunk.Status != WebImportChunkCancelled {
			chunk.Status = WebImportChunkWriting
			chunk.Step = "migrating"
		}
		chunk.Progress = progress
		chunk.ContentPreview = preview
		chunk.UpdatedAt = m.now()
	}
	file.TotalChunks = total
	file.DoneChunks = done
	file.FailedChunks = bad
	file.CancelledChunks = 0
	webImportRecomputeFileProgress(file)
	if file.Status != WebImportFileFailed && file.Status != WebImportFileCancelled {
		file.Status = WebImportFileWriting
		file.CurrentStep = "migrating"
	}
	file.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// reloadStoresAfterExternalMigration 对齐 _reload_stores_after_external_migration
func (m *WebImportManager) reloadStoresAfterExternalMigration() {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	for _, store := range m.vectorStoresForPersistence() {
		if !store.HasData() {
			continue
		}
		if err := store.Load(); err != nil {
			m.deps.Logger.Warning(fmt.Sprintf("迁移后重载 VectorStore 失败: %v", err))
		}
	}
	if m.deps.Graph != nil && m.deps.Graph.HasData() {
		if err := m.deps.Graph.Load(); err != nil {
			m.deps.Logger.Warning(fmt.Sprintf("迁移后重载 GraphStore 失败: %v", err))
		}
	}
}

// runImportCommand 运行外部命令，并在等待期间刷新进度
func (m *WebImportManager) runImportCommand(ctx context.Context, command []string, workingDir string, poll func()) (int, string, string, error) {
	if m.deps.CommandRunner == nil {
		return -1, "", "", fmt.Errorf("外部命令执行器未初始化")
	}
	if poll == nil {
		return m.deps.CommandRunner.Run(ctx, command, workingDir)
	}
	type commandResult struct {
		code   int
		stdout string
		stderr string
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		code, stdout, stderr, err := m.deps.CommandRunner.Run(ctx, command, workingDir)
		done <- commandResult{code: code, stdout: stdout, stderr: stderr, err: err}
	}()
	ticker := time.NewTicker(time.Duration(max(1, int(m.timeoutConfig()["process_poll_seconds"]*1000))) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-done:
			return result.code, result.stdout, result.stderr, result.err
		case <-ctx.Done():
			return -1, "", "", ctx.Err()
		case <-ticker.C:
			poll()
		}
	}
}

// processMaibotMigration 对齐 _process_maibot_migration
func (m *WebImportManager) processMaibotMigration(ctx context.Context, taskID string, file *WebImportFileRecord) {
	chunkID := m.ensureMigrationChunk(taskID, file.FileID, "maibot_migration", "MaiBot chat_history 迁移任务")
	m.setFileStrategy(taskID, file.FileID, "maibot_migration")
	m.setFileState(taskID, file.FileID, WebImportFilePreparing, "preparing")
	m.setChunkState(taskID, file.FileID, chunkID, WebImportChunkWriting, "migrating", 0)
	params := m.webImportTaskParams(taskID)
	if len(params) == 0 {
		m.setFileFailed(taskID, file.FileID, "任务不存在")
		return
	}
	command, err := m.buildMaibotMigrationCommand(params)
	if err != nil {
		m.setChunkFailed(taskID, file.FileID, chunkID, err.Error())
		m.setFileFailed(taskID, file.FileID, err.Error())
		return
	}
	targetDataDir := importValueText(params["target_data_dir"])
	statePath := filepath.Join(targetDataDir, "migration_state", "chat_history_resume.json")
	reportPath := filepath.Join(targetDataDir, "migration_state", "chat_history_report.json")
	m.deps.Logger.Info("开始执行 MaiBot 迁移任务: " + strings.Join(command, " "))
	var stdoutLines []string
	var stderrLines []string
	poll := func() {
		m.refreshMigrationProgress(taskID, file.FileID, chunkID, statePath)
	}
	code, stdout, stderr, err := m.runImportCommand(ctx, command, m.resolveRepoRoot(), poll)
	stdoutLines = webImportTailLines(stdout, 120)
	stderrLines = webImportTailLines(stderr, 120)
	m.refreshMigrationProgress(taskID, file.FileID, chunkID, statePath)
	if err != nil {
		detail := fmt.Sprintf("迁移进程执行失败: %v", err)
		m.setChunkFailed(taskID, file.FileID, chunkID, detail)
		m.setFileFailed(taskID, file.FileID, detail)
		return
	}
	if m.isCancelRequested(taskID) {
		m.setChunkCancelled(taskID, file.FileID, chunkID, "任务已取消")
		m.setFileCancelled(taskID, file.FileID, "任务已取消")
		return
	}
	report, _ := webImportReadJSONFile(reportPath)
	badRows := 0
	if report != nil {
		if stats, ok := report["stats"].(map[string]any); ok {
			badRows = max(0, webImportCoerceInt(stats["bad_rows"], 0))
		}
	}
	if code == 0 || code == 2 {
		m.setFileState(taskID, file.FileID, WebImportFileSaving, "saving")
		m.reloadStoresAfterExternalMigration()
		m.mu.Lock()
		if task := m.tasks[taskID]; task != nil {
			if f := webImportFindFile(task, file.FileID); f != nil {
				if c := webImportFindChunk(f, chunkID); c != nil && c.Status != WebImportChunkCancelled && c.Status != WebImportChunkFailed {
					c.Status = WebImportChunkCompleted
					c.Step = "completed"
					c.Progress = 1
					c.UpdatedAt = m.now()
				}
				if f.TotalChunks <= 0 {
					f.TotalChunks = 1
				}
				if f.DoneChunks+f.FailedChunks <= 0 {
					f.DoneChunks = f.TotalChunks - badRows
					f.FailedChunks = badRows
				}
				f.DoneChunks = webImportClampInt(f.DoneChunks, 0, f.TotalChunks)
				f.FailedChunks = webImportClampInt(f.FailedChunks, 0, f.TotalChunks)
				f.CancelledChunks = 0
				webImportRecomputeFileProgress(f)
				f.Status = WebImportFileCompleted
				f.CurrentStep = "completed"
				if badRows > 0 && f.Error == "" {
					f.Error = fmt.Sprintf("迁移完成，但存在坏行: %d", badRows)
				}
				f.UpdatedAt = m.now()
			}
			m.webImportRecomputeTaskProgress(task)
		}
		m.mu.Unlock()
		return
	}
	failReason := ""
	if report != nil {
		failReason = strings.TrimSpace(importValueText(report["fail_reason"]))
	}
	tail := ""
	if len(stderrLines) > 0 {
		tail = stderrLines[len(stderrLines)-1]
	} else if len(stdoutLines) > 0 {
		tail = stdoutLines[len(stdoutLines)-1]
	}
	detail := failReason
	if detail == "" {
		detail = tail
	}
	if detail == "" {
		detail = fmt.Sprintf("迁移进程退出码: %d", code)
	}
	m.setChunkFailed(taskID, file.FileID, chunkID, detail)
	m.setFileFailed(taskID, file.FileID, detail)
}

// buildMaibotMigrationCommand 对齐 _build_maibot_migration_command
func (m *WebImportManager) buildMaibotMigrationCommand(params map[string]any) ([]string, error) {
	scriptPath := m.resolveMigrationScript()
	if !m.hasNativeScript("migrate_maibot_memory") {
		if _, err := os.Stat(scriptPath); err != nil {
			return nil, fmt.Errorf("迁移脚本不存在: %s", scriptPath)
		}
	}
	python, err := m.webImportPythonExecutable()
	if err != nil {
		return nil, err
	}
	command := []string{
		python, scriptPath,
		"--source-db", importValueText(params["source_db"]),
		"--target-data-dir", importValueText(params["target_data_dir"]),
		"--read-batch-size", importValueText(params["read_batch_size"]),
		"--commit-window-rows", importValueText(params["commit_window_rows"]),
		"--embed-batch-size", importValueText(params["embed_batch_size"]),
		"--entity-embed-batch-size", importValueText(params["entity_embed_batch_size"]),
		"--max-errors", importValueText(params["max_errors"]),
		"--log-every", importValueText(params["log_every"]),
		"--preview-limit", importValueText(params["preview_limit"]),
		"--yes",
	}
	optionalInts := [][2]string{{"embed_workers", "--embed-workers"}, {"start_id", "--start-id"}, {"end_id", "--end-id"}}
	for _, pair := range optionalInts {
		if params[pair[0]] != nil {
			command = append(command, pair[1], importValueText(params[pair[0]]))
		}
	}
	if value := strings.TrimSpace(importValueText(params["time_from"])); value != "" {
		command = append(command, "--time-from", value)
	}
	if value := strings.TrimSpace(importValueText(params["time_to"])); value != "" {
		command = append(command, "--time-to", value)
	}
	for _, item := range []struct{ key, flag string }{{"stream_ids", "--stream-id"}, {"group_ids", "--group-id"}, {"user_ids", "--user-id"}} {
		for _, value := range webImportCoerceList(params[item.key]) {
			command = append(command, item.flag, value)
		}
	}
	for _, item := range []struct{ key, flag string }{{"reset_state", "--reset-state"}, {"no_resume", "--no-resume"}, {"dry_run", "--dry-run"}, {"verify_only", "--verify-only"}} {
		if webImportCoerceBool(params[item.key], false) {
			command = append(command, item.flag)
		}
	}
	return command, nil
}
