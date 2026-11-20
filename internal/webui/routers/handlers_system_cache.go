package webuiapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"MaiGo/internal/chat/image_system"
)

// 本文件对齐 ../MaiBot/src/webui/routers/system.py 的本地缓存管理端点。
// 生产库为 SQLite（data/MaiBot.db）；VACUUM / 分页统计走真实文件。

type cacheImageItem struct {
	RelativePath string  `json:"relative_path"`
	FileName     string  `json:"file_name"`
	FullPath     string  `json:"full_path"`
	Size         int64   `json:"size"`
	ModifiedTime float64 `json:"modified_time"`
	Format       string  `json:"format"`
	DBID         *int64  `json:"db_id"`
	ImageHash    *string `json:"image_hash"`
	Description  string  `json:"description"`
	IsRegistered *bool   `json:"is_registered"`
	IsBanned     *bool   `json:"is_banned"`
	NoFileFlag   *bool   `json:"no_file_flag"`
}

type cacheImageDateGroup struct {
	Date      string `json:"date"`
	FileCount int    `json:"file_count"`
	TotalSize int64  `json:"total_size"`
}

func (d Deps) systemCachePaths(w http.ResponseWriter) (systemCachePaths, bool) {
	paths, err := resolveSystemCachePaths(d.InstanceDir)
	if err != nil {
		writeInstanceDirMissing(w, "/api/webui/system/local-cache")
		return systemCachePaths{}, false
	}
	return paths, true
}

func writeSystemCacheError(w http.ResponseWriter, err error, status int) {
	var notFound e404
	if errors.As(err, &notFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": string(notFound)})
		return
	}
	writeJSON(w, status, map[string]any{"detail": err.Error()})
}

func decodeSystemJSONBody(r *http.Request, target any) error {
	body := readBody(r)
	if len(body) == 0 {
		return errors.New("请求体为空")
	}
	return sonic.Unmarshal(body, target)
}

func writeSystemBadJSON(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
}

func (d Deps) handleSystemLocalCache(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	if cached, hit := getCachedLocalCacheStats(); hit {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	writeJSON(w, http.StatusOK, storeLocalCacheStats(buildLocalCacheStatsResponse(paths)))
}

func (d Deps) handleSystemDatabaseStats(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	if cached, hit := getCachedDatabaseStats(); hit {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	writeJSON(w, http.StatusOK, storeDatabaseStats(buildDatabaseStorageOpts(paths, true)))
}

func (d Deps) handleSystemDatabaseVacuum(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	payload, err := vacuumSQLiteDatabase(paths)
	if err != nil {
		if errors.Is(err, errDatabaseMissing) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "数据库文件不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"detail": "数据库 VACUUM 失败: " + err.Error(),
		})
		return
	}
	invalidateLocalCacheStatsCache()
	writeJSON(w, http.StatusOK, payload)
}

func (d Deps) handleSystemDataEntries(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	relative := r.URL.Query().Get("relative_path")
	target, err := resolveWithin(paths.Data, relative)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "无效的 data 路径"})
		return
	}
	if !pathExists(target) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定 data 条目"})
		return
	}
	if !isDir(target) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "只能浏览 data 目录中的文件夹"})
		return
	}
	entries := []cacheDataEntry{}
	children, err := os.ReadDir(target)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "获取 data 目录条目失败: " + err.Error()})
		return
	}
	for _, child := range children {
		entries = append(entries, buildDataEntry(filepath.Join(target, child.Name()), paths))
	}
	sortDataEntries(entries)
	fileCount, totalSize := 0, int64(0)
	for _, entry := range entries {
		fileCount += entry.FileCount
		totalSize += entry.TotalSize
	}
	currentRelative := ""
	if filepath.Clean(target) != filepath.Clean(paths.Data) {
		if rel, err := filepath.Rel(paths.Data, target); err == nil {
			currentRelative = filepath.ToSlash(rel)
		}
	}
	var parentPath *string
	if filepath.Clean(target) != filepath.Clean(paths.Data) {
		parent := ""
		if filepath.Clean(filepath.Dir(target)) != filepath.Clean(paths.Data) {
			if rel, err := filepath.Rel(paths.Data, filepath.Dir(target)); err == nil {
				parent = filepath.ToSlash(rel)
			}
		}
		parentPath = &parent
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "root_path": paths.Data, "relative_path": currentRelative,
		"current_path": target, "parent_path": parentPath, "file_count": fileCount,
		"total_size": totalSize, "total": len(entries), "data": entries,
	})
}

