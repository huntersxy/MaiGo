package host

// 宿主停机窗口内的导出快照和可恢复导入；所有目标必须属于指定 Bot。
// 对齐 src/host/transfer_storage.py。

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
)

var allowedImportParts = map[string]struct{}{"config": {}, "data": {}, "plugins": {}, "logs": {}}

type exportFile struct {
	path        string
	archiveName string
	size        int64
}

func portableConfig(bot BotRecord, host string, staging string) ([]exportFile, map[string]bool, error) {
	files := make([]exportFile, 0, 2)
	excluded := map[string]bool{}
	modelPath := transferTarget(bot, host, "config/model_config.toml")
	if info, err := os.Stat(modelPath); err == nil && !info.IsDir() {
		lock, err := lockModelConfig(host)
		if err != nil {
			return nil, nil, err
		}
		local, err := readTOMLMap(modelPath)
		if err != nil {
			lock.Release()
			return nil, nil, err
		}
		catalog, err := readCatalogDocument(filepath.Join(host, maibotcfg.CatalogFilename))
		if err != nil {
			lock.Release()
			return nil, nil, err
		}
		merged := maibotcfg.MergeCatalogWith(local, catalog)
		lock.Release()
		names := map[string]struct{}{}
		tasks, _ := merged["model_task_config"].(map[string]any)
		for taskName, raw := range tasks {
			if taskName == "web_search" {
				continue
			}
			if task, ok := raw.(map[string]any); ok {
				collectModelNames(task, names)
			}
		}
		models := make([]any, 0)
		providers := map[string]struct{}{}
		if items, ok := merged["models"].([]any); ok {
			for _, raw := range items {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if _, selected := names[hostString(entry["name"])]; !selected {
					continue
				}
				models = append(models, entry)
				providers[hostString(entry["api_provider"])] = struct{}{}
			}
		}
		merged["models"] = models
		providerItems := make([]any, 0)
		if items, ok := merged["api_providers"].([]any); ok {
			for _, raw := range items {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if _, selected := providers[hostString(entry["name"])]; selected {
					providerItems = append(providerItems, entry)
				}
			}
		}
		merged["api_providers"] = providerItems
		selectedServices := map[string]struct{}{}
		if webSearch, ok := tasks["web_search"].(map[string]any); ok {
			if list, ok := webSearch["model_list"].([]any); ok {
				for _, name := range list {
					selectedServices[hostString(name)] = struct{}{}
				}
			}
		}
		if web, ok := merged["web"].(map[string]any); ok {
			if services, ok := web["services"].(map[string]any); ok {
				filtered := map[string]any{}
				for name, service := range services {
					if _, selected := selectedServices[name]; selected {
						filtered[name] = service
					}
				}
				web["services"] = filtered
			}
		}
		target := filepath.Join(staging, "model_config.toml")
		encoded, err := encodeTOMLDocument(merged)
		if err != nil {
			return nil, nil, err
		}
		if err := WritePrivate(target, encoded); err != nil {
			return nil, nil, err
		}
		files = append(files, exportFile{path: target, archiveName: "config/model_config.toml", size: int64(len(encoded))})
		excluded[resolvedPath(modelPath)] = true
	}
	botPath := transferTarget(bot, host, "config/bot_config.toml")
	if info, err := os.Stat(botPath); err == nil && !info.IsDir() {
		document, err := readTOMLMap(botPath)
		if err != nil {
			return nil, nil, err
		}
		if err := mergeHostRetention(document, filepath.Join(host, "config.toml")); err != nil {
			return nil, nil, err
		}
		encoded, err := encodeTOMLDocument(document)
		if err != nil {
			return nil, nil, err
		}
		target := filepath.Join(staging, "bot_config.toml")
		if err := WritePrivate(target, encoded); err != nil {
			return nil, nil, err
		}
		files = append(files, exportFile{path: target, archiveName: "config/bot_config.toml", size: int64(len(encoded))})
		excluded[resolvedPath(botPath)] = true
	}
	excluded[resolvedPath(filepath.Join(bot.Directory, "config", "old"))] = true
	return files, excluded, nil
}

