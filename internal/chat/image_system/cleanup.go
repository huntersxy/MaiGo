package imagesystem

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// CleanupBatchSize 单次清理每类最多处理的记录/文件数
	CleanupBatchSize = 1000
	// CleanupDisabledPoll 清理关闭时的轮询间隔
	CleanupDisabledPoll = 5 * time.Minute
)

var cacheImageExtensions = map[string]struct{}{
	".avif": {},
	".bmp":  {},
	".gif":  {},
	".jpeg": {},
	".jpg":  {},
	".png":  {},
	".webp": {},
}

// ImageCacheCleanupResult 图片缓存清理统计，字段与 Python 一致
type ImageCacheCleanupResult struct {
	RemovedFiles             int   `json:"removed_files"`
	RemovedBytes             int64 `json:"removed_bytes"`
	MarkedNoFileRecords      int   `json:"marked_no_file_records"`
	RemovedOrphanFiles       int   `json:"removed_orphan_files"`
	RemovedOrphanBytes       int64 `json:"removed_orphan_bytes"`
	RemovedRecords           int   `json:"removed_records"`
	RestoredFileFlags        int   `json:"restored_file_flags"`
	SkippedOutsideCacheFiles int   `json:"skipped_outside_cache_files"`
}

// Changed 是否有实际清理动作，跳过缓存目录外的统计不算
func (r ImageCacheCleanupResult) Changed() bool {
	return r.RemovedFiles > 0 ||
		r.RemovedBytes > 0 ||
		r.MarkedNoFileRecords > 0 ||
		r.RemovedOrphanFiles > 0 ||
		r.RemovedOrphanBytes > 0 ||
		r.RemovedRecords > 0 ||
		r.RestoredFileFlags > 0
}

// ImageCacheCleanupRunner 图片缓存清理执行器
type ImageCacheCleanupRunner struct {
	Store Store
	Root  string
	Dir   string
	Now   func() time.Time
}

// NewImageCacheCleanupRunner 创建清理执行器，Dir 缺省为 <Root>/data/images
func NewImageCacheCleanupRunner(store Store, root string) *ImageCacheCleanupRunner {
	if strings.TrimSpace(root) == "" {
		root = instanceRoot()
	}
	return &ImageCacheCleanupRunner{
		Store: store,
		Root:  root,
		Dir:   filepath.Join(root, "data", "images"),
		Now:   time.Now,
	}
}

