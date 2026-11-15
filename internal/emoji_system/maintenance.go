package emojisystem

// 表情包目录定期维护，对齐 src/emoji_system/emoji_manager.py periodic_emoji_maintenance
// 与 check_emoji_file_integrity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"MaiGo/internal/common/database"

	"go.uber.org/zap"
)

// MaintenanceOptions 表情包维护参数
type MaintenanceOptions struct {
	StealEmoji        bool
	MaxRegNum         int
	DoReplace         bool
	CheckInterval     time.Duration
	MaxEmojiSizeBytes int64
	ContentFiltration bool
	BotName           string
}

// MaintenanceResult 单轮维护统计
type MaintenanceResult struct {
	Scanned    int
	Registered int
	Skipped    int
	Failed     int
	Removed    int
}

// RunMaintenance 按间隔循环执行表情包维护，直到 ctx 取消
func (r *Registry) RunMaintenance(ctx context.Context, opts MaintenanceOptions) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := opts.CheckInterval
	if interval <= 0 {
		interval = r.Settings().CheckInterval
	}
	if interval < 0 {
		interval = 0
	}
	// 缓存清理独立周期，对齐 main.py 单独启动的 periodic_emoji_cache_cleanup
	r.cacheLoopOnce.Do(func() { go r.RunCacheCleanupLoop(ctx) })
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-r.settingsWake():
			// 对齐 Python：热重载只重置等待，不立即执行维护
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			next := opts.CheckInterval
			if next <= 0 {
				next = r.Settings().CheckInterval
			}
			if next < 0 {
				next = 0
			}
			timer.Reset(next)
			continue
		}
		current := r.maintenanceOptions(opts)
		result, err := r.RunMaintenanceOnce(ctx, current)
		if err != nil {
			zap.L().Warn("表情包维护失败", zap.Error(err))
		} else if result.Scanned > 0 || result.Registered > 0 || result.Removed > 0 {
			zap.L().Info("表情包维护完成",
				zap.Int("scanned", result.Scanned),
				zap.Int("registered", result.Registered),
				zap.Int("skipped", result.Skipped),
				zap.Int("failed", result.Failed),
				zap.Int("removed", result.Removed))
		}
		if opts.CheckInterval <= 0 {
			interval = r.Settings().CheckInterval
		}
		if interval < 0 {
			interval = 0
		}
		timer.Reset(interval)
	}
}

// maintenanceOptions 每轮从当前配置刷新维护参数，对齐 Python 每轮读取全局配置
func (r *Registry) maintenanceOptions(opts MaintenanceOptions) MaintenanceOptions {
	settings := r.Settings()
	return MaintenanceOptions{
		StealEmoji:        settings.StealEmoji,
		MaxRegNum:         settings.MaxRegNum,
		DoReplace:         settings.DoReplace,
		CheckInterval:     settings.CheckInterval,
		MaxEmojiSizeBytes: settings.MaxEmojiSizeBytes,
		ContentFiltration: settings.ContentFiltration,
		BotName:           opts.BotName,
	}
}

