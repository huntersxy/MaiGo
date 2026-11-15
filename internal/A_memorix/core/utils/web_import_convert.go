package utils

// Web 导入 LPMM 转换执行与校验，对齐 web_import_manager.py 的转换流程

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// preflightConvertRuntime 对齐 _preflight_convert_runtime
func (m *WebImportManager) preflightConvertRuntime(ctx context.Context) (bool, string) {
	if m.hasNativeScript("convert_lpmm") {
		// Go 转换器不依赖 scipy/pyarrow，无需 Python 预检
		return true, ""
	}
	python, err := m.webImportPythonExecutable()
	if err != nil {
		return false, fmt.Sprintf("依赖预检失败: %v", err)
	}
	probeCode := "import importlib\nmods=['scipy','pyarrow']\nfailed=[]\nfor m in mods:\n    try:\n        importlib.import_module(m)\n    except Exception as e:\n        failed.append(f'{m}:{e.__class__.__name__}:{e}')\nprint('OK' if not failed else ';'.join(failed))\n"
	timeout := m.timeoutConfig()["convert_preflight_seconds"]
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	code, stdout, stderr, err := m.runImportCommand(probeCtx, []string{python, "-c", probeCode}, "", nil)
	if err != nil {
		return false, fmt.Sprintf("依赖预检执行失败: %v", err)
	}
	out := strings.TrimSpace(stdout)
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = out
	}
	if code != 0 {
		if detail == "" {
			detail = fmt.Sprintf("return_code=%d", code)
		}
		return false, fmt.Sprintf("依赖预检失败 (python=%s): %s", python, detail)
	}
	if out != "OK" {
		return false, fmt.Sprintf("依赖预检失败 (python=%s): %s", python, out)
	}
	return true, ""
}

// verifyConvertOutput 对齐 _verify_convert_output
func (m *WebImportManager) verifyConvertOutput(ctx context.Context, outputDir string) map[string]any {
	if m.deps.VerifyConvert != nil {
		checks, err := m.deps.VerifyConvert(ctx, outputDir)
		if err != nil {
			checks["error"] = err.Error()
		}
		return checks
	}
	vectorsRoot := filepath.Join(outputDir, "vectors")
	checks := map[string]any{
		"paragraph_vectors_exists": webImportDirExists(filepath.Join(vectorsRoot, "paragraph")),
		"graph_vectors_exists":     webImportDirExists(filepath.Join(vectorsRoot, "graph")),
		"graph_exists":             webImportDirExists(filepath.Join(outputDir, "graph")),
		"metadata_exists":          webImportDirExists(filepath.Join(outputDir, "metadata")),
		"manifest_exists":          webImportFileExists(filepath.Join(vectorsRoot, "dual_ready.json")),
		"stores_opened":            false,
		"references_valid":         false,
		"counts_match":             false,
		"ok":                       false,
	}
	for _, key := range []string{"paragraph_vectors_exists", "graph_vectors_exists", "graph_exists", "metadata_exists", "manifest_exists"} {
		if !webImportCoerceBool(checks[key], false) {
			return checks
		}
	}
	checks["error"] = "转换校验器未初始化"
	return checks
}