// Run 执行一次图片缓存清理
func (r *ImageCacheCleanupRunner) Run(ctx context.Context, config ImageCacheCleanupSettings) (ImageCacheCleanupResult, error) {
	if r == nil || r.Store == nil {
		return ImageCacheCleanupResult{}, fmt.Errorf("图片记录存储未配置")
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	fileRetentionDays := max(1, config.ImageFileRetentionDays)
	resultRetentionDays := max(1, config.NoFileResultRetentionDays)
	fileCutoff := now.Add(-time.Duration(fileRetentionDays) * 24 * time.Hour)
	recordCutoff := now.Add(-time.Duration(resultRetentionDays) * 24 * time.Hour)

	result, err := r.cleanupTrackedImageFiles(ctx, now, fileCutoff)
	if err != nil {
		return result, err
	}
	orphan, err := r.cleanupOrphanImageFiles(ctx, fileCutoff)
	if err != nil {
		return result, err
	}
	mergeImageCleanupResult(&result, orphan)
	noFile, err := r.cleanupNoFileResults(ctx, now, recordCutoff)
	if err != nil {
		return result, err
	}
	mergeImageCleanupResult(&result, noFile)

	if result.Changed() || result.SkippedOutsideCacheFiles > 0 {
		zap.L().Info(fmt.Sprintf(
			"图片缓存自动清理完成：删除文件 %d 个/%d 字节，删除孤立文件 %d 个/%d 字节，标记无文件记录 %d 条，删除识别结果 %d 条，恢复文件状态 %d 条，跳过缓存目录外文件 %d 个",
			result.RemovedFiles, result.RemovedBytes,
			result.RemovedOrphanFiles, result.RemovedOrphanBytes,
			result.MarkedNoFileRecords,
			result.RemovedRecords,
			result.RestoredFileFlags,
			result.SkippedOutsideCacheFiles,
		))
	}
	return result, nil
}

func (r *ImageCacheCleanupRunner) cleanupTrackedImageFiles(ctx context.Context, now, cutoff time.Time) (ImageCacheCleanupResult, error) {
	result := ImageCacheCleanupResult{}
	processed := 0
	records, err := r.Store.List(ctx, ImageTypeImage)
	if err != nil {
		return result, err
	}
	for index := range records {
		if processed >= CleanupBatchSize {
			break
		}
		record := records[index]
		cachePath := r.resolveCacheImagePath(record.FullPath)
		fileExists := r.recordFileExists(record)
		if record.NoFileFlag {
			if fileExists {
				record.NoFileFlag = false
				if err := r.Store.Save(ctx, &record); err != nil {
					return result, err
				}
				result.RestoredFileFlags++
				processed++
			}
			continue
		}
		if !fileExists {
			record.NoFileFlag = true
			if err := r.Store.Save(ctx, &record); err != nil {
				return result, err
			}
			result.MarkedNoFileRecords++
			processed++
			continue
		}
		if imageRecordReferenceTime(record, now).After(cutoff) {
			continue
		}
		if cachePath == "" {
			result.SkippedOutsideCacheFiles++
			zap.L().Debug("跳过不在图片缓存目录内的图片文件清理", zap.String("path", record.FullPath))
			continue
		}
		info, statErr := os.Stat(cachePath)
		if statErr != nil {
			zap.L().Warn("清理图片缓存文件失败", zap.String("path", cachePath), zap.Error(statErr))
			continue
		}
		if removeErr := os.Remove(cachePath); removeErr != nil {
			zap.L().Warn("清理图片缓存文件失败", zap.String("path", cachePath), zap.Error(removeErr))
			continue
		}
		record.NoFileFlag = true
		if err := r.Store.Save(ctx, &record); err != nil {
			return result, err
		}
		result.RemovedFiles++
		result.RemovedBytes += info.Size()
		result.MarkedNoFileRecords++
		processed++
	}
	return result, nil
}

func (r *ImageCacheCleanupRunner) cleanupOrphanImageFiles(ctx context.Context, cutoff time.Time) (ImageCacheCleanupResult, error) {
	result := ImageCacheCleanupResult{}
	dir := strings.TrimSpace(r.Dir)
	if dir == "" {
		dir = filepath.Join(r.Root, "data", "images")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return result, nil
	}
	tracked, err := r.trackedCachePaths(ctx)
	if err != nil {
		return result, err
	}
	// 对齐 Python cutoff_time.timestamp() 浮点秒比较，避免 Unix() 截断导致同秒后半段误删
	cutoffTimestamp := float64(cutoff.UnixNano()) / 1e9
	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			zap.L().Warn("读取孤立图片缓存文件失败", zap.String("path", path), zap.Error(walkErr))
			return nil
		}
		if result.RemovedOrphanFiles >= CleanupBatchSize {
			return fs.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if _, ok := cacheImageExtensions[extension]; !ok {
			return nil
		}
		resolved, resolveErr := filepath.Abs(path)
		if resolveErr == nil {
			if evaluated, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
				resolved = evaluated
			}
		}
		if resolveErr != nil {
			zap.L().Warn("读取孤立图片缓存文件失败", zap.String("path", path), zap.Error(resolveErr))
			return nil
		}
		if !pathWithin(dir, resolved) {
			return nil
		}
		fileInfo, statErr := os.Stat(path)
		if statErr != nil {
			zap.L().Warn("读取孤立图片缓存文件失败", zap.String("path", path), zap.Error(statErr))
			return nil
		}
		fileMtime := float64(fileInfo.ModTime().UnixNano()) / 1e9
		if _, ok := tracked[resolved]; ok || fileMtime > cutoffTimestamp {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil {
			zap.L().Warn("清理孤立图片缓存文件失败", zap.String("path", path), zap.Error(removeErr))
			return nil
		}
		result.RemovedOrphanFiles++
		result.RemovedOrphanBytes += fileInfo.Size()
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		return result, walkErr
	}
	return result, nil
}

