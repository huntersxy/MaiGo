package webuiapi

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// 本文件对齐 ../MaiBot/src/webui/routers/data_transfer.py 的任务状态、导出打包与导入校验

const (
	transferChunkSize       = 1024 * 1024
	transferRetentionSecond = 24 * 60 * 60
	transferArchiveFormat   = "maibot-data-archive"
)

var (
	transferJobIDPattern     = regexp.MustCompile("^[0-9a-f]{32}$")
	transferTempEntryPattern = regexp.MustCompile("^([0-9a-f]{32})(?:\\.zip|-upload\\.zip|-snapshots-[a-z0-9_]+)$")
	graphGenerationPattern   = regexp.MustCompile("^graph-[0-9a-f]{32}$")

	transferRequiredParts = []string{"config", "data"}
	transferExcludedPaths = map[string]bool{
		"data/.a_memorix_runtime_writer.lock":           true,
		"data/a-memorix/.a_memorix_runtime_writer.lock": true,
	}
	transferExcludedPrefixes = []string{"data/prompt_imgs/"}
)

// transferError 表示可直接映射为 HTTP 状态码的数据迁移错误
type transferError struct {
	Status int
	Detail string
}

func (e *transferError) Error() string { return e.Detail }

func newTransferError(status int, detail string) error {
	return &transferError{Status: status, Detail: detail}
}

type transferJob struct {
	// stateMu 保护所有可变任务状态；transferJobs.mu 只保护 jobs 映射与历史记录写入
	// 锁顺序固定为 transferJobs.mu -> stateMu，禁止反向获取
	stateMu sync.Mutex

	JobID           string
	Kind            string
	Status          string
	Progress        int
	Message         string
	TotalFiles      int
	ProcessedFiles  int
	TotalBytes      int64
	ProcessedBytes  int64
	Filename        string
	FilePath        string
	Manifest        map[string]any
	Error           string
	CancelRequested bool
	WorkerActive    bool
	ActiveDownloads int
	ExpiresAt       float64
	CompletedAt     float64
	ArchiveBytes    int64
	// RestartRequired 表示导入的数据库快照需要下次启动恢复
	RestartRequired bool
	// DatabaseRestorePending 表示实例已写入待恢复标记
	DatabaseRestorePending bool
}

type transferJobStore struct {
	mu   sync.Mutex
	jobs map[string]*transferJob
}

var transferJobs = &transferJobStore{jobs: map[string]*transferJob{}}

func transferTempDir() string {
	return filepath.Join(os.TempDir(), "maibot_webui_transfer")
}

func unixFloat(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}

func parseUnixFloat(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	parsed := value
	return &parsed
}

func newTransferJob(kind string) (*transferJob, error) {
	if err := os.MkdirAll(transferTempDir(), 0o755); err != nil {
		return nil, err
	}
	jobID, err := randomJobID()
	if err != nil {
		return nil, err
	}
	job := &transferJob{JobID: jobID, Kind: kind, Status: "pending", Message: "等待处理"}
	transferJobs.mu.Lock()
	transferJobs.jobs[jobID] = job
	transferJobs.mu.Unlock()
	return job, nil
}

func randomJobID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func (s *transferJobStore) lookup(jobID string, kind string) (*transferJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[jobID]
	if job == nil || (kind != "" && job.Kind != kind) {
		return nil, false
	}
	return job, true
}

// transferJobSnapshot 是一次任务状态的一致快照，避免响应字段互相撕裂
type transferJobSnapshot struct {
	JobID                  string
	Kind                   string
	Status                 string
	Progress               int
	Message                string
	TotalFiles             int
	ProcessedFiles         int
	TotalBytes             int64
	ProcessedBytes         int64
	Filename               string
	FilePath               string
	Manifest               map[string]any
	Error                  string
	CancelRequested        bool
	WorkerActive           bool
	ActiveDownloads        int
	ExpiresAt              float64
	CompletedAt            float64
	ArchiveBytes           int64
	RestartRequired        bool
	DatabaseRestorePending bool
}

func (job *transferJob) snapshot() transferJobSnapshot {
	if job == nil {
		return transferJobSnapshot{}
	}
	job.stateMu.Lock()
	defer job.stateMu.Unlock()
	return job.snapshotLocked()
}

