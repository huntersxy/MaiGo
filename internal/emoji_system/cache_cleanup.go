package emojisystem

// 表情包缓存自动清理，对齐 src/emoji_system/emoji_cache_cleanup.py

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	imagesystem "maibot-go/internal/chat/image_system"
	"maibot-go/internal/common/database"

	"go.uber.org/zap"
)

const (
	emojiCleanupBatchSize    = 1000
	emojiCleanupDisabledPoll = 5 * time.Minute
)

var emojiCacheExtensions = map[string]struct{}{
	".avif": {}, ".bmp": {}, ".gif": {}, ".jpeg": {}, ".jpg": {},
	".mpo": {}, ".png": {}, ".webp": {},
}

// EmojiCacheCleanupResult 一次缓存清理的统计，字段对齐 Python 同名 dataclass
type EmojiCacheCleanupResult struct {
	RemovedFiles           int
	RemovedBytes           int64
	MarkedNoFileRecords    int
	RemovedOrphanFiles     int
	RemovedOrphanBytes     int64
	RemovedRecords         int
	RestoredFileFlags      int
	SkippedRegisteredFiles int
}

// Changed 是否有任何清理动作
func (r EmojiCacheCleanupResult) Changed() bool {
	return r.RemovedFiles > 0 || r.RemovedBytes > 0 || r.MarkedNoFileRecords > 0 ||
		r.RemovedOrphanFiles > 0 || r.RemovedOrphanBytes > 0 || r.RemovedRecords > 0 ||
		r.RestoredFileFlags > 0
}

// RunEmojiCacheCleanup 执行一次表情包缓存清理，始终保护已注册表情
func (r *Registry) RunEmojiCacheCleanup(ctx context.Context) (EmojiCacheCleanupResult, error) {
	if r == nil {
		return EmojiCacheCleanupResult{}, errMemoryUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	mem := r.mem
	dir := r.stickerDir
	r.mu.RUnlock()
	if mem == nil || strings.TrimSpace(dir) == "" {
		return EmojiCacheCleanupResult{}, errMemoryUnavailable
	}
	settings := r.Settings().CacheCleanup
	now := time.Now()
	fileCutoff := now.Add(-time.Duration(max(1, settings.EmojiFileRetentionDays)) * 24 * time.Hour)
	recordCutoff := now.Add(-time.Duration(max(1, settings.NoFileRecordRetentionDays)) * 24 * time.Hour)

	rows, err := mem.ListEmojiRecords(ctx)
	if err != nil {
		return EmojiCacheCleanupResult{}, err
	}
	result, err := r.cleanupUnregisteredEmojiFiles(ctx, mem, rows, fileCutoff, now)
	if err != nil {
		return result, err
	}
	orphan, err := cleanupOrphanEmojiFiles(ctx, mem, rows, fileCutoff)
	if err != nil {
		return result, err
	}
	mergeEmojiCleanupResult(&result, orphan)
	noFile, err := cleanupUnregisteredNoFileRecords(ctx, mem, rows, recordCutoff, now)
	if err != nil {
		return result, err
	}
	mergeEmojiCleanupResult(&result, noFile)

	if result.Changed() {
		zap.L().Info("表情包缓存自动清理完成",
			zap.Int("removed_files", result.RemovedFiles),
			zap.Int64("removed_bytes", result.RemovedBytes),
			zap.Int("marked_no_file_records", result.MarkedNoFileRecords),
			zap.Int("removed_orphan_files", result.RemovedOrphanFiles),
			zap.Int64("removed_orphan_bytes", result.RemovedOrphanBytes),
			zap.Int("removed_records", result.RemovedRecords),
			zap.Int("restored_file_flags", result.RestoredFileFlags),
			zap.Int("skipped_registered_files", result.SkippedRegisteredFiles))
	}
	return result, nil
}

// cleanupUnregisteredEmojiFiles 清理过期的未注册表情缓存文件，对齐 _cleanup_unregistered_emoji_files
func (r *Registry) cleanupUnregisteredEmojiFiles(ctx context.Context, mem *memory.Manager, rows []memory.EmojiRecord, cutoff, now time.Time) (EmojiCacheCleanupResult, error) {
	result := EmojiCacheCleanupResult{}
	registeredPaths := registeredEmojiPaths(mem, rows)
	processed := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if processed >= emojiCleanupBatchSize {
			break
		}
		if row.IsRegistered {
			result.SkippedRegisteredFiles++
			continue
		}
		cachePath := resolveCacheEmojiPath(mem, row.FullPath)
		fileExists := mem.EmojiRecordFileExists(&row)

		if row.NoFileFlag {
			if fileExists {
				if err := mem.ClearEmojiNoFileFlag(ctx, row.ID); err != nil {
					zap.L().Warn("恢复表情包文件标记失败", zap.Uint("id", row.ID), zap.Error(err))
					continue
				}
				result.RestoredFileFlags++
				processed++
			}
			continue
		}
		if !fileExists {
			if err := mem.MarkEmojiNoFile(ctx, row.ID); err != nil {
				zap.L().Warn("标记表情包文件缺失失败", zap.Uint("id", row.ID), zap.Error(err))
				continue
			}
			result.MarkedNoFileRecords++
			processed++
			continue
		}
		if emojiReferenceTime(row, now).After(cutoff) {
			continue
		}
		if cachePath == "" {
			zap.L().Warn("跳过不在表情包缓存目录内的文件清理", zap.String("path", row.FullPath))
			continue
		}
		if _, ok := registeredPaths[cachePath]; ok {
			result.SkippedRegisteredFiles++
			continue
		}
		info, statErr := os.Stat(cachePath)
		if statErr != nil {
			zap.L().Warn("读取表情包缓存文件失败", zap.String("file", cachePath), zap.Error(statErr))
			continue
		}
		if err := os.Remove(cachePath); err != nil {
			zap.L().Warn("清理表情包缓存文件失败", zap.String("file", cachePath), zap.Error(err))
			continue
		}
		if err := mem.MarkEmojiNoFile(ctx, row.ID); err != nil {
			zap.L().Warn("标记表情包文件缺失失败", zap.Uint("id", row.ID), zap.Error(err))
		}
		result.RemovedFiles++
		result.RemovedBytes += info.Size()
		result.MarkedNoFileRecords++
		processed++
	}
	return result, nil
}