// RunMaintenanceOnce 执行一轮扫描、注册与完整性检查
func (r *Registry) RunMaintenanceOnce(ctx context.Context, opts MaintenanceOptions) (MaintenanceResult, error) {
	result := MaintenanceResult{}
	if r == nil {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	mem := r.mem
	mgr := r.manager
	stickerDir := r.stickerDir
	r.mu.RUnlock()
	if mem == nil || mgr == nil || strings.TrimSpace(stickerDir) == "" {
		return result, nil
	}
	scanEnabled := opts.StealEmoji && (opts.MaxRegNum <= 0 || mgr.Count() < opts.MaxRegNum || opts.DoReplace)
	knownNames := map[string]struct{}{}
	if scanEnabled {
		var knownErr error
		knownNames, knownErr = knownStickerFileNames(ctx, mem, stickerDir)
		if knownErr != nil {
			return result, fmt.Errorf("读取表情包记录失败: %w", knownErr)
		}
	}

	var entries []os.DirEntry
	if scanEnabled {
		var readErr error
		entries, readErr = os.ReadDir(stickerDir)
		if readErr != nil {
			return result, fmt.Errorf("读取表情包目录失败: %w", readErr)
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			continue
		}
		result.Scanned++
		if _, known := knownNames[entry.Name()]; known {
			continue
		}
		if opts.MaxEmojiSizeBytes > 0 && info.Size() > opts.MaxEmojiSizeBytes {
			zap.L().Info("表情包文件超过收集上限，跳过",
				zap.String("file", entry.Name()), zap.Int64("size", info.Size()))
			result.Skipped++
			continue
		}
		status, err := r.registerEmojiByFilename(ctx, filepath.Join(stickerDir, entry.Name()), opts)
		if err != nil {
			zap.L().Warn("表情包注册失败", zap.String("file", entry.Name()), zap.Error(err))
			status = emojiRegisterFailed
		}
		switch status {
		case emojiRegisterRegistered:
			result.Registered++
		case emojiRegisterSkipped:
			result.Skipped++
		default:
			result.Failed++
		}
		// 对齐 Python：一轮只处理一个未知文件，任何结果都结束本轮扫描
		break
	}
	if err := mgr.ReloadFromDB(ctx); err != nil {
		zap.L().Warn("表情包维护后刷新内存池失败", zap.Error(err))
	}
	removed, removeErr := checkStickerIntegrity(ctx, mem, stickerDir)
	result.Removed = removed
	if removeErr != nil {
		return result, removeErr
	}
	if removed > 0 {
		if err := mgr.ReloadFromDB(ctx); err != nil {
			zap.L().Warn("表情包完整性检查后刷新内存池失败", zap.Error(err))
		}
	}
	return result, nil
}

// knownStickerFileNames 返回已注册、封禁或已消耗 VLM 的文件名，对齐 _is_known_emoji_maintenance_record
func knownStickerFileNames(ctx context.Context, mem *memory.Manager, stickerDir string) (map[string]struct{}, error) {
	rows, err := mem.ListEmojiRecords(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		// 对齐 Python：只把仍存在于目录中的记录算作已处理，文件缺失时允许重新注册
		if !knownEmojiMaintenanceRecord(row) {
			continue
		}
		if !mem.EmojiRecordFileExists(&row) {
			continue
		}
		name := emojiFileName(row)
		if name == "" {
			continue
		}
		out[filepath.Base(name)] = struct{}{}
	}
	return out, nil
}

// knownEmojiMaintenanceRecord 判断维护扫描是否应把记录对应文件视为已处理
// 对齐 _is_known_emoji_maintenance_record
func knownEmojiMaintenanceRecord(row memory.EmojiRecord) bool {
	return row.IsRegistered || row.IsBanned || row.VLMProcessed
}

// checkStickerIntegrity 对齐 check_emoji_file_integrity：
// 已注册但文件缺失的记录删除，未注册的保留为描述缓存
func checkStickerIntegrity(ctx context.Context, mem *memory.Manager, stickerDir string) (int, error) {
	rows, err := mem.ListEmojiRecords(ctx)
	if err != nil {
		return 0, fmt.Errorf("读取表情包记录失败: %w", err)
	}
	removed := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if row.NoFileFlag || !mem.EmojiRecordFileExists(&row) {
			if !row.IsRegistered {
				continue
			}
			if err := mem.DeleteEmojiRecord(ctx, row.ID); err != nil {
				zap.L().Warn("删除破损表情包记录失败", zap.Uint("id", row.ID), zap.Error(err))
				continue
			}
			removed++
		}
	}
	return removed, nil
}

// markStickerProcessed 记录 VLM 已处理文件，避免每轮重复消耗识别额度
func markStickerProcessed(ctx context.Context, mem *memory.Manager, hash, fullPath, description string) {
	if err := mem.MarkEmojiVLMProcessed(ctx, hash, fullPath, description); err != nil {
		zap.L().Warn("标记表情包 VLM 处理状态失败", zap.String("hash", hash), zap.Error(err))
	}
}

// extensionForImageFormat 生成临时文件名用的扩展名，对齐 Python 图片格式到扩展名
func extensionForImageFormat(format string) string {
	switch strings.ToLower(format) {
	case "png":
		return ".png"
	case "gif":
		return ".gif"
	case "webp":
		return ".webp"
	case "bmp":
		return ".bmp"
	case "jpeg", "jpg":
		return ".jpg"
	default:
		return ".jpg"
	}
}