func transferTarget(bot BotRecord, host string, name string) string {
	if name == "@catalog" {
		return filepath.Join(host, maibotcfg.CatalogFilename)
	}
	relative := filepath.Clean(name)
	if filepath.IsAbs(relative) {
		return ""
	}
	rawParts := strings.Split(filepath.ToSlash(name), "/")
	for _, part := range rawParts {
		if part == ".." {
			return ""
		}
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 {
		return ""
	}
	if _, allowed := allowedImportParts[parts[0]]; !allowed {
		return ""
	}
	for _, part := range parts {
		if part == ".." {
			return ""
		}
	}
	target := filepath.Join(bot.Directory, relative)
	if !pathContains(bot.Directory, target) {
		return ""
	}
	current := target
	for current != filepath.Dir(bot.Directory) {
		if isDirectoryLink(current) {
			return ""
		}
		if current == bot.Directory {
			break
		}
		current = filepath.Dir(current)
	}
	return target
}

func validateTransferTarget(bot BotRecord, host, name string) (string, error) {
	target := transferTarget(bot, host, name)
	if target == "" {
		return "", errors.New("导入目标不属于当前 Bot")
	}
	return target, nil
}

func resolvedPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if absolute, absErr := filepath.Abs(path); absErr == nil {
			return absolute
		}
		return path
	}
	return resolved
}

func collectModelNames(node map[string]any, names map[string]struct{}) {
	for key, value := range node {
		if key == "model_list" {
			if list, ok := value.([]any); ok {
				for _, name := range list {
					names[hostString(name)] = struct{}{}
				}
			}
			continue
		}
		if child, ok := value.(map[string]any); ok {
			collectModelNames(child, names)
		}
	}
}

func lockModelConfig(host string) (*InstanceLease, error) {
	lockPath := filepath.Join(host, "model-config.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	if err := lockInstanceHandle(file); err != nil {
		_ = file.Close()
		return nil, ErrInstanceBusy
	}
	return &InstanceLease{file: file}, nil
}

func mergeHostRetention(document map[string]any, configPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var shared map[string]any
	if err := toml.Unmarshal(raw, &shared); err != nil {
		return err
	}
	retention, _ := shared["retention"].(map[string]any)
	if retention == nil {
		retention = maibotcfg.SharedRetentionDefaults()
	}
	if images, ok := retention["images"].(map[string]any); ok {
		visual, _ := document["visual"].(map[string]any)
		if visual == nil {
			visual = map[string]any{}
			document["visual"] = visual
		}
		visual["image_cache_cleanup"] = cloneAnyMap(images)
	}
	if emoji, ok := retention["emoji"].(map[string]any); ok {
		section, _ := document["emoji"].(map[string]any)
		if section == nil {
			section = map[string]any{}
			document["emoji"] = section
		}
		section["cache_cleanup"] = cloneAnyMap(emoji)
	}
	if logs, ok := retention["logs"].(map[string]any); ok {
		section, _ := document["log"].(map[string]any)
		if section == nil {
			section = map[string]any{}
			document["log"] = section
		}
		for key, value := range logs {
			section[key] = value
		}
	}
	return nil
}

func stripRetention(document map[string]any) {
	if visual, ok := document["visual"].(map[string]any); ok {
		delete(visual, "image_cache_cleanup")
	}
	if emoji, ok := document["emoji"].(map[string]any); ok {
		delete(emoji, "cache_cleanup")
	}
	if logs, ok := document["log"].(map[string]any); ok {
		for _, key := range []string{"log_file_max_bytes", "max_log_files", "log_cleanup_days"} {
			delete(logs, key)
		}
	}
}

func cloneAnyMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

type importJournalFile struct {
	Name    string `json:"name"`
	Existed bool   `json:"existed"`
	Delete  bool   `json:"delete"`
}

type importJournal struct {
	ID    string              `json:"id"`
	Files []importJournalFile `json:"files"`
}

func importJournalPath(bot BotRecord) string {
	return filepath.Join(bot.Directory, "temp", "data-import", "transaction.json")
}

// recoverImport 按日志把被替换的文件恢复原状
func recoverImport(bot BotRecord, host string) error {
	journalPath := importJournalPath(bot)
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var journal importJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return err
	}
	work := filepath.Join(filepath.Dir(journalPath), journal.ID)
	if !pathContains(filepath.Dir(journalPath), work) {
		return errors.New("无效的导入恢复目录")
	}
	for index := len(journal.Files) - 1; index >= 0; index-- {
		entry := journal.Files[index]
		target, err := validateTransferTarget(bot, host, entry.Name)
		if err != nil {
			return err
		}
		backup := filepath.Join(work, "backup", fmt.Sprint(index))
		staged := filepath.Join(work, "prepared", fmt.Sprint(index))
		if entry.Existed {
			if _, err := os.Stat(backup); err == nil {
				if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
					return err
				}
				if err := os.Rename(backup, target); err != nil {
					return err
				}
			}
		} else if _, err := os.Stat(staged); os.IsNotExist(err) {
			_ = os.Remove(target)
		}
	}
	return os.Remove(journalPath)
}

