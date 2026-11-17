package host

// 每个 Bot 的数据导入导出任务；宿主停止 Bot 后在实例锁内执行。
// 对齐 src/host/transfer_service.py。

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"MaiGo/internal/common/version"
)

const (
	exportRetentionSeconds = 24 * 60 * 60
	transferChunkSize      = 1024 * 1024
)

var transferJobPattern = `[0-9a-f]{32}`

type transferJob struct {
	mu              sync.Mutex
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
	ExpiresAt       *float64
	CompletedAt     *float64
	ArchiveBytes    int64
}

// TransferService 单个 Bot 的传输任务、历史与临时目录
type TransferService struct {
	root        string
	transferDir string
	version     string
	bot         BotRecord
	host        string

	jobsMu sync.Mutex
	jobs   map[string]*transferJob

	runOperation func(operation func() error) error
}

// NewTransferService 创建传输服务
func NewTransferService(bot BotRecord, host string, transferDir string, runOperation func(func() error) error) *TransferService {
	service := &TransferService{
		root: bot.Directory, transferDir: transferDir, version: version.String(), bot: bot, host: host,
		jobs: map[string]*transferJob{}, runOperation: runOperation,
	}
	_ = os.MkdirAll(transferDir, 0o777)
	return service
}

func newJobID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return strings.ReplaceAll(fmt.Sprintf("%032x", time.Now().UnixNano()), "-", "")[:32]
	}
	return hex.EncodeToString(buffer)
}

func (s *TransferService) jobResponse(job *transferJob) map[string]any {
	job.mu.Lock()
	defer job.mu.Unlock()
	var downloadURL any
	if job.Kind == "export" && job.Status == "completed" && job.FilePath != "" {
		downloadURL = "/api/webui/data-transfer/export/" + job.JobID + "/download"
	}
	return map[string]any{
		"job_id": job.JobID, "kind": job.Kind, "status": job.Status, "progress": job.Progress,
		"message": job.Message, "total_files": job.TotalFiles, "processed_files": job.ProcessedFiles,
		"total_bytes": job.TotalBytes, "processed_bytes": job.ProcessedBytes, "filename": nullableString(job.Filename),
		"download_url": downloadURL, "manifest": job.Manifest, "error": nullableString(job.Error),
		"completed_at": job.CompletedAt, "expires_at": job.ExpiresAt, "archive_bytes": job.ArchiveBytes,
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *TransferService) newJob(kind string) *transferJob {
	_ = os.MkdirAll(s.transferDir, 0o777)
	job := &transferJob{JobID: newJobID(), Kind: kind, Status: "pending", Message: "等待处理"}
	s.jobsMu.Lock()
	s.jobs[job.JobID] = job
	s.jobsMu.Unlock()
	return job
}

func (s *TransferService) getJob(jobID, kind string) (*transferJob, error) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	s.jobsMu.Unlock()
	if job == nil && kind == "export" {
		s.restoreExportHistory()
		s.jobsMu.Lock()
		job = s.jobs[jobID]
		s.jobsMu.Unlock()
	}
	if job == nil || (kind != "" && job.Kind != kind) {
		return nil, errors.New("未找到指定的数据迁移任务")
	}
	return job, nil
}

func (s *TransferService) execute(job *transferJob, operation func() error) {
	go func() {
		var err error
		if s.runOperation != nil {
			err = s.runOperation(operation)
		} else {
			err = operation()
		}
		if err != nil {
			job.mu.Lock()
			if job.Status == "completed" {
				job.Status = "failed"
				job.Error = "数据传输已完成，但恢复 Bot 失败: " + err.Error()
			} else if job.Status == "pending" || job.Status == "running" {
				job.Status = "failed"
				job.Error = err.Error()
			}
			job.Message = "传输失败"
			job.WorkerActive = false
			job.mu.Unlock()
		}
	}()
}

func (s *TransferService) saveExportHistory(job *transferJob) {
	recordPath := filepath.Join(s.transferDir, job.JobID+".export.json")
	payload := s.jobResponse(job)
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = WritePrivate(recordPath, string(encoded))
}