// cleanupOrphanEmojiFiles 删除未被任何记录引用且早于保留期的缓存文件，对齐 _cleanup_orphan_emoji_files
func cleanupOrphanEmojiFiles(ctx context.Context, mem *memory.Manager, rows []memory.EmojiRecord, cutoff time.Time) (EmojiCacheCleanupResult, error) {
	result := EmojiCacheCleanupResult{}
	registeredPaths := registeredEmojiPaths(mem, rows)
	trackedPaths := trackedEmojiPaths(mem, rows)
	dir := mem.InstanceRoot()
	emojiDir := filepath.Join(dir, "data", "emoji")
	info, err := os.Stat(emojiDir)
	if err != nil || !info.IsDir() {
		return result, nil
	}
	walkErr := filepath.WalkDir(emojiDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			zap.L().Warn("读取孤立表情包缓存文件失败", zap.String("file", path), zap.Error(err))
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if result.RemovedOrphanFiles >= emojiCleanupBatchSize {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := emojiCacheExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
			return nil
		}
		resolved := path
		if abs, absErr := filepath.Abs(path); absErr == nil {
			resolved = abs
		}
		if _, ok := registeredPaths[resolved]; ok {
			result.SkippedRegisteredFiles++
			return nil
		}
		fileInfo, statErr := entry.Info()
		if statErr != nil {
			zap.L().Warn("读取孤立表情包缓存文件失败", zap.String("file", path), zap.Error(statErr))
			return nil
		}
		if _, ok := trackedPaths[resolved]; ok {
			return nil
		}
		if fileInfo.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			zap.L().Warn("清理孤立表情包缓存文件失败", zap.String("file", path), zap.Error(err))
			return nil
		}
		result.RemovedOrphanFiles++
		result.RemovedOrphanBytes += fileInfo.Size()
		return nil
	})
	return result, walkErr
}