func (d Deps) handleSystemDataEntryDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	var request struct {
		RelativePath string `json:"relative_path"`
	}
	if err := decodeSystemJSONBody(r, &request); err != nil {
		writeSystemBadJSON(w, err)
		return
	}
	target, err := resolveWithin(paths.Data, request.RelativePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "无效的 data 路径"})
		return
	}
	if !pathExists(target) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定 data 条目"})
		return
	}
	if reason := dataProtectionReason(target, paths); reason != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": reason})
		return
	}
	if isFile(target) {
		info, err := os.Stat(target)
		if err == nil {
			if err := os.Remove(target); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "删除 data 文件失败"})
				return
			}
			invalidateLocalCacheStatsCache()
			writeJSON(w, http.StatusOK, cacheCleanupResult("data", "data 文件已删除", 1, info.Size()))
			return
		}
	}
	if !isDir(target) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "只能删除 data 目录中的文件或文件夹"})
		return
	}
	removedFiles, removedBytes := removeDirectoryContents(target)
	if err := os.Remove(target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "删除 data 文件夹失败，可能仍有文件被占用"})
		return
	}
	invalidateLocalCacheStatsCache()
	writeJSON(w, http.StatusOK, cacheCleanupResult("data", "data 文件夹已删除", removedFiles, removedBytes))
}

func (d Deps) cacheImageRoot(paths systemCachePaths, target string) (string, string, bool) {
	switch target {
	case "images":
		return paths.Images, "图片", true
	case "emoji":
		return paths.Emoji, "表情包", true
	default:
		return "", "", false
	}
}

func buildCacheImageItems(root string) []cacheImageItem {
	items := []cacheImageItem{}
	for _, file := range listFiles(root) {
		if !isCacheImageFile(file) {
			continue
		}
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			continue
		}
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(file)), ".")
		if format == "" {
			format = "unknown"
		}
		items = append(items, cacheImageItem{
			RelativePath: filepath.ToSlash(relative), FileName: filepath.Base(file), FullPath: file,
			Size: info.Size(), ModifiedTime: float64(info.ModTime().UnixNano()) / 1e9, Format: format,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ModifiedTime > items[j].ModifiedTime })
	return items
}

func parseCacheDate(value, fieldName string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), time.Local)
	if err != nil {
		return nil, errors.New(fieldName + " 必须是 YYYY-MM-DD 格式")
	}
	return &parsed, nil
}

func filterCacheImageItems(items []cacheImageItem, startDate, endDate string) ([]cacheImageItem, error) {
	start, err := parseCacheDate(startDate, "start_date")
	if err != nil {
		return nil, err
	}
	end, err := parseCacheDate(endDate, "end_date")
	if err != nil {
		return nil, err
	}
	if start != nil && end != nil && start.After(*end) {
		return nil, errors.New("开始日期不能晚于结束日期")
	}
	filtered := []cacheImageItem{}
	for _, item := range items {
		itemDate := time.Unix(int64(item.ModifiedTime), 0).In(time.Local)
		itemDay := time.Date(itemDate.Year(), itemDate.Month(), itemDate.Day(), 0, 0, 0, 0, time.Local)
		if start != nil && itemDay.Before(time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)) {
			continue
		}
		if end != nil && itemDay.After(time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.Local)) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