func (s *TransferService) restoreExportHistory() {
	entries, _ := os.ReadDir(s.transferDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".export.json") {
			continue
		}
		jobID := strings.TrimSuffix(entry.Name(), ".export.json")
		if !isHex32(jobID) {
			continue
		}
		s.jobsMu.Lock()
		_, exists := s.jobs[jobID]
		s.jobsMu.Unlock()
		if exists {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.transferDir, entry.Name()))
		if err != nil {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil || hostString(record["status"]) != "completed" {
			continue
		}
		job := &transferJob{JobID: jobID, Kind: "export", Status: "completed", Progress: 100}
		job.Message = hostString(record["message"])
		job.Filename = hostString(record["filename"])
		job.TotalFiles = int(numberFromAny(record["total_files"]))
		job.ProcessedFiles = int(numberFromAny(record["processed_files"]))
		job.TotalBytes = int64(numberFromAny(record["total_bytes"]))
		job.ProcessedBytes = int64(numberFromAny(record["processed_bytes"]))
		job.ArchiveBytes = int64(numberFromAny(record["archive_bytes"]))
		if manifest, ok := record["manifest"].(map[string]any); ok {
			job.Manifest = manifest
		}
		if value, ok := record["completed_at"].(float64); ok {
			job.CompletedAt = &value
		}
		if value, ok := record["expires_at"].(float64); ok {
			job.ExpiresAt = &value
		}
		archivePath := filepath.Join(s.transferDir, jobID+".zip")
		if info, err := os.Stat(archivePath); err == nil && info.Mode().IsRegular() {
			job.FilePath = archivePath
		} else {
			job.Message = "导出文件已过期或已删除"
		}
		s.jobsMu.Lock()
		s.jobs[jobID] = job
		s.jobsMu.Unlock()
	}
}

func numberFromAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func isHex32(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (s *TransferService) cleanupTransferTempFiles() int {
	deleted := 0
	if _, err := os.Stat(s.transferDir); err != nil {
		return 0
	}
	s.restoreExportHistory()
	now := time.Now().Unix()
	entries, _ := os.ReadDir(s.transferDir)
	for _, entry := range entries {
		if entry.IsDir() || isDirectoryLink(filepath.Join(s.transferDir, entry.Name())) {
			continue
		}
		name := entry.Name()
		jobID := ""
		if isHex32(strings.TrimSuffix(name, ".zip")) {
			jobID = strings.TrimSuffix(name, ".zip")
		} else if isHex32(strings.TrimSuffix(name, "-upload.zip")) {
			jobID = strings.TrimSuffix(name, "-upload.zip")
		}
		if jobID == "" {
			continue
		}
		s.jobsMu.Lock()
		job := s.jobs[jobID]
		s.jobsMu.Unlock()
		if job != nil {
			job.mu.Lock()
			active := job.WorkerActive || job.Status == "pending" || job.Status == "running" || job.ActiveDownloads > 0
			expires := job.ExpiresAt
			job.mu.Unlock()
			if active {
				continue
			}
			if expires != nil && float64(now) < *expires {
				continue
			}
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if job == nil && float64(now)-float64(info.ModTime().Unix()) < exportRetentionSeconds {
			continue
		}
		if os.Remove(filepath.Join(s.transferDir, name)) == nil {
			deleted++
		}
	}
	return deleted
}

func (s *TransferService) export(request map[string]any) *transferJob {
	job := s.newJob("export")
	includePlugins, _ := request["include_plugins"].(bool)
	includeLogs, _ := request["include_logs"].(bool)
	s.execute(job, func() error {
		s.runExportJob(job, includePlugins, includeLogs)
		return nil
	})
	return job
}

func (s *TransferService) runExportJob(job *transferJob, includePlugins, includeLogs bool) {
	if s.isCancelled(job) {
		return
	}
	job.mu.Lock()
	job.WorkerActive = true
	job.Status = "running"
	job.Message = "正在扫描需要导出的文件"
	job.mu.Unlock()
	var archivePath string
	snapshotDirectory := ""
	defer func() {
		if snapshotDirectory != "" {
			_ = os.RemoveAll(snapshotDirectory)
		}
		job.mu.Lock()
		if job.Status != "completed" && archivePath != "" {
			_ = os.Remove(archivePath)
			job.FilePath = ""
		}
		job.WorkerActive = false
		job.mu.Unlock()
	}()
	parts := []string{"config", "data"}
	if includePlugins {
		parts = append(parts, "plugins")
	}
	if includeLogs {
		parts = append(parts, "logs")
	}
	snapshotDirectory, err := os.MkdirTemp(s.transferDir, job.JobID+"-snapshots-")
	if err != nil {
		s.failJob(job, err)
		return
	}
	for _, part := range parts {
		root := filepath.Join(s.root, part)
		if isDirectoryLink(root) || !pathContains(s.root, root) {
			s.failJob(job, errors.New("导出目录不能指向实例以外或使用目录链接"))
			return
		}
	}
	files := make([]exportFile, 0)
	excluded := map[string]bool{}
	configFiles, configExcluded, err := portableConfig(s.bot, s.host, snapshotDirectory)
	if err != nil {
		s.failJob(job, err)
		return
	}
	files = append(files, configFiles...)
	for path := range configExcluded {
		excluded[path] = true
	}
	for _, part := range parts {
		if s.isCancelled(job) {
			return
		}
		files = append(files, s.iterExportFiles(filepath.Join(s.root, part), part, job, excluded)...)
	}
	totalBytes := int64(0)
	for _, file := range files {
		totalBytes += file.size
	}
	manifest := s.buildManifest(parts, files)
	job.mu.Lock()
	job.TotalFiles = len(files)
	job.TotalBytes = totalBytes
	job.Manifest = manifest
	job.Filename = "maibot-data-" + transferTimestamp() + ".zip"
	archivePath = filepath.Join(s.transferDir, job.JobID+".zip")
	job.FilePath = archivePath
	job.Message = "正在写入压缩包"
	job.mu.Unlock()
	archiveFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		s.failJob(job, err)
		return
	}
	writer := zip.NewWriter(archiveFile)
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	manifestWriter, err := writer.Create("manifest.json")
	if err == nil {
		_, err = manifestWriter.Write(manifestBytes)
	}
	if err != nil {
		_ = writer.Close()
		_ = archiveFile.Close()
		s.failJob(job, err)
		return
	}
	for _, file := range files {
		if s.isCancelled(job) {
			_ = writer.Close()
			_ = archiveFile.Close()
			return
		}
		if err := s.writeArchiveFile(writer, file, job); err != nil {
			_ = writer.Close()
			_ = archiveFile.Close()
			s.failJob(job, err)
			return
		}
		job.mu.Lock()
		job.ProcessedFiles++
		s.updateProgress(job)
		job.mu.Unlock()
	}
	if err := writer.Close(); err != nil {
		_ = archiveFile.Close()
		s.failJob(job, err)
		return
	}
	if err := archiveFile.Close(); err != nil {
		s.failJob(job, err)
		return
	}
	if s.isCancelled(job) {
		return
	}
	info, _ := os.Stat(archivePath)
	now := float64(time.Now().UnixNano()) / 1e9
	expires := now + exportRetentionSeconds
	job.mu.Lock()
	job.ExpiresAt = &expires
	job.Status = "completed"
	job.Progress = 100
	job.Message = "导出完成"
	job.CompletedAt = &now
	if info != nil {
		job.ArchiveBytes = info.Size()
	}
	job.mu.Unlock()
	s.saveExportHistory(job)
}

func (s *TransferService) failJob(job *transferJob, err error) {
	job.mu.Lock()
	if job.Status != "cancelled" {
		job.Status = "failed"
		job.Error = err.Error()
		job.Message = "导出失败"
	}
	job.mu.Unlock()
}

func (s *TransferService) isCancelled(job *transferJob) bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	if !job.CancelRequested {
		return false
	}
	job.Status = "cancelled"
	job.Progress = 0
	job.Message = "导出已取消"
	job.Error = ""
	return true
}

func (s *TransferService) iterExportFiles(root, archiveRoot string, job *transferJob, excluded map[string]bool) []exportFile {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	files := make([]exportFile, 0)
	if info.Mode().IsRegular() {
		return append(files, exportFile{path: root, archiveName: archiveRoot + "/" + info.Name(), size: info.Size()})
	}
	rootResolved := resolvedPath(root)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || !info.Mode().IsRegular() || isDirectoryLink(path) {
			return nil
		}
		resolved := resolvedPath(path)
		if !pathContains(rootResolved, resolved) {
			return nil
		}
		for excludedPath := range excluded {
			if excludedPath == resolved || pathContains(excludedPath, resolved) {
				return nil
			}
		}
		relative, err := filepath.Rel(rootResolved, resolved)
		if err != nil {
			return nil
		}
		archiveName := archiveRoot + "/" + filepath.ToSlash(relative)
		if archiveName == "data/.a_memorix_runtime_writer.lock" ||
			archiveName == "data/a-memorix/.a_memorix_runtime_writer.lock" ||
			strings.HasPrefix(archiveName, "data/prompt_imgs/") {
			return nil
		}
		files = append(files, exportFile{path: resolved, archiveName: archiveName, size: info.Size()})
		return nil
	})
	return files
}