// snapshotLocked 要求调用方已持有 stateMu
func (job *transferJob) snapshotLocked() transferJobSnapshot {
	return transferJobSnapshot{
		JobID: job.JobID, Kind: job.Kind, Status: job.Status,
		Progress: job.Progress, Message: job.Message,
		TotalFiles: job.TotalFiles, ProcessedFiles: job.ProcessedFiles,
		TotalBytes: job.TotalBytes, ProcessedBytes: job.ProcessedBytes,
		Filename: job.Filename, FilePath: job.FilePath, Manifest: job.Manifest,
		Error: job.Error, CancelRequested: job.CancelRequested, WorkerActive: job.WorkerActive,
		ActiveDownloads: job.ActiveDownloads, ExpiresAt: job.ExpiresAt,
		CompletedAt: job.CompletedAt, ArchiveBytes: job.ArchiveBytes,
		RestartRequired: job.RestartRequired, DatabaseRestorePending: job.DatabaseRestorePending,
	}
}

// transferJobResponse 对齐 Python DataTransferJobResponse，所有字段来自同一把锁下的快照
func transferJobResponse(job *transferJob) map[string]any {
	return transferJobResponseFromSnapshot(job.snapshot())
}

func transferJobResponseFromSnapshot(snapshot transferJobSnapshot) map[string]any {
	var downloadURL any
	if snapshot.Kind == "export" && snapshot.Status == "completed" && snapshot.FilePath != "" {
		downloadURL = "/api/webui/data-transfer/export/" + snapshot.JobID + "/download"
	}
	var filename any
	if snapshot.Filename != "" {
		filename = snapshot.Filename
	}
	var manifest any
	if snapshot.Manifest != nil {
		manifest = snapshot.Manifest
	}
	var jobErr any
	if snapshot.Error != "" {
		jobErr = snapshot.Error
	}
	return map[string]any{
		"job_id": snapshot.JobID, "kind": snapshot.Kind, "status": snapshot.Status,
		"progress": snapshot.Progress, "message": snapshot.Message,
		"total_files": snapshot.TotalFiles, "processed_files": snapshot.ProcessedFiles,
		"total_bytes": snapshot.TotalBytes, "processed_bytes": snapshot.ProcessedBytes,
		"filename": filename, "download_url": downloadURL, "manifest": manifest,
		"error": jobErr, "completed_at": parseUnixFloat(snapshot.CompletedAt),
		"expires_at": parseUnixFloat(snapshot.ExpiresAt), "archive_bytes": snapshot.ArchiveBytes,
		"restart_required": snapshot.RestartRequired, "database_restore_pending": snapshot.DatabaseRestorePending,
	}
}

func restoreExportHistoryLocked() {
	entries, err := filepath.Glob(filepath.Join(transferTempDir(), "*.export.json"))
	if err != nil {
		return
	}
	for _, recordPath := range entries {
		jobID := strings.TrimSuffix(filepath.Base(recordPath), ".export.json")
		if !transferJobIDPattern.MatchString(jobID) {
			continue
		}
		if info, err := os.Lstat(recordPath); err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if _, exists := transferJobs.jobs[jobID]; exists {
			continue
		}
		raw, err := os.ReadFile(recordPath)
		if err != nil {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if stringFromMapValue(record, "job_id") != jobID || stringFromMapValue(record, "kind") != "export" ||
			stringFromMapValue(record, "status") != "completed" {
			continue
		}
		job := &transferJob{JobID: jobID, Kind: "export", Status: "completed", Progress: 100}
		job.Message = stringFromMapValue(record, "message")
		job.Filename = stringFromMapValue(record, "filename")
		if manifest, ok := record["manifest"].(map[string]any); ok {
			job.Manifest = manifest
		}
		job.TotalFiles = intValueFromAny(record["total_files"])
		job.ProcessedFiles = intValueFromAny(record["processed_files"])
		job.TotalBytes = int64FromAny(record["total_bytes"])
		job.ProcessedBytes = int64FromAny(record["processed_bytes"])
		job.CompletedAt = floatValueFromAny(record["completed_at"])
		job.ExpiresAt = floatValueFromAny(record["expires_at"])
		job.ArchiveBytes = int64FromAny(record["archive_bytes"])
		archivePath := filepath.Join(transferTempDir(), jobID+".zip")
		if info, err := os.Lstat(archivePath); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			job.FilePath = archivePath
		} else {
			job.Message = "导出文件已过期或已删除"
		}
		transferJobs.jobs[jobID] = job
	}
	recoverLegacyExportHistoryLocked()
}