// cleanupUnregisteredNoFileRecords 删除长期无文件的未注册记录，对齐 _cleanup_unregistered_no_file_records
func cleanupUnregisteredNoFileRecords(ctx context.Context, mem *memory.Manager, rows []memory.EmojiRecord, cutoff, now time.Time) (EmojiCacheCleanupResult, error) {
	result := EmojiCacheCleanupResult{}
	processed := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if processed >= emojiCleanupBatchSize {
			break
		}
		if row.IsRegistered {
			continue
		}
		if mem.EmojiRecordFileExists(&row) {
			if row.NoFileFlag {
				if err := mem.ClearEmojiNoFileFlag(ctx, row.ID); err != nil {
					zap.L().Warn("恢复表情包文件标记失败", zap.Uint("id", row.ID), zap.Error(err))
					continue
				}
				result.RestoredFileFlags++
				processed++
			}
			continue
		}
		if !row.NoFileFlag {
			if err := mem.MarkEmojiNoFile(ctx, row.ID); err != nil {
				zap.L().Warn("标记表情包文件缺失失败", zap.Uint("id", row.ID), zap.Error(err))
				continue
			}
			result.MarkedNoFileRecords++
			processed++
			continue
		}
		if emojiReferenceTime(row, now).After(cutoff) {
			continue
		}
		if err := mem.DeleteEmojiRecord(ctx, row.ID); err != nil {
			zap.L().Warn("删除未注册无文件表情包记录失败", zap.Uint("id", row.ID), zap.Error(err))
			continue
		}
		result.RemovedRecords++
		processed++
	}
	return result, nil
}

// registeredEmojiPaths 已注册表情的真实路径集合，对齐 _get_registered_emoji_paths
func registeredEmojiPaths(mem *memory.Manager, rows []memory.EmojiRecord) map[string]struct{} {
	paths := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if !row.IsRegistered {
			continue
		}
		if cachePath := resolveCacheEmojiPath(mem, row.FullPath); cachePath != "" {
			paths[cachePath] = struct{}{}
		}
	}
	return paths
}

// trackedEmojiPaths 全部表情记录涉及的缓存路径集合，对齐 _get_tracked_emoji_paths
func trackedEmojiPaths(mem *memory.Manager, rows []memory.EmojiRecord) map[string]struct{} {
	paths := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if cachePath := resolveCacheEmojiPath(mem, row.FullPath); cachePath != "" {
			paths[cachePath] = struct{}{}
		}
	}
	return paths
}

// resolveCacheEmojiPath 解析存储路径并限定在表情缓存目录内，对齐 _resolve_cache_emoji_path
func resolveCacheEmojiPath(mem *memory.Manager, storedPath string) string {
	if strings.TrimSpace(storedPath) == "" {
		return ""
	}
	if _, ok := emojiCacheExtensions[strings.ToLower(filepath.Ext(storedPath))]; !ok {
		return ""
	}
	resolved, err := imagesystem.ResolveStoredImagePath(mem.InstanceRoot(), storedPath)
	if err != nil {
		return ""
	}
	emojiDir := filepath.Join(mem.InstanceRoot(), "data", "emoji")
	if !strings.HasPrefix(resolved, emojiDir+string(os.PathSeparator)) && resolved != emojiDir {
		return ""
	}
	return resolved
}

// emojiReferenceTime 记录引用时间，对齐 _record_reference_time：
// last_used_time / register_time / record_time 依次兜底
func emojiReferenceTime(row memory.EmojiRecord, fallback time.Time) time.Time {
	if row.LastUsedTime != nil {
		return *row.LastUsedTime
	}
	if row.RegisterTime != nil {
		return *row.RegisterTime
	}
	if !row.RecordTime.IsZero() {
		return row.RecordTime
	}
	return fallback
}

// RunCacheCleanupLoop 按配置周期执行缓存清理，对齐 periodic_emoji_cache_cleanup
func (r *Registry) RunCacheCleanupLoop(ctx context.Context) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		settings := r.Settings().CacheCleanup
		if settings.Enabled {
			if _, err := r.RunEmojiCacheCleanup(ctx); err != nil {
				zap.L().Warn("表情包缓存自动清理失败", zap.Error(err))
			}
		}
		interval := settings.CacheCleanupInterval()
		if !settings.Enabled {
			interval = emojiCleanupDisabledPoll
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func mergeEmojiCleanupResult(target *EmojiCacheCleanupResult, source EmojiCacheCleanupResult) {
	target.RemovedFiles += source.RemovedFiles
	target.RemovedBytes += source.RemovedBytes
	target.MarkedNoFileRecords += source.MarkedNoFileRecords
	target.RemovedOrphanFiles += source.RemovedOrphanFiles
	target.RemovedOrphanBytes += source.RemovedOrphanBytes
	target.RemovedRecords += source.RemovedRecords
	target.RestoredFileFlags += source.RestoredFileFlags
	target.SkippedRegisteredFiles += source.SkippedRegisteredFiles
}