func buildCacheImageDateGroups(items []cacheImageItem) []cacheImageDateGroup {
	groups := map[string]*cacheImageDateGroup{}
	for _, item := range items {
		date := time.Unix(int64(item.ModifiedTime), 0).In(time.Local).Format("2006-01-02")
		group, ok := groups[date]
		if !ok {
			group = &cacheImageDateGroup{Date: date}
			groups[date] = group
		}
		group.FileCount++
		group.TotalSize += item.Size
	}
	result := make([]cacheImageDateGroup, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Date > result[j].Date })
	return result
}

func (d Deps) handleSystemCacheImages(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	query := r.URL.Query()
	target := query.Get("target")
	root, _, valid := d.cacheImageRoot(paths, target)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target 必须是 images 或 emoji"})
		return
	}
	page := queryInt(r, "page", 1, 1, 1<<20)
	pageSize := queryInt(r, "page_size", 40, 1, 200)
	allItems := buildCacheImageItems(root)
	dateGroups := buildCacheImageDateGroups(allItems)
	items, err := filterCacheImageItems(allItems, query.Get("start_date"), query.Get("end_date"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	totalSize := int64(0)
	for _, item := range items {
		totalSize += item.Size
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	pageItems := items[start:end]
	d.fillCacheImageRecords(r.Context(), paths.Root, target, pageItems)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "target": target, "total": len(items), "page": page, "page_size": pageSize,
		"total_size": totalSize, "data": pageItems, "date_groups": dateGroups,
	})
}

func (d Deps) fillCacheImageRecords(ctx context.Context, root, target string, items []cacheImageItem) {
	if d.Memory == nil || len(items) == 0 {
		return
	}
	imageType := imagesystem.ImageTypeImage
	if target == "emoji" {
		imageType = imagesystem.ImageTypeEmoji
	}
	paths := make([]string, 0, len(items)*2)
	for _, item := range items {
		paths = append(paths, item.FullPath)
		if stored, err := imagesystem.SerializeStoredImagePath(root, item.FullPath); err == nil {
			paths = append(paths, stored)
		}
	}
	var records []imagesystem.ImageRecord
	if err := d.Memory.GetDB().WithContext(ctx).Where("image_type = ? AND full_path IN ?", imageType, paths).Order("id").Find(&records).Error; err != nil {
		return
	}
	byPath := make(map[string]imagesystem.ImageRecord, len(records))
	for _, record := range records {
		path, err := imagesystem.ResolveStoredImagePath(root, record.FullPath)
		if err != nil {
			continue
		}
		if _, exists := byPath[path]; !exists {
			byPath[path] = record
		}
	}
	for index := range items {
		item := &items[index]
		path, err := imagesystem.ResolveStoredImagePath(root, item.FullPath)
		if err != nil {
			continue
		}
		record, found := byPath[path]
		if found {
			id := int64(record.ID)
			item.DBID, item.ImageHash, item.Description = &id, &record.ImageHash, record.Description
			item.IsRegistered, item.IsBanned, item.NoFileFlag = &record.IsRegistered, &record.IsBanned, &record.NoFileFlag
		}
	}
}

func (d Deps) resolveCacheImageFile(w http.ResponseWriter, paths systemCachePaths, target, relative string) (string, string, bool) {
	root, label, valid := d.cacheImageRoot(paths, target)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target 必须是 images 或 emoji"})
		return "", "", false
	}
	file, err := resolveWithin(root, relative)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "无效的" + label + "路径"})
		return "", "", false
	}
	if !isFile(file) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定" + label + "文件"})
		return "", "", false
	}
	if !isCacheImageFile(file) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "只能浏览图片缓存文件"})
		return "", "", false
	}
	return file, label, true
}

func (d Deps) handleSystemCacheImagePreview(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	query := r.URL.Query()
	file, _, valid := d.resolveCacheImageFile(w, paths, query.Get("target"), query.Get("relative_path"))
	if !valid {
		return
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(file)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(file)+"\"")
	http.ServeFile(w, r, file)
}