func recoverLegacyExportHistoryLocked() {
	archives, err := filepath.Glob(filepath.Join(transferTempDir(), "*.zip"))
	if err != nil {
		return
	}
	for _, archivePath := range archives {
		jobID := strings.TrimSuffix(filepath.Base(archivePath), ".zip")
		if !transferJobIDPattern.MatchString(jobID) {
			continue
		}
		if _, exists := transferJobs.jobs[jobID]; exists {
			continue
		}
		if info, err := os.Lstat(archivePath); err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			continue
		}
		manifest, err := loadArchiveManifest(&zr.Reader)
		entryCount, entryBytes := 0, int64(0)
		for _, member := range zr.File {
			if member.Name == "manifest.json" {
				continue
			}
			entryCount++
			entryBytes += int64(member.UncompressedSize64)
		}
		zr.Close()
		if err != nil {
			continue
		}
		expectedFiles, expectedBytes := manifestPartTotals(manifest)
		if entryCount != expectedFiles || entryBytes != expectedBytes {
			continue
		}
		info, err := os.Stat(archivePath)
		if err != nil {
			continue
		}
		job := &transferJob{JobID: jobID, Kind: "export", Status: "completed", Progress: 100,
			Message: "导出完成", FilePath: archivePath, Manifest: manifest}
		job.Filename = "maibot-data-" + info.ModTime().Format("20060102-150405") + ".zip"
		job.TotalFiles, job.ProcessedFiles = expectedFiles, expectedFiles
		job.TotalBytes, job.ProcessedBytes = expectedBytes, expectedBytes
		job.CompletedAt = unixFloat(info.ModTime())
		job.ExpiresAt = job.CompletedAt + transferRetentionSecond
		job.ArchiveBytes = info.Size()
		transferJobs.jobs[jobID] = job
		saveExportHistory(job)
	}
}

func manifestPartTotals(manifest map[string]any) (int, int64) {
	parts, ok := manifest["parts"].(map[string]any)
	if !ok {
		return 0, 0
	}
	files, bytes := 0, int64(0)
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		files += intValueFromAny(partMap["file_count"])
		bytes += int64FromAny(partMap["total_bytes"])
	}
	return files, bytes
}

func saveExportHistory(job *transferJob) {
	recordPath := filepath.Join(transferTempDir(), job.JobID+".export.json")
	raw, err := json.Marshal(transferJobResponse(job))
	if err != nil {
		return
	}
	temporaryPath := recordPath + ".tmp"
	if err := os.WriteFile(temporaryPath, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(temporaryPath, recordPath)
}

func stringFromMapValue(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

func intValueFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	}
	return 0
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	}
	return 0
}

func floatValueFromAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	}
	return 0
}

// exportFile 记录待打包文件的来源路径与压缩包内路径
type exportFile struct {
	Path        string
	ArchiveName string
	Size        int64
}

var errExportCancelled = errors.New("导出已取消")