func (s *TransferService) buildManifest(parts []string, files []exportFile) map[string]any {
	stats := map[string]map[string]int64{}
	for _, part := range parts {
		stats[part] = map[string]int64{"file_count": 0, "total_bytes": 0}
	}
	for _, file := range files {
		part := strings.SplitN(file.archiveName, "/", 2)[0]
		if entry, exists := stats[part]; exists {
			entry["file_count"]++
			entry["total_bytes"] += file.size
		}
	}
	return map[string]any{
		"format": "maibot-data-archive", "format_version": 1,
		"created_at": time.Now().UTC().Format(time.RFC3339), "maibot_version": s.version,
		"included": parts, "parts": stats,
	}
}

func (s *TransferService) updateProgress(job *transferJob) {
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

func (s *TransferService) writeArchiveFile(writer *zip.Writer, file exportFile, job *transferJob) error {
	header := &zip.FileHeader{Name: file.archiveName, Method: zip.Deflate}
	header.SetMode(0o644)
	target, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	source, err := os.Open(file.path)
	if err != nil {
		return err
	}
	defer source.Close()
	buffer := make([]byte, transferChunkSize)
	for {
		if s.isCancelled(job) {
			return errors.New("导出已取消")
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := target.Write(buffer[:count]); err != nil {
				return err
			}
			job.mu.Lock()
			job.ProcessedBytes += int64(count)
			s.updateProgress(job)
			job.mu.Unlock()
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (s *TransferService) loadArchiveManifest(reader *zip.Reader) (map[string]any, error) {
	for _, member := range reader.File {
		if member.Name != "manifest.json" {
			continue
		}
		source, err := member.Open()
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(source, 8<<20))
		_ = source.Close()
		if err != nil {
			return nil, errors.New("manifest.json 不是合法 JSON")
		}
		var manifest map[string]any
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, errors.New("manifest.json 不是合法 JSON")
		}
		if hostString(manifest["format"]) != "maibot-data-archive" || numberFromAny(manifest["format_version"]) != 1 {
			return nil, errors.New("不支持的数据包格式")
		}
		return manifest, nil
	}
	return nil, errors.New("压缩包缺少 manifest.json")
}

func (s *TransferService) validateArchiveMembers(reader *zip.Reader) error {
	for _, member := range reader.File {
		if _, _, err := safeZipMemberPath(member.Name); err != nil {
			return err
		}
		if member.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("压缩包不允许包含符号链接: %s", member.Name)
		}
	}
	return nil
}

func (s *TransferService) importUploaded(archivePath string, parts map[string]struct{}) *transferJob {
	job := s.newJob("import")
	s.execute(job, func() error {
		job.mu.Lock()
		job.WorkerActive = true
		job.Status = "running"
		job.Message = "正在校验压缩包"
		job.mu.Unlock()
		err := importArchive(s.bot, s.host, s, archivePath, parts, job)
		job.mu.Lock()
		defer job.mu.Unlock()
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
			job.Message = "导入失败"
		} else {
			job.Status = "completed"
			job.Progress = 100
			job.Message = "导入完成"
		}
		job.WorkerActive = false
		_ = os.Remove(archivePath)
		return nil
	})
	return job
}