func (d Deps) handleSystemCacheImageDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	var request struct {
		Target       string `json:"target"`
		RelativePath string `json:"relative_path"`
	}
	if err := decodeSystemJSONBody(r, &request); err != nil {
		writeSystemBadJSON(w, err)
		return
	}
	file, label, valid := d.resolveCacheImageFile(w, paths, request.Target, request.RelativePath)
	if !valid {
		return
	}
	info, err := os.Stat(file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "删除本地缓存图片失败: " + err.Error()})
		return
	}
	if err := os.Remove(file); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "删除本地缓存图片失败: " + err.Error()})
		return
	}
	invalidateLocalCacheStatsCache()
	writeJSON(w, http.StatusOK, cacheCleanupResult(request.Target, label+"缓存文件已删除", 1, info.Size()))
}

func deleteCacheImageItems(root string, items []cacheImageItem) (int, int64) {
	removedFiles, removedBytes := 0, int64(0)
	for _, item := range items {
		if err := os.Remove(item.FullPath); err != nil {
			continue
		}
		removedFiles++
		removedBytes += item.Size
	}
	return removedFiles, removedBytes
}

func (d Deps) handleSystemCacheImagesBulkDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	var request struct {
		Target         string `json:"target"`
		Mode           string `json:"mode"`
		StartDate      string `json:"start_date"`
		EndDate        string `json:"end_date"`
		KeepRecentDays *int   `json:"keep_recent_days"`
	}
	if err := decodeSystemJSONBody(r, &request); err != nil {
		writeSystemBadJSON(w, err)
		return
	}
	root, _, valid := d.cacheImageRoot(paths, request.Target)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target 必须是 images 或 emoji"})
		return
	}
	allItems := buildCacheImageItems(root)
	items := allItems
	message := ""
	switch request.Mode {
	case "date_range":
		if strings.TrimSpace(request.StartDate) == "" && strings.TrimSpace(request.EndDate) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请至少选择开始日期或结束日期"})
			return
		}
		filtered, err := filterCacheImageItems(allItems, request.StartDate, request.EndDate)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		items = filtered
		message = "指定日期区间缓存已删除"
	case "older_than_recent_days":
		if request.KeepRecentDays == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请选择要保留的最近天数"})
			return
		}
		days := *request.KeepRecentDays
		if days != 1 && days != 7 && days != 30 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "keep_recent_days 只能是 1、7 或 30"})
			return
		}
		cutoff := cacheNowSeconds() - float64(days)*24*60*60
		items = make([]cacheImageItem, 0, len(allItems))
		for _, item := range allItems {
			if item.ModifiedTime < cutoff {
				items = append(items, item)
			}
		}
		message = "最近 " + itoa(days) + " 天以外的缓存已删除"
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "mode 必须是 date_range 或 older_than_recent_days"})
		return
	}
	removedFiles, removedBytes := deleteCacheImageItems(root, items)
	invalidateLocalCacheStatsCache()
	writeJSON(w, http.StatusOK, cacheCleanupResult(request.Target, message, removedFiles, removedBytes))
}

func (d Deps) handleSystemLogDirectories(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	items := buildLogDirectories(paths.Logs)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total": len(items), "data": items})
}

func (d Deps) handleSystemLogDirectoryDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	var request struct {
		RelativePath string `json:"relative_path"`
	}
	if err := decodeSystemJSONBody(r, &request); err != nil {
		writeSystemBadJSON(w, err)
		return
	}
	logPath, err := resolveLogDirectory(paths.Logs, request.RelativePath)
	if err != nil {
		var notFound e404
		if errors.As(err, &notFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": string(notFound)})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if filepath.Clean(logPath) == filepath.Clean(paths.Logs) {
		removedFiles, removedBytes := removeDirectFiles(paths.Logs)
		invalidateLocalCacheStatsCache()
		writeJSON(w, http.StatusOK, cacheCleanupResult("log_files", "日志根目录文件已清理", removedFiles, removedBytes))
		return
	}
	removedFiles, removedBytes := removeDirectoryContents(logPath)
	invalidateLocalCacheStatsCache()
	writeJSON(w, http.StatusOK, cacheCleanupResult("log_files", "日志目录 "+request.RelativePath+" 已清理", removedFiles, removedBytes))
}