// processLpmmConvert 对齐 _process_lpmm_convert
func (m *WebImportManager) processLpmmConvert(ctx context.Context, taskID string, file *WebImportFileRecord) {
	chunkID := m.ensureMigrationChunk(taskID, file.FileID, "lpmm_convert", "LPMM 二进制转换任务")
	m.setFileStrategy(taskID, file.FileID, "lpmm_convert")
	m.setFileState(taskID, file.FileID, WebImportFilePreparing, "preflight")
	m.setChunkState(taskID, file.FileID, chunkID, WebImportChunkWriting, "converting", 0.05)
	params := m.webImportTaskParams(taskID)
	if len(params) == 0 {
		m.setFileFailed(taskID, file.FileID, "任务不存在")
		return
	}
	sourceDir := importValueText(params["source_path"])
	targetDir := importValueText(params["target_path"])
	if !webImportDirExists(sourceDir) {
		m.setFileFailed(taskID, file.FileID, "输入目录无效: "+sourceDir)
		return
	}
	if !webImportDirExists(targetDir) {
		m.setFileFailed(taskID, file.FileID, "目标目录无效: "+targetDir)
		return
	}
	scriptPath := m.resolveConvertScript()
	if !m.hasNativeScript("convert_lpmm") && !webImportFileExists(scriptPath) {
		m.setFileFailed(taskID, file.FileID, "转换脚本不存在: "+scriptPath)
		return
	}
	runtimeOK, runtimeDetail := m.preflightConvertRuntime(ctx)
	if !runtimeOK {
		m.setFileFailed(taskID, file.FileID, runtimeDetail)
		m.setChunkFailed(taskID, file.FileID, chunkID, runtimeDetail)
		return
	}
	requiredInputs := []string{"paragraph.parquet", "entity.parquet"}
	found := false
	for _, name := range requiredInputs {
		if webImportFileExists(filepath.Join(sourceDir, name)) {
			found = true
			break
		}
	}
	if !found {
		m.setFileFailed(taskID, file.FileID, "输入目录缺少必要文件，至少需要其一: "+strings.Join(requiredInputs, ", "))
		return
	}
	if webImportDiskFree(targetDir) < 512*1024*1024 {
		m.setFileFailed(taskID, file.FileID, "磁盘剩余空间不足（<512MB）")
		return
	}
	python, err := m.webImportPythonExecutable()
	if err != nil {
		detail := "解析 Python 解释器失败: " + err.Error()
		m.setFileFailed(taskID, file.FileID, detail)
		m.setChunkFailed(taskID, file.FileID, chunkID, detail)
		return
	}
	command := []string{
		python, scriptPath,
		"--input", sourceDir,
		"--output", targetDir,
		"--data-dir", m.resolveDataDir(),
		"--dim", importValueText(params["dimension"]),
		"--batch-size", importValueText(params["batch_size"]),
	}
	code, stdout, stderr, err := m.runImportCommand(ctx, command, m.resolveRepoRoot(), nil)
	stdoutLines := webImportTailLines(stdout, 120)
	stderrLines := webImportTailLines(stderr, 120)
	if m.isCancelRequested(taskID) {
		m.setChunkCancelled(taskID, file.FileID, chunkID, "任务已取消")
		m.setFileCancelled(taskID, file.FileID, "任务已取消")
		return
	}
	if err != nil {
		detail := fmt.Sprintf("转换进程执行失败: %v", err)
		m.setFileFailed(taskID, file.FileID, detail)
		m.setChunkFailed(taskID, file.FileID, chunkID, detail)
		return
	}
	if code != 0 {
		detail := ""
		if len(stderrLines) > 0 {
			detail = stderrLines[len(stderrLines)-1]
		} else if len(stdoutLines) > 0 {
			detail = stdoutLines[len(stdoutLines)-1]
		}
		m.setFileFailed(taskID, file.FileID, firstNonEmpty(detail, fmt.Sprintf("转换失败，退出码: %d", code)))
		m.setChunkFailed(taskID, file.FileID, chunkID, firstNonEmpty(detail, fmt.Sprintf("退出码: %d", code)))
		return
	}
	m.setChunkState(taskID, file.FileID, chunkID, WebImportChunkWriting, "verifying", 0.65)
	verify := m.verifyConvertOutput(ctx, targetDir)
	verifyJSON, _ := pyJSONMarshal(verify)
	m.mu.Lock()
	if task := m.tasks[taskID]; task != nil {
		task.ArtifactPaths["output_dir"] = targetDir
		task.ArtifactPaths["verify"] = verifyJSON
	}
	m.mu.Unlock()
	if !webImportCoerceBool(verify["ok"], false) {
		m.setFileFailed(taskID, file.FileID, "校验失败: "+verifyJSON)
		m.setChunkFailed(taskID, file.FileID, chunkID, "校验失败: "+verifyJSON)
		return
	}
	m.setChunkCompleted(taskID, file.FileID, chunkID)
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	fileRecord := webImportFindFile(task, file.FileID)
	if fileRecord == nil {
		return
	}
	fileRecord.TotalChunks = 1
	fileRecord.DoneChunks = 1
	fileRecord.FailedChunks = 0
	fileRecord.CancelledChunks = 0
	fileRecord.Progress = 1
	fileRecord.Status = WebImportFileCompleted
	fileRecord.CurrentStep = "completed"
	fileRecord.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// processTemporalBackfill 对齐 _process_temporal_backfill
func (m *WebImportManager) processTemporalBackfill(ctx context.Context, taskID string, file *WebImportFileRecord) {
	chunkID := m.ensureMigrationChunk(taskID, file.FileID, "temporal_backfill", "时序字段回填任务")
	m.setFileStrategy(taskID, file.FileID, "temporal_backfill")
	m.setFileState(taskID, file.FileID, WebImportFilePreparing, "backfilling")
	m.setChunkState(taskID, file.FileID, chunkID, WebImportChunkWriting, "backfilling", 0.2)
	params := m.webImportTaskParams(taskID)
	if len(params) == 0 {
		m.setFileFailed(taskID, file.FileID, "任务不存在")
		return
	}
	targetDir := file.SourcePath
	if !webImportDirExists(filepath.Join(targetDir, "metadata")) {
		m.setFileFailed(taskID, file.FileID, "metadata 目录不存在: "+filepath.Join(targetDir, "metadata"))
		return
	}
	if m.deps.TemporalBackfill == nil {
		m.setFileFailed(taskID, file.FileID, "回填执行器未初始化")
		return
	}
	dryRun := webImportCoerceBool(params["dry_run"], false)
	noFallback := webImportCoerceBool(params["no_created_fallback"], false)
	limit := max(1, webImportCoerceInt(params["limit"], 100000))
	summary, err := m.deps.TemporalBackfill(ctx, targetDir, limit, dryRun, noFallback)
	if err != nil {
		m.setFileFailed(taskID, file.FileID, err.Error())
		return
	}
	candidates := webImportCoerceInt(summary["candidates"], 0)
	updated := webImportCoerceInt(summary["updated"], 0)
	payload := map[string]any{
		"target_dir":          targetDir,
		"dry_run":             dryRun,
		"no_created_fallback": noFallback,
		"limit":               limit,
		"candidates":          candidates,
		"updated":             updated,
	}
	m.mu.Lock()
	if task := m.tasks[taskID]; task != nil {
		temporalJSON, _ := pyJSONMarshal(payload)
		task.ArtifactPaths["temporal_backfill"] = temporalJSON
	}
	m.mu.Unlock()
	m.setChunkCompleted(taskID, file.FileID, chunkID)
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil {
		return
	}
	fileRecord := webImportFindFile(task, file.FileID)
	if fileRecord == nil {
		return
	}
	fileRecord.TotalChunks = 1
	fileRecord.DoneChunks = 1
	fileRecord.FailedChunks = 0
	fileRecord.CancelledChunks = 0
	fileRecord.Progress = 1
	fileRecord.Status = WebImportFileCompleted
	fileRecord.CurrentStep = "completed"
	fileRecord.UpdatedAt = m.now()
	m.webImportRecomputeTaskProgress(task)
}

// webImportReadJSONFile 读取 JSON 文件为字典
func webImportReadJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON 内容不是对象")
	}
	return object, nil
}

// webImportTailLines 保留输出末尾若干行
func webImportTailLines(text string, limit int) []string {
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		if token := strings.TrimSpace(line); token != "" {
			lines = append(lines, token)
		}
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// webImportDirExists 判断目录是否存在
func webImportDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// webImportFileExists 判断文件是否存在
func webImportFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// webImportClampInt 限制整数范围
func webImportClampInt(value int, lower int, upper int) int {
	if value < lower {
		return lower
	}
	if value > upper {
		return upper
	}
	return value
}

// firstNonEmpty 返回首个非空字符串
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