func isExcludedExportName(name string) bool {
	if transferExcludedPaths[name] {
		return true
	}
	for _, prefix := range transferExcludedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isExcludedArchivePath 判断压缩包内路径是否命中固定快照排除项（含其子路径）
func isExcludedArchivePath(name string, excluded map[string]bool) bool {
	if len(excluded) == 0 {
		return false
	}
	if excluded[name] {
		return true
	}
	for {
		index := strings.LastIndex(name, "/")
		if index < 0 {
			return false
		}
		name = name[:index]
		if excluded[name] {
			return true
		}
	}
}

func checkExportCancelled(job *transferJob) error {
	if job == nil {
		return nil
	}
	job.stateMu.Lock()
	cancelled := job.CancelRequested
	job.stateMu.Unlock()
	if cancelled {
		return errExportCancelled
	}
	return nil
}

func scanExportFiles(root string, archiveRoot string, job *transferJob, excluded map[string]bool) ([]exportFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode().IsRegular() {
		if err := checkExportCancelled(job); err != nil {
			return nil, err
		}
		archiveName := archiveRoot + "/" + info.Name()
		if isExcludedExportName(archiveName) || excluded[archiveName] {
			return nil, nil
		}
		return []exportFile{{Path: root, ArchiveName: archiveName, Size: info.Size()}}, nil
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	files := make([]exportFile, 0, 64)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkError error) error {
		if walkError != nil {
			return nil
		}
		if err := checkExportCancelled(job); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root {
				relative, relErr := filepath.Rel(rootResolved, path)
				if relErr == nil {
					archiveName := archiveRoot + "/" + filepath.ToSlash(relative)
					for _, prefix := range transferExcludedPrefixes {
						if strings.HasPrefix(prefix, archiveName+"/") {
							return filepath.SkipDir
						}
					}
					if isExcludedArchivePath(archiveName, excluded) {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if excluded != nil && excluded[path] {
			return nil
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return nil
		}
		relative, relErr := filepath.Rel(rootResolved, resolved)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil
		}
		archiveName := archiveRoot + "/" + filepath.ToSlash(relative)
		if isExcludedExportName(archiveName) || isExcludedArchivePath(archiveName, excluded) {
			return nil
		}
		fileInfo, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		files = append(files, exportFile{Path: resolved, ArchiveName: archiveName, Size: fileInfo.Size()})
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, errExportCancelled) {
			return nil, walkErr
		}
		return files, nil
	}
	return files, nil
}

func copyTransferFile(source string, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

// stageGraphSnapshots 固定有效图快照及其指针，避免压缩期间旧 generation 被轮换删除
func stageGraphSnapshots(dataRoot string, destination string, job *transferJob) ([]exportFile, map[string]bool, error) {
	excluded := map[string]bool{}
	files := []exportFile{}
	rootResolved, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return files, excluded, nil
		}
		return nil, nil, err
	}
	var pointers []string
	walkErr := filepath.WalkDir(rootResolved, func(path string, entry os.DirEntry, walkError error) error {
		if walkError != nil {
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() != "graph_snapshot.json" {
			return nil
		}
		pointers = append(pointers, path)
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	for _, pointerPath := range pointers {
		if err := checkExportCancelled(job); err != nil {
			return nil, nil, err
		}
		graphRoot := filepath.Dir(pointerPath)
		relativeRoot, relErr := filepath.Rel(rootResolved, graphRoot)
		if relErr != nil {
			continue
		}
		for attempt := 0; attempt < 3; attempt++ {
			staged, stageErr := stageSingleGraphSnapshot(pointerPath, graphRoot, relativeRoot, attempt, destination, job)
			if stageErr == nil {
				archiveRoot := "data"
				if relativeRoot != "." && relativeRoot != "" {
					archiveRoot = "data/" + filepath.ToSlash(relativeRoot)
				}
				stagedFiles, scanErr := scanExportFiles(staged, archiveRoot, job, nil)
				if scanErr != nil {
					return nil, nil, scanErr
				}
				files = append(files, stagedFiles...)
				excluded[filepath.ToSlash(filepath.Join("data", filepath.ToSlash(relativeRoot), "graph_snapshot.json"))] = true
				excluded[filepath.ToSlash(filepath.Join("data", filepath.ToSlash(relativeRoot), "graph_snapshots"))] = true
				break
			}
			if os.IsNotExist(stageErr) && attempt < 2 {
				continue
			}
			return nil, nil, stageErr
		}
	}
	return files, excluded, nil
}

func stageSingleGraphSnapshot(pointerPath string, graphRoot string, relativeRoot string, attempt int, destination string, job *transferJob) (string, error) {
	pointerBytes, err := os.ReadFile(pointerPath)
	if err != nil {
		return "", err
	}
	var pointer map[string]any
	if err := json.Unmarshal(pointerBytes, &pointer); err != nil {
		return "", err
	}
	generation, _ := pointer["generation"].(string)
	if !graphGenerationPattern.MatchString(generation) {
		return "", fmt.Errorf("图快照 generation 无效: %s", pointerPath)
	}
	source := filepath.Join(graphRoot, "graph_snapshots", generation)
	metadataBytes, err := os.ReadFile(filepath.Join(source, "graph_metadata.json"))
	if err != nil {
		return "", err
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return "", err
	}
	stagedRoot := filepath.Join(destination, fmt.Sprintf("%d", attempt), relativeRoot)
	stagedSnapshot := filepath.Join(stagedRoot, "graph_snapshots", generation)
	if err := os.MkdirAll(stagedSnapshot, 0o755); err != nil {
		return "", err
	}
	names := []string{"graph_metadata.json"}
	if hasAdjacency, _ := metadata["has_adjacency"].(bool); hasAdjacency {
		names = append(names, "graph_adjacency.npz")
	}
	for _, name := range names {
		if err := checkExportCancelled(job); err != nil {
			return "", err
		}
		if err := copyTransferFile(filepath.Join(source, name), filepath.Join(stagedSnapshot, name)); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(stagedRoot, "graph_snapshot.json"), pointerBytes, 0o644); err != nil {
		return "", err
	}
	return stagedRoot, nil
}

func buildExportManifest(parts []string, files []exportFile, version string, database ...map[string]any) map[string]any {
	partStats := map[string]any{}
	for _, part := range parts {
		partStats[part] = map[string]any{"file_count": 0, "total_bytes": 0}
	}
	for _, file := range files {
		part := strings.SplitN(file.ArchiveName, "/", 2)[0]
		stats, ok := partStats[part].(map[string]any)
		if !ok {
			continue
		}
		stats["file_count"] = intValueFromAny(stats["file_count"]) + 1
		stats["total_bytes"] = int64FromAny(stats["total_bytes"]) + file.Size
	}
	manifest := map[string]any{
		"format": transferArchiveFormat, "format_version": 1,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
		"maibot_version": version, "included": parts, "parts": partStats,
	}
	if len(database) > 0 && database[0] != nil {
		manifest["database"] = database[0]
	}
	return manifest
}

// updateTransferProgressLocked 要求调用方已持有 stateMu
func updateTransferProgressLocked(job *transferJob) {
	if job.Status == "cancelled" {
		return
	}
	if job.TotalBytes > 0 {
		job.Progress = min(99, int(job.ProcessedBytes*100/job.TotalBytes))
	} else if job.TotalFiles > 0 {
		job.Progress = min(99, job.ProcessedFiles*100/job.TotalFiles)
	} else {
		job.Progress = 99
	}
}

func writeExportArchive(job *transferJob, archivePath string, manifest map[string]any, files []exportFile) error {
	output, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(output)
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = zw.Close()
		_ = output.Close()
		return err
	}
	manifestEntry, err := zw.Create("manifest.json")
	if err != nil {
		_ = zw.Close()
		_ = output.Close()
		return err
	}
	if _, err := manifestEntry.Write(manifestRaw); err != nil {
		_ = zw.Close()
		_ = output.Close()
		return err
	}
	buffer := make([]byte, transferChunkSize)
	for _, file := range files {
		if err := checkExportCancelled(job); err != nil {
			_ = zw.Close()
			_ = output.Close()
			return err
		}
		if err := appendExportFile(zw, job, file, buffer); err != nil {
			_ = zw.Close()
			_ = output.Close()
			return err
		}
		job.stateMu.Lock()
		job.ProcessedFiles++
		updateTransferProgressLocked(job)
		job.stateMu.Unlock()
	}
	if err := zw.Close(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func appendExportFile(zw *zip.Writer, job *transferJob, file exportFile, buffer []byte) error {
	input, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("导出文件失败 (%s): %w", file.ArchiveName, err)
	}
	defer input.Close()
	entry, err := zw.Create(file.ArchiveName)
	if err != nil {
		return err
	}
	for {
		if err := checkExportCancelled(job); err != nil {
			return err
		}
		read, readErr := input.Read(buffer)
		if read > 0 {
			if _, writeErr := entry.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
			job.stateMu.Lock()
			job.ProcessedBytes += int64(read)
			updateTransferProgressLocked(job)
			job.stateMu.Unlock()
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("导出文件失败 (%s): %w", file.ArchiveName, readErr)
		}
	}
}

func removeTransferTempPath(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() {
		return os.Remove(path) == nil
	}
	return os.RemoveAll(path) == nil
}

const transferTempCleanupInterval = time.Hour

// cleanupTransferTempFilesAt 清理数据迁移临时目录，语义对齐 MaiBot cleanup_transfer_temp_files
// 返回删除条目数；活动、未到期、命名不匹配与符号链接条目保留
func cleanupTransferTempFilesAt(now time.Time) int {
	transferJobs.mu.Lock()
	defer transferJobs.mu.Unlock()
	restoreExportHistoryLocked()
	entries, err := os.ReadDir(transferTempDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		zap.L().Warn("读取数据迁移临时目录失败", zap.Error(err))
		return 0
	}
	deleted := 0
	for _, entry := range entries {
		path := filepath.Join(transferTempDir(), entry.Name())
		matches := transferTempEntryPattern.FindStringSubmatch(entry.Name())
		if matches == nil || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		jobID := matches[1]
		job := transferJobs.jobs[jobID]
		if job != nil {
			job.stateMu.Lock()
			blocked := job.WorkerActive || job.Status == "pending" || job.Status == "running" || job.ActiveDownloads > 0
			keep := blocked
			if !keep && job.Status == "completed" && job.ExpiresAt > 0 && float64(now.UnixNano())/1e9 < job.ExpiresAt {
				keep = true
			}
			job.stateMu.Unlock()
			if keep {
				continue
			}
		} else {
			info, statErr := entry.Info()
			if statErr != nil || now.Sub(info.ModTime()) < time.Duration(transferRetentionSecond)*time.Second {
				continue
			}
		}
		if !removeTransferTempPath(path) {
			continue
		}
		deleted++
		if job != nil {
			job.stateMu.Lock()
			if job.FilePath == path {
				job.FilePath = ""
				if job.Status == "completed" {
					job.Message = "导出文件已过期，请重新导出"
				}
			}
			job.stateMu.Unlock()
		}
	}
	if deleted > 0 {
		zap.L().Info("数据迁移临时文件清理完成", zap.Int("deleted", deleted))
	}
	return deleted
}

// cleanupTransferTempFiles 按当前时间清理一次临时目录
func cleanupTransferTempFiles() int { return cleanupTransferTempFilesAt(time.Now()) }

// RunPeriodicTransferTempCleanup 启动立即清理一次，之后每小时清理，ctx 取消后退出
func RunPeriodicTransferTempCleanup(ctx context.Context) {
	runTransferTempCleanupLoop(ctx, transferTempCleanupInterval)
}

func runTransferTempCleanupLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = transferTempCleanupInterval
	}
	cleanupTransferTempFiles()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupTransferTempFiles()
		}
	}
}

