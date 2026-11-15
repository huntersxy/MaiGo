package webuiapi

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 本文件实现数据包导入校验与上传处理

var transferAllowedImportParts = map[string]bool{"config": true, "data": true, "plugins": true, "logs": true}

func safeZipMemberPath(memberName string) (string, string, error) {
	normalized := strings.ReplaceAll(memberName, "\\", "/")
	normalized = strings.TrimLeft(normalized, "/")
	if normalized == "" || strings.HasSuffix(normalized, "/") {
		return "", "", nil
	}
	if filepath.IsAbs(normalized) {
		return "", "", newTransferError(http.StatusBadRequest, "压缩包包含非法路径: "+memberName)
	}
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if part == ".." {
			return "", "", newTransferError(http.StatusBadRequest, "压缩包包含非法路径: "+memberName)
		}
	}
	topLevel := parts[0]
	if normalized != "manifest.json" && !transferAllowedImportParts[topLevel] {
		return "", "", newTransferError(http.StatusBadRequest, "压缩包包含不支持的顶层目录: "+topLevel)
	}
	return topLevel, strings.Join(parts[1:], "/"), nil
}

func loadArchiveManifest(reader *zip.Reader) (map[string]any, error) {
	var manifestFile *zip.File
	for _, member := range reader.File {
		if member.Name == "manifest.json" {
			manifestFile = member
			break
		}
	}
	if manifestFile == nil {
		return nil, newTransferError(http.StatusBadRequest, "压缩包缺少 manifest.json")
	}
	input, err := manifestFile.Open()
	if err != nil {
		return nil, newTransferError(http.StatusBadRequest, "manifest.json 不是合法 JSON")
	}
	defer input.Close()
	raw, err := io.ReadAll(input)
	if err != nil {
		return nil, newTransferError(http.StatusBadRequest, "manifest.json 不是合法 JSON")
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, newTransferError(http.StatusBadRequest, "manifest.json 不是合法 JSON")
	}
	if manifest["format"] != transferArchiveFormat || intValueFromAny(manifest["format_version"]) != 1 {
		return nil, newTransferError(http.StatusBadRequest, "不支持的数据包格式")
	}
	return manifest, nil
}

func validateArchiveMembers(reader *zip.Reader) error {
	for _, member := range reader.File {
		if _, _, err := safeZipMemberPath(member.Name); err != nil {
			return err
		}
		if member.Mode()&os.ModeSymlink != 0 {
			return newTransferError(http.StatusBadRequest, "压缩包不允许包含符号链接: "+member.Name)
		}
	}
	return nil
}

func resolveImportTarget(instanceDir string, topLevel string, relativePath string) (string, error) {
	root, err := filepath.Abs(filepath.Join(instanceDir, topLevel))
	if err != nil {
		return "", newTransferError(http.StatusBadRequest, "压缩包包含非法路径: "+topLevel+"/"+relativePath)
	}
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	if relative, relErr := filepath.Rel(root, target); relErr != nil ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", newTransferError(http.StatusBadRequest, "压缩包包含非法路径: "+topLevel+"/"+relativePath)
	}
	return target, nil
}

type importEntry struct {
	Member   *zip.File
	TopLevel string
	Relative string
}

func runImportJob(job *transferJob, archivePath string, enabled map[string]bool, instanceDir string) {
	release, err := reserveDataTransferImport(instanceDir)
	if err != nil {
		failImportJob(job, archivePath, err.Error())
		return
	}
	runImportJobReserved(job, archivePath, enabled, instanceDir, release)
}

// runImportJobReserved 在调用方已持有 instanceDir 导入占位时执行导入
func runImportJobReserved(job *transferJob, archivePath string, enabled map[string]bool, instanceDir string, release func()) {
	defer release()
	runImportJobBody(job, archivePath, enabled, instanceDir)
}

func failImportJob(job *transferJob, archivePath string, detail string) {
	_ = os.Remove(archivePath)
	job.stateMu.Lock()
	job.WorkerActive = false
	job.Error = detail
	job.Message = "导入失败"
	job.Status = "failed"
	job.stateMu.Unlock()
}