// importArchive 在实例锁内完成校验、公共目录合并与事务提交
func importArchive(bot BotRecord, host string, service *TransferService, archivePath string, parts map[string]struct{}, job *transferJob) error {
	base := filepath.Join(bot.Directory, "temp", "data-import")
	work := filepath.Join(base, newJobID())
	incoming := filepath.Join(work, "incoming")
	if err := os.MkdirAll(incoming, 0o777); err != nil {
		return err
	}
	modelLock, err := lockModelConfig(host)
	if err != nil {
		return err
	}
	defer modelLock.Release()
	if err := recoverImport(bot, host); err != nil {
		return err
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return errors.New("上传文件不是合法的 zip 压缩包")
	}
	defer archive.Close()
	manifest, err := service.loadArchiveManifest(&archive.Reader)
	if err != nil {
		return err
	}
	if err := service.validateArchiveMembers(&archive.Reader); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, member := range archive.File {
		top, relative, err := safeZipMemberPath(member.Name)
		if err != nil {
			return err
		}
		if _, selected := parts[top]; !selected || relative == "" || strings.HasSuffix(member.Name, "/") {
			continue
		}
		name := top + "/" + relative
		normalized := strings.ToLower(filepath.ToSlash(filepath.Clean(name)))
		if _, duplicate := seen[normalized]; duplicate {
			return fmt.Errorf("导入包存在重复路径: %s", name)
		}
		seen[normalized] = struct{}{}
		if strings.HasPrefix(name, "config/old/") || strings.HasSuffix(name, ".lock") {
			continue
		}
		if _, err := validateTransferTarget(bot, host, name); err != nil {
			return err
		}
		target := filepath.Join(incoming, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return err
		}
		source, err := member.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
		if err != nil {
			_ = source.Close()
			return err
		}
		_, copyErr := io.Copy(output, source)
		_ = source.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	botConfig := filepath.Join(incoming, "config", "bot_config.toml")
	if _, err := os.Stat(botConfig); err == nil {
		document, err := readTOMLMap(botConfig)
		if err != nil {
			return err
		}
		stripRetention(document)
		stripHostSettings(document)
		encoded, err := encodeTOMLDocument(document)
		if err != nil {
			return err
		}
		if err := WritePrivate(botConfig, encoded); err != nil {
			return err
		}
	}
	catalog := filepath.Join(host, maibotcfg.CatalogFilename)
	var stagedCatalog string
	if _, err := os.Stat(filepath.Join(incoming, "config", "model_config.toml")); err == nil {
		isolated := filepath.Join(work, "catalog-merge", "host")
		if err := os.MkdirAll(isolated, 0o777); err != nil {
			return err
		}
		stagedCatalog = filepath.Join(isolated, maibotcfg.CatalogFilename)
		raw, err := os.ReadFile(catalog)
		if err != nil {
			return err
		}
		if err := os.WriteFile(stagedCatalog, raw, 0o600); err != nil {
			return err
		}
		var shared map[string]any
		if err := toml.Unmarshal(raw, &shared); err != nil {
			return err
		}
		localPath := filepath.Join(bot.Directory, "config", "model_config.toml")
		local, err := readTOMLMap(filepath.Join(incoming, "config", "model_config.toml"))
		if err != nil {
			return err
		}
		if err := planModelCatalog(shared, []BotRecord{bot}, map[string]map[string]any{localPath: local}); err != nil {
			return fmt.Errorf("导入模型配置无效: %w", err)
		}
		encodedCatalog, err := encodeTOMLDocument(shared)
		if err != nil {
			return err
		}
		if err := WritePrivate(stagedCatalog, encodedCatalog); err != nil {
			return err
		}
		encodedLocal, err := encodeTOMLDocument(local)
		if err != nil {
			return err
		}
		if err := WritePrivate(filepath.Join(incoming, "config", "model_config.toml"), encodedLocal); err != nil {
			return err
		}
	}
	entries := make([]exportFile, 0, 32)
	incomingNames := map[string]struct{}{}
	_ = filepath.Walk(incoming, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(incoming, path)
		if err != nil {
			return nil
		}
		name := filepath.ToSlash(relative)
		if strings.HasPrefix(name, "config/old/") {
			return nil
		}
		entries = append(entries, exportFile{path: path, archiveName: name, size: info.Size()})
		incomingNames[name] = struct{}{}
		return nil
	})
	records := make([]importJournalFile, 0, len(entries)+8)
	for _, entry := range entries {
		target, err := validateTransferTarget(bot, host, entry.archiveName)
		if err != nil {
			return err
		}
		if info, err := os.Stat(target); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("导入目标不是普通文件: %s", entry.archiveName)
		}
		if isSQLiteFile(entry.path) {
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				sidecar := entry.archiveName + suffix
				if _, included := incomingNames[sidecar]; included {
					continue
				}
				sidecarTarget, err := validateTransferTarget(bot, host, sidecar)
				if err != nil {
					continue
				}
				if _, err := os.Stat(sidecarTarget); err == nil {
					entries = append(entries, exportFile{path: "", archiveName: sidecar, size: 0})
				}
			}
		}
	}
	if stagedCatalog != "" {
		original, err := os.ReadFile(catalog)
		if err != nil {
			return err
		}
		staged, err := os.ReadFile(stagedCatalog)
		if err != nil {
			return err
		}
		if string(original) != string(staged) {
			entries = append(entries, exportFile{path: stagedCatalog, archiveName: "@catalog", size: int64(len(staged))})
		}
	}
	if err := os.MkdirAll(filepath.Join(work, "prepared"), 0o777); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(work, "backup"), 0o777); err != nil {
		return err
	}
	for index, entry := range entries {
		target, err := validateTransferTarget(bot, host, entry.archiveName)
		if err != nil {
			return err
		}
		existed := false
		if _, err := os.Stat(target); err == nil {
			existed = true
			if err := copyTransferFile(target, filepath.Join(work, "backup", fmt.Sprint(index))); err != nil {
				return err
			}
		}
		if entry.path != "" {
			if err := copyTransferFile(entry.path, filepath.Join(work, "prepared", fmt.Sprint(index))); err != nil {
				return err
			}
		}
		records = append(records, importJournalFile{Name: entry.archiveName, Existed: existed, Delete: entry.path == ""})
	}
	journalPath := filepath.Join(base, "transaction.json")
	journal := importJournal{ID: filepath.Base(work), Files: records}
	encodedJournal, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := WritePrivate(journalPath, string(encodedJournal)); err != nil {
		return err
	}
	job.TotalFiles = len(records)
	job.Manifest = manifest
	for index, record := range records {
		target, err := validateTransferTarget(bot, host, record.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return err
		}
		if record.Delete {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if err := os.Rename(filepath.Join(work, "prepared", fmt.Sprint(index)), target); err != nil {
			_ = recoverImport(bot, host)
			return err
		}
		job.ProcessedFiles = index + 1
		job.Progress = 100 * job.ProcessedFiles / max(1, job.TotalFiles)
	}
	manifestBytes, _ := json.Marshal(records)
	_ = WritePrivate(filepath.Join(work, "backup-manifest.json"), string(manifestBytes))
	return os.Remove(journalPath)
}

func copyTransferFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func isSQLiteFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, 16)
	if _, err := io.ReadFull(file, header); err != nil {
		return false
	}
	return string(header) == "SQLite format 3\x00"
}

func safeZipMemberPath(memberName string) (string, string, error) {
	normalized := strings.ReplaceAll(memberName, "\\", "/")
	if strings.HasPrefix(normalized, "/") || strings.Contains(normalized, ":") {
		return "", "", fmt.Errorf("压缩包包含非法路径: %s", memberName)
	}
	if normalized == "" || strings.HasSuffix(normalized, "/") {
		return "", "", nil
	}
	cleaned := filepath.ToSlash(filepath.Clean(normalized))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", "", fmt.Errorf("压缩包包含非法路径: %s", memberName)
	}
	parts := strings.Split(cleaned, "/")
	top := parts[0]
	if normalized != "manifest.json" {
		if _, allowed := allowedImportParts[top]; !allowed {
			return "", "", fmt.Errorf("压缩包包含不支持的顶层目录: %s", top)
		}
	}
	return top, strings.Join(parts[1:], "/"), nil
}

func sortedExportParts(parts map[string]struct{}) []string {
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func transferTimestamp() string {
	return time.Now().Format("20060102-150405")
}