// handleDataTransfer 分发宿主的 data-transfer 子路径
func (s *TransferService) handleDataTransfer(rw http.ResponseWriter, r *http.Request, transferPath string) {
	segments := strings.Split(strings.Trim(transferPath, "/"), "/")
	switch {
	case r.Method == http.MethodPost && len(segments) == 1 && segments[0] == "export":
		var body struct {
			IncludePlugins bool `json:"include_plugins"`
			IncludeLogs    bool `json:"include_logs"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		job := s.export(map[string]any{"include_plugins": body.IncludePlugins, "include_logs": body.IncludeLogs})
		writeJSONResponse(rw, http.StatusOK, s.jobResponse(job))
	case r.Method == http.MethodGet && len(segments) == 2 && segments[0] == "jobs":
		s.getJobResponse(rw, segments[1])
	case r.Method == http.MethodDelete && len(segments) == 2 && segments[0] == "jobs":
		s.deleteJob(rw, segments[1])
	case r.Method == http.MethodGet && len(segments) == 1 && segments[0] == "exports":
		s.listExports(rw)
	case r.Method == http.MethodGet && len(segments) == 3 && segments[0] == "export" && segments[2] == "download":
		s.downloadExport(rw, r, segments[1])
	case r.Method == http.MethodPost && len(segments) == 3 && segments[0] == "export" && segments[2] == "cancel":
		s.cancelExport(rw, segments[1])
	case r.Method == http.MethodPost && len(segments) == 1 && segments[0] == "import":
		s.createImport(rw, r)
	default:
		writeJSONResponse(rw, http.StatusNotFound, map[string]any{"detail": "未找到接口"})
	}
}

func (s *TransferService) getJobResponse(rw http.ResponseWriter, jobID string) {
	job, err := s.getJob(jobID, "")
	if err != nil {
		writeJSONResponse(rw, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	writeJSONResponse(rw, http.StatusOK, s.jobResponse(job))
}

func (s *TransferService) listExports(rw http.ResponseWriter) {
	s.restoreExportHistory()
	s.jobsMu.Lock()
	jobs := make([]*transferJob, 0)
	for _, job := range s.jobs {
		if job.Kind == "export" && job.Status == "completed" {
			jobs = append(jobs, job)
		}
	}
	s.jobsMu.Unlock()
	sort.Slice(jobs, func(i, j int) bool {
		left, right := jobs[i], jobs[j]
		left.mu.Lock()
		right.mu.Lock()
		leftTime, rightTime := float64(0), float64(0)
		if left.CompletedAt != nil {
			leftTime = *left.CompletedAt
		}
		if right.CompletedAt != nil {
			rightTime = *right.CompletedAt
		}
		left.mu.Unlock()
		right.mu.Unlock()
		return leftTime > rightTime
	})
	payload := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		job.mu.Lock()
		if job.FilePath != "" {
			if _, err := os.Stat(job.FilePath); err != nil {
				job.FilePath = ""
				job.Message = "导出文件已过期或已删除"
			}
		}
		job.mu.Unlock()
		payload = append(payload, s.jobResponse(job))
	}
	writeJSONResponse(rw, http.StatusOK, payload)
}

func (s *TransferService) downloadExport(rw http.ResponseWriter, r *http.Request, jobID string) {
	job, err := s.getJob(jobID, "export")
	if err != nil {
		writeJSONResponse(rw, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	job.mu.Lock()
	if job.Status != "completed" || job.FilePath == "" {
		job.mu.Unlock()
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "导出任务尚未完成或文件已失效"})
		return
	}
	info, statErr := os.Stat(job.FilePath)
	if statErr != nil || !info.Mode().IsRegular() {
		job.mu.Unlock()
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "导出任务尚未完成或文件已失效"})
		return
	}
	expires := float64(time.Now().UnixNano())/1e9 + exportRetentionSeconds
	job.ExpiresAt = &expires
	job.ActiveDownloads++
	filename := job.Filename
	filePath := job.FilePath
	job.mu.Unlock()
	defer func() {
		job.mu.Lock()
		job.ActiveDownloads--
		expires := float64(time.Now().UnixNano())/1e9 + exportRetentionSeconds
		job.ExpiresAt = &expires
		job.mu.Unlock()
		s.saveExportHistory(job)
	}()
	rw.Header().Set("Content-Type", "application/zip")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(filename, `"`, "")+`"`)
	http.ServeFile(rw, r, filePath)
}

func (s *TransferService) cancelExport(rw http.ResponseWriter, jobID string) {
	job, err := s.getJob(jobID, "export")
	if err != nil {
		writeJSONResponse(rw, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	job.mu.Lock()
	if job.Status != "completed" && job.Status != "failed" && job.Status != "cancelled" {
		job.CancelRequested = true
		job.Status = "cancelled"
		job.Progress = 0
		job.Message = "导出已取消"
		job.Error = ""
	}
	job.mu.Unlock()
	writeJSONResponse(rw, http.StatusOK, s.jobResponse(job))
}

func (s *TransferService) createImport(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "请上传 .zip 格式的数据包"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "请上传 .zip 格式的数据包"})
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "请上传 .zip 格式的数据包"})
		return
	}
	parts := map[string]struct{}{}
	for _, part := range []string{"config", "data", "plugins", "logs"} {
		if formBool(r, "import_"+part) {
			parts[part] = struct{}{}
		}
	}
	if len(parts) == 0 {
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "请至少选择一个导入范围"})
		return
	}
	job := s.newJob("import")
	uploadPath := filepath.Join(s.transferDir, job.JobID+"-upload.zip")
	output, err := os.OpenFile(uploadPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := io.Copy(output, io.LimitReader(file, 4<<30)); err != nil {
		_ = output.Close()
		_ = os.Remove(uploadPath)
		writeJSONResponse(rw, http.StatusBadRequest, map[string]any{"detail": "保存上传文件失败"})
		return
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(uploadPath)
		writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	job = s.importUploaded(uploadPath, parts)
	writeJSONResponse(rw, http.StatusOK, map[string]any{"job_id": job.JobID, "status": job.Status})
}

func formBool(r *http.Request, name string) bool {
	value := strings.TrimSpace(r.FormValue(name))
	if value == "" {
		return name == "import_config" || name == "import_data"
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func (s *TransferService) deleteJob(rw http.ResponseWriter, jobID string) {
	job, err := s.getJob(jobID, "")
	if err != nil {
		writeJSONResponse(rw, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	job.mu.Lock()
	if job.WorkerActive || job.Status == "pending" || job.Status == "running" || job.ActiveDownloads > 0 {
		job.mu.Unlock()
		writeJSONResponse(rw, http.StatusConflict, map[string]any{"detail": "任务仍在处理或下载中，请稍后删除"})
		return
	}
	filePath := job.FilePath
	job.mu.Unlock()
	if filePath != "" {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			writeJSONResponse(rw, http.StatusInternalServerError, map[string]any{"detail": "删除导出文件失败"})
			return
		}
	}
	_ = os.Remove(filepath.Join(s.transferDir, jobID+".export.json"))
	s.jobsMu.Lock()
	delete(s.jobs, jobID)
	s.jobsMu.Unlock()
	writeJSONResponse(rw, http.StatusOK, map[string]any{"success": true})
}

// hostDataTransfer 按 Bot 路由传输请求；Bot 停止时接口和任务仍然可用
type hostDataTransfer struct {
	webui    *WebUI
	mu       sync.Mutex
	services map[string]*TransferService
}

func (w *WebUI) mountDataTransfer(router chi.Router) {
	transfer := &hostDataTransfer{webui: w, services: map[string]*TransferService{}}
	router.HandleFunc("/api/host/bots/{bot_id}/proxy/api/webui/data-transfer/*", transfer.dispatch)
}

func (h *hostDataTransfer) service(bot BotRecord) *TransferService {
	h.mu.Lock()
	defer h.mu.Unlock()
	service := h.services[bot.BotID]
	if service == nil {
		hostDir := h.webui.supervisor.workspace.HostDirectory()
		service = NewTransferService(bot, hostDir, filepath.Join(hostDir, "transfers", bot.BotID), func(operation func() error) error {
			return h.webui.maintenance.TransferData(bot.BotID, operation)
		})
		h.services[bot.BotID] = service
	}
	return service
}

func (h *hostDataTransfer) dispatch(rw http.ResponseWriter, r *http.Request) {
	if !h.webui.requireSession(r) {
		writeJSONResponse(rw, http.StatusUnauthorized, map[string]any{"detail": "请先登录 WebUI"})
		return
	}
	botID := chi.URLParam(r, "bot_id")
	bot, err := h.webui.supervisor.workspace.Registry.GetBot(botID)
	if err != nil {
		writeJSONResponse(rw, http.StatusNotFound, map[string]any{"detail": "未找到 Bot"})
		return
	}
	service := h.service(bot)
	service.cleanupTransferTempFiles()
	path := chi.URLParam(r, "*")
	service.handleDataTransfer(rw, r, path)
}