func (r *ImageCacheCleanupRunner) cleanupNoFileResults(ctx context.Context, now, cutoff time.Time) (ImageCacheCleanupResult, error) {
	result := ImageCacheCleanupResult{}
	processed := 0
	records, err := r.Store.List(ctx, ImageTypeImage)
	if err != nil {
		return result, err
	}
	for index := range records {
		if processed >= CleanupBatchSize {
			break
		}
		record := records[index]
		if r.recordFileExists(record) {
			if record.NoFileFlag {
				record.NoFileFlag = false
				if err := r.Store.Save(ctx, &record); err != nil {
					return result, err
				}
				result.RestoredFileFlags++
				processed++
			}
			continue
		}
		if !record.NoFileFlag {
			record.NoFileFlag = true
			if err := r.Store.Save(ctx, &record); err != nil {
				return result, err
			}
			result.MarkedNoFileRecords++
			processed++
			continue
		}
		if imageRecordReferenceTime(record, now).After(cutoff) {
			continue
		}
		if err := r.Store.Delete(ctx, record.ID); err != nil {
			return result, err
		}
		result.RemovedRecords++
		processed++
	}
	return result, nil
}

func (r *ImageCacheCleanupRunner) trackedCachePaths(ctx context.Context) (map[string]struct{}, error) {
	paths := map[string]struct{}{}
	rawPaths, err := r.Store.ListPaths(ctx, ImageTypeImage)
	if err != nil {
		return nil, err
	}
	for _, raw := range rawPaths {
		cachePath := r.resolveCacheImagePath(raw)
		if cachePath != "" {
			paths[cachePath] = struct{}{}
		}
	}
	return paths, nil
}

func (r *ImageCacheCleanupRunner) recordFileExists(record ImageRecord) bool {
	path, err := ResolveStoredImagePath(r.Root, record.FullPath)
	if err != nil {
		return false
	}
	return fileExists(path)
}

func (r *ImageCacheCleanupRunner) resolveCacheImagePath(storedPath string) string {
	path, err := ResolveStoredImagePath(r.Root, storedPath)
	if err != nil {
		return ""
	}
	extension := strings.ToLower(filepath.Ext(path))
	if _, ok := cacheImageExtensions[extension]; !ok {
		return ""
	}
	dir := strings.TrimSpace(r.Dir)
	if dir == "" {
		dir = filepath.Join(r.Root, "data", "images")
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	dirAbs = filepath.Clean(dirAbs)
	if resolved, err := filepath.EvalSymlinks(dirAbs); err == nil {
		dirAbs = resolved
	}
	if !pathWithin(dirAbs, path) {
		return ""
	}
	return path
}

func imageRecordReferenceTime(record ImageRecord, fallback time.Time) time.Time {
	if record.LastUsedTime != nil {
		return *record.LastUsedTime
	}
	if !record.RecordTime.IsZero() {
		return record.RecordTime
	}
	return fallback
}

func mergeImageCleanupResult(target *ImageCacheCleanupResult, source ImageCacheCleanupResult) {
	target.RemovedFiles += source.RemovedFiles
	target.RemovedBytes += source.RemovedBytes
	target.MarkedNoFileRecords += source.MarkedNoFileRecords
	target.RemovedOrphanFiles += source.RemovedOrphanFiles
	target.RemovedOrphanBytes += source.RemovedOrphanBytes
	target.RemovedRecords += source.RemovedRecords
	target.RestoredFileFlags += source.RestoredFileFlags
	target.SkippedOutsideCacheFiles += source.SkippedOutsideCacheFiles
}

// RunImageCacheCleanup 使用默认目录执行一次清理
func RunImageCacheCleanup(ctx context.Context, store Store, root string, config ImageCacheCleanupSettings) (ImageCacheCleanupResult, error) {
	return NewImageCacheCleanupRunner(store, root).Run(ctx, config)
}

// RunPeriodicImageCacheCleanup 按配置周期执行清理，配置每轮重新读取
func (r *ImageCacheCleanupRunner) RunPeriodicImageCacheCleanup(ctx context.Context, settings func() ImageCacheCleanupSettings) {
	if settings == nil {
		settings = func() ImageCacheCleanupSettings {
			return DefaultImageSettings(r.Root).CacheCleanup
		}
	}
	for {
		config := settings()
		if config.Enabled {
			if _, err := r.Run(ctx, config); err != nil {
				zap.L().Error("图片缓存自动清理失败", zap.Error(err))
			}
		}
		wait := CleanupDisabledPoll
		if config.Enabled {
			wait = config.CleanupInterval()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