func runExportJob(job *transferJob, includePlugins bool, includeLogs bool, instanceDir string, version string, databaseDSN string) {
	job.stateMu.Lock()
	job.WorkerActive = true
	job.stateMu.Unlock()
	defer func() {
		job.stateMu.Lock()
		job.WorkerActive = false
		job.stateMu.Unlock()
	}()
	archivePath := filepath.Join(transferTempDir(), job.JobID+".zip")
	completed := false
	defer func() {
		if !completed {
			removeTransferTempPath(archivePath)
			job.stateMu.Lock()
			job.FilePath = ""
			job.stateMu.Unlock()
		}
	}()

	job.stateMu.Lock()
	job.Status = "running"
	job.Message = "正在扫描需要导出的文件"
	job.stateMu.Unlock()
	parts := append([]string{}, transferRequiredParts...)
	if includePlugins {
		parts = append(parts, "plugins")
	}
	if includeLogs {
		parts = append(parts, "logs")
	}
	exportDirs := map[string]string{
		"config":  filepath.Join(instanceDir, "config"),
		"data":    filepath.Join(instanceDir, "data"),
		"plugins": filepath.Join(instanceDir, "plugins"),
		"logs":    filepath.Join(instanceDir, "logs"),
	}
	snapshotDir, err := os.MkdirTemp(transferTempDir(), job.JobID+"-snapshots-")
	if err != nil {
		failTransferJob(job, err)
		return
	}
	defer os.RemoveAll(snapshotDir)

	job.stateMu.Lock()
	job.Message = "正在固定记忆图快照"
	job.stateMu.Unlock()
	stagedFiles, excludedPaths, err := stageGraphSnapshots(exportDirs["data"], snapshotDir, job)
	if err != nil {
		if errors.Is(err, errExportCancelled) {
			return
		}
		failTransferJob(job, err)
		return
	}
	job.stateMu.Lock()
	job.Message = "正在扫描需要导出的文件"
	job.stateMu.Unlock()
	files := stagedFiles
	for _, part := range parts {
		if err := checkExportCancelled(job); err != nil {
			return
		}
		partFiles, scanErr := scanExportFiles(exportDirs[part], part, job, excludedPaths)
		if scanErr != nil {
			if errors.Is(scanErr, errExportCancelled) {
				return
			}
			failTransferJob(job, scanErr)
			return
		}
		files = append(files, partFiles...)
	}
	// 数据库不再是独立快照：SQLite 库文件就在 data/ 目录里，随 data 部分一起打包，
	// 对齐 MaiBot src/webui/routers/data_transfer.py 的 _EXPORT_DIRS / _iter_export_files
	// （Python 侧没有 pg_dump 这一步，库文件是普通被导出的文件之一）。
	totalBytes := int64(0)
	for _, file := range files {
		totalBytes += file.Size
	}
	manifest := buildExportManifest(parts, files, version)
	job.stateMu.Lock()
	job.TotalFiles = len(files)
	job.TotalBytes = totalBytes
	job.Manifest = manifest
	job.Filename = "maibot-data-" + time.Now().Format("20060102-150405") + ".zip"
	job.FilePath = archivePath
	job.Message = "正在写入压缩包"
	job.stateMu.Unlock()
	if err := writeExportArchive(job, archivePath, manifest, files); err != nil {
		if errors.Is(err, errExportCancelled) {
			return
		}
		failTransferJob(job, err)
		return
	}
	if err := checkExportCancelled(job); err != nil {
		return
	}
	stat, err := os.Stat(archivePath)
	if err != nil {
		failTransferJob(job, err)
		return
	}
	completed = true
	job.stateMu.Lock()
	job.ExpiresAt = unixFloat(time.Now().Add(transferRetentionSecond * time.Second))
	job.Status = "completed"
	job.Progress = 100
	job.Message = "导出完成"
	job.CompletedAt = unixFloat(time.Now())
	job.ArchiveBytes = stat.Size()
	job.stateMu.Unlock()
	transferJobs.mu.Lock()
	saveExportHistory(job)
	transferJobs.mu.Unlock()
}