func (d Deps) handleSystemCacheCleanup(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	paths, ok := d.systemCachePaths(w)
	if !ok {
		return
	}
	var request struct {
		Target             string   `json:"target"`
		Tables             []string `json:"tables"`
		DatabaseMode       string   `json:"database_mode"`
		OlderThanDays      *int     `json:"older_than_days"`
		VacuumAfterCleanup *bool    `json:"vacuum_after_cleanup"`
	}
	if err := decodeSystemJSONBody(r, &request); err != nil {
		writeSystemBadJSON(w, err)
		return
	}
	switch request.Target {
	case "images":
		removedFiles, removedBytes := removeDirectoryContents(paths.Images)
		invalidateLocalCacheStatsCache()
		writeJSON(w, http.StatusOK, cacheCleanupResult("images", "图片缓存已清理", removedFiles, removedBytes))
	case "emoji":
		emojiFiles, emojiBytes := removeDirectoryContents(paths.Emoji)
		thumbnailFiles, thumbnailBytes := removeDirectoryContents(paths.Thumbnails)
		invalidateLocalCacheStatsCache()
		writeJSON(w, http.StatusOK, cacheCleanupResult("emoji", "表情包缓存已清理", emojiFiles+thumbnailFiles, emojiBytes+thumbnailBytes))
	case "log_files":
		removedFiles, removedBytes := removeDirectoryContents(paths.Logs)
		invalidateLocalCacheStatsCache()
		writeJSON(w, http.StatusOK, cacheCleanupResult("log_files", "日志文件已清理", removedFiles, removedBytes))
	case "database_logs":
		d.handleSystemDatabaseLogsCleanup(w, paths, request.Tables, request.DatabaseMode, request.OlderThanDays, request.VacuumAfterCleanup)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target 必须是 images、emoji、log_files 或 database_logs"})
	}
}

// handleSystemDatabaseLogsCleanup 对齐 Python _cleanup_local_cache_response 的 database_logs 分支
func (d Deps) handleSystemDatabaseLogsCleanup(
	w http.ResponseWriter,
	paths systemCachePaths,
	tables []string,
	mode string,
	olderThanDays *int,
	vacuumAfter *bool,
) {
	removedRecords, err := deleteDatabaseRecords(paths.Database, tables, mode, olderThanDays)
	if err != nil {
		if errors.Is(err, errDatabaseCleanupEmptyTables) || errors.Is(err, errDatabaseCleanupNeedDays) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"detail": "清理本地缓存失败: " + err.Error(),
		})
		return
	}

	doVacuum := true
	if vacuumAfter != nil {
		doVacuum = *vacuumAfter
	}
	var (
		vacuumed   bool
		sizeBefore any
		sizeAfter  any
		reclaimed  int64
	)
	if doVacuum && removedRecords > 0 {
		payload, vacuumErr := vacuumSQLiteDatabase(paths)
		if vacuumErr != nil {
			if errors.Is(vacuumErr, errDatabaseMissing) {
				writeJSON(w, http.StatusNotFound, map[string]any{"detail": "数据库文件不存在"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"detail": "数据库 VACUUM 失败: " + vacuumErr.Error(),
			})
			return
		}
		vacuumed = true
		sizeBefore = payload["database_size_before"]
		sizeAfter = payload["database_size_after"]
		if v, ok := payload["reclaimed_bytes"].(int64); ok {
			reclaimed = v
		}
	}

	invalidateLocalCacheStatsCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":              true,
		"message":              "数据库记录已清理",
		"target":               "database_logs",
		"removed_files":        0,
		"removed_bytes":        0,
		"removed_records":      removedRecords,
		"vacuumed":             vacuumed,
		"database_size_before": sizeBefore,
		"database_size_after":  sizeAfter,
		"reclaimed_bytes":      reclaimed,
	})
}