func runImportJobBody(job *transferJob, archivePath string, enabled map[string]bool, instanceDir string) {
	defer func() {
		_ = os.Remove(archivePath)
		job.stateMu.Lock()
		job.WorkerActive = false
		job.stateMu.Unlock()
	}()
	job.stateMu.Lock()
	job.WorkerActive = true
	job.Status = "running"
	job.Message = "正在校验压缩包"
	job.stateMu.Unlock()

	fail := func(err error) {
		detail := err.Error()
		var typed *transferError
		if errors.As(err, &typed) {
			detail = typed.Detail
		}
		job.stateMu.Lock()
		// 先写 error/message 再写 status，保证读到 failed 时错误文案已就绪
		job.Error = detail
		job.Message = "导入失败"
		job.Status = "failed"
		job.stateMu.Unlock()
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		fail(newTransferError(http.StatusBadRequest, "上传文件不是合法的 zip 压缩包"))
		return
	}
	defer reader.Close()
	manifest, err := loadArchiveManifest(&reader.Reader)
	if err != nil {
		fail(err)
		return
	}
	job.stateMu.Lock()
	job.Manifest = manifest
	job.stateMu.Unlock()
	// 数据库不再有独立快照：SQLite 库文件在 data/ 目录里，已在上面的文件循环中解压，
	// 无需 pg_restore 与待恢复标记，对齐 Python data_transfer.py 的 _run_import_job。
	if err := validateArchiveMembers(&reader.Reader); err != nil {
		fail(err)
		return
	}
	entries := make([]importEntry, 0, len(reader.File))
	for _, member := range reader.File {
		topLevel, relative, pathErr := safeZipMemberPath(member.Name)
		if pathErr != nil {
			fail(pathErr)
			return
		}
		if topLevel == "" || relative == "" || member.FileInfo().IsDir() || !enabled[topLevel] {
			continue
		}
		entries = append(entries, importEntry{Member: member, TopLevel: topLevel, Relative: relative})
	}
	totalBytes := int64(0)
	for _, entry := range entries {
		totalBytes += max(0, int64(entry.Member.UncompressedSize64))
	}
	job.stateMu.Lock()
	job.TotalFiles = len(entries)
	job.TotalBytes = totalBytes
	job.Message = "正在导入文件"
	job.stateMu.Unlock()
	buffer := make([]byte, transferChunkSize)
	for _, entry := range entries {
		targetPath, targetErr := resolveImportTarget(instanceDir, entry.TopLevel, entry.Relative)
		if targetErr != nil {
			fail(targetErr)
			return
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			fail(err)
			return
		}
		if err := extractImportMember(entry.Member, targetPath, buffer); err != nil {
			fail(err)
			return
		}
		job.stateMu.Lock()
		job.ProcessedFiles++
		job.ProcessedBytes += max(0, int64(entry.Member.UncompressedSize64))
		updateTransferProgressLocked(job)
		job.stateMu.Unlock()
	}
	// 只有当归档确实覆盖了 SQLite 库文件时才需要重启：
	// 数据库不再是独立快照，库文件是 data/ 下的普通文件，随文件循环一起解压。
	overwroteDatabase := false
	for _, entry := range entries {
		if entry.TopLevel == "data" && strings.EqualFold(filepath.Ext(entry.Relative), ".db") {
			overwroteDatabase = true
			break
		}
	}
	job.stateMu.Lock()
	job.Status = "completed"
	job.Progress = 100
	job.RestartRequired = overwroteDatabase
	if overwroteDatabase {
		job.Message = "导入完成（数据库文件已覆盖，重启后生效）"
	} else {
		job.Message = "导入完成"
	}
	job.stateMu.Unlock()
}

func extractImportMember(member *zip.File, targetPath string, buffer []byte) error {
	input, err := member.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	for {
		read, readErr := input.Read(buffer)
		if read > 0 {
			if _, writeErr := output.Write(buffer[:read]); writeErr != nil {
				output.Close()
				return writeErr
			}
		}
		if readErr == io.EOF {
			return output.Close()
		}
		if readErr != nil {
			output.Close()
			return readErr
		}
	}
}

func transferFormBool(r *http.Request, key string, fallback bool) bool {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func (d Deps) handleDataTransferImport(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	releaseImport, guardErr := reserveDataTransferImport(d.InstanceDir)
	if guardErr != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": guardErr.Error()})
		return
	}
	importReserved := true
	defer func() {
		if importReserved {
			releaseImport()
		}
	}()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "上传文件解析失败: " + err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请上传 .zip 格式的数据包"})
		return
	}
	defer file.Close()
	filename := ""
	if header != nil {
		filename = header.Filename
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".zip") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请上传 .zip 格式的数据包"})
		return
	}
	enabled := map[string]bool{
		"config":  transferFormBool(r, "import_config", true),
		"data":    transferFormBool(r, "import_data", true),
		"plugins": transferFormBool(r, "import_plugins", false),
		"logs":    transferFormBool(r, "import_logs", false),
	}
	if !enabled["config"] && !enabled["data"] && !enabled["plugins"] && !enabled["logs"] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请至少选择一个导入范围"})
		return
	}
	job, err := newTransferJob("import")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "创建导入任务失败: " + err.Error()})
		return
	}
	uploadPath := filepath.Join(transferTempDir(), job.JobID+"-upload.zip")
	output, err := os.Create(uploadPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存上传文件失败: " + err.Error()})
		return
	}
	if _, err := io.Copy(output, file); err != nil {
		output.Close()
		_ = os.Remove(uploadPath)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存上传文件失败: " + err.Error()})
		return
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(uploadPath)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存上传文件失败: " + err.Error()})
		return
	}
	importReserved = false
	go runImportJobReserved(job, uploadPath, enabled, d.InstanceDir, releaseImport)
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.JobID, "status": job.snapshot().Status})
}