func failTransferJob(job *transferJob, err error) {
	detail := err.Error()
	var typed *transferError
	if errors.As(err, &typed) {
		detail = typed.Detail
	}
	job.stateMu.Lock()
	// 先写 error/message 再写 status，保证读到 failed 时错误文案已就绪
	job.Error = detail
	job.Message = "导出失败"
	job.Status = "failed"
	job.stateMu.Unlock()
}

func (d Deps) handleDataTransferExport(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	decoded, err := decodeAnyBody(r)
	if err != nil {
		writeSystemBadJSON(w, err)
		return
	}
	body, _ := decoded.(map[string]any)
	includePlugins, _ := body["include_plugins"].(bool)
	includeLogs, _ := body["include_logs"].(bool)
	job, err := newTransferJob("export")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "创建导出任务失败: " + err.Error()})
		return
	}
	go runExportJob(job, includePlugins, includeLogs, d.InstanceDir, d.buildVersion(), "")
	writeJSON(w, http.StatusOK, transferJobResponse(job))
}

func (s *transferJobStore) lookupExport(jobID string) (*transferJob, bool) {
	s.mu.Lock()
	job := s.jobs[jobID]
	s.mu.Unlock()
	if job != nil && job.Kind == "export" {
		return job, true
	}
	s.mu.Lock()
	restoreExportHistoryLocked()
	job = s.jobs[jobID]
	s.mu.Unlock()
	if job == nil || job.Kind != "export" {
		return nil, false
	}
	return job, true
}

func (d Deps) handleDataTransferJob(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	jobID := chiURLParam(r, "job_id")
	job, ok := transferJobs.lookupExport(jobID)
	if !ok {
		job, ok = transferJobs.lookup(jobID, "")
	}
	if !ok || job == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定的数据迁移任务"})
		return
	}
	writeJSON(w, http.StatusOK, transferJobResponse(job))
}

func (d Deps) handleDataTransferExports(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	transferJobs.mu.Lock()
	restoreExportHistoryLocked()
	snapshots := make([]transferJobSnapshot, 0, len(transferJobs.jobs))
	for _, job := range transferJobs.jobs {
		snapshot := job.snapshot()
		if snapshot.Kind == "export" && snapshot.Status == "completed" {
			snapshots = append(snapshots, snapshot)
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].CompletedAt > snapshots[j].CompletedAt })
	payload := make([]map[string]any, 0, len(snapshots))
	for index := range snapshots {
		if snapshots[index].FilePath != "" {
			if info, err := os.Stat(snapshots[index].FilePath); err != nil || !info.Mode().IsRegular() {
				if job := transferJobs.jobs[snapshots[index].JobID]; job != nil {
					job.stateMu.Lock()
					job.FilePath = ""
					job.Message = "导出文件已过期或已删除"
					job.stateMu.Unlock()
				}
				snapshots[index].FilePath = ""
				snapshots[index].Message = "导出文件已过期或已删除"
			}
		}
		payload = append(payload, transferJobResponseFromSnapshot(snapshots[index]))
	}
	transferJobs.mu.Unlock()
	writeJSON(w, http.StatusOK, payload)
}

func (d Deps) handleDataTransferDownload(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	jobID := chiURLParam(r, "job_id")
	job, ok := transferJobs.lookupExport(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定的数据迁移任务"})
		return
	}
	job.stateMu.Lock()
	if job.Status != "completed" || job.FilePath == "" {
		job.stateMu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "导出任务尚未完成或文件已失效"})
		return
	}
	if info, err := os.Stat(job.FilePath); err != nil || !info.Mode().IsRegular() {
		job.stateMu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "导出任务尚未完成或文件已失效"})
		return
	}
	job.ActiveDownloads++
	job.ExpiresAt = unixFloat(time.Now().Add(transferRetentionSecond * time.Second))
	filename := job.Filename
	if filename == "" {
		filename = "maibot-data.zip"
	}
	path := job.FilePath
	job.stateMu.Unlock()
	defer func() {
		job.stateMu.Lock()
		job.ActiveDownloads--
		job.ExpiresAt = unixFloat(time.Now().Add(transferRetentionSecond * time.Second))
		completedAt := job.CompletedAt
		job.stateMu.Unlock()
		if completedAt > 0 {
			transferJobs.mu.Lock()
			saveExportHistory(job)
			transferJobs.mu.Unlock()
		}
	}()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	http.ServeFile(w, r, path)
}

func (d Deps) handleDataTransferCancel(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	jobID := chiURLParam(r, "job_id")
	job, ok := transferJobs.lookupExport(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定的数据迁移任务"})
		return
	}
	transferJobs.mu.Lock()
	job.stateMu.Lock()
	if job.Status != "completed" && job.Status != "failed" && job.Status != "cancelled" {
		job.CancelRequested = true
		job.Status = "cancelled"
		job.Progress = 0
		job.Message = "导出已取消"
		job.Error = ""
	}
	snapshot := job.snapshotLocked()
	job.stateMu.Unlock()
	transferJobs.mu.Unlock()
	writeJSON(w, http.StatusOK, transferJobResponseFromSnapshot(snapshot))
}

func (d Deps) handleDataTransferDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	jobID := chiURLParam(r, "job_id")
	transferJobs.mu.Lock()
	restoreExportHistoryLocked()
	job := transferJobs.jobs[jobID]
	if job == nil {
		transferJobs.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定的数据迁移任务"})
		return
	}
	job.stateMu.Lock()
	blocked := job.WorkerActive || job.Status == "pending" || job.Status == "running" || job.ActiveDownloads > 0
	filePath := job.FilePath
	job.stateMu.Unlock()
	if blocked {
		transferJobs.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "任务仍在处理或下载中，请稍后删除"})
		return
	}
	if filePath != "" && !removeTransferTempPath(filePath) {
		transferJobs.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "删除导出文件失败，请查看日志"})
		return
	}
	_ = os.Remove(filepath.Join(transferTempDir(), jobID+".export.json"))
	delete(transferJobs.jobs, jobID)
	transferJobs.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
