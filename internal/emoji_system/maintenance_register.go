package emojisystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/common/database"

	"go.uber.org/zap"
)

// 表情包注册状态，对齐 EmojiRegisterStatus
const (
	emojiRegisterRegistered = memory.EmojiRegisterRegistered
	emojiRegisterSkipped    = memory.EmojiRegisterSkipped
	emojiRegisterFailed     = memory.EmojiRegisterFailed
)

// RegisterEmojiByFilename 对齐 register_emoji_by_filename：识别格式、补扩展名、按需描述与审核后注册
//
// 与 Python 一致：保留 data/emoji 中的原文件名，不把文件移动到 hash 名称
func (r *Registry) RegisterEmojiByFilename(ctx context.Context, filename string, opts MaintenanceOptions) (string, error) {
	status, err := r.registerEmojiByFilename(ctx, filename, opts)
	if err != nil {
		return status, err
	}
	// 对齐 Python：注册成功后立即进入可发送内存池
	if status == emojiRegisterRegistered {
		if reloadErr := r.Reload(ctx); reloadErr != nil {
			zap.L().Warn("[register_emoji] 注册后刷新内存池失败", zap.Error(reloadErr))
		}
	}
	return status, nil
}

func (r *Registry) registerEmojiByFilename(ctx context.Context, filename string, opts MaintenanceOptions) (string, error) {
	if r == nil {
		return emojiRegisterFailed, nil
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
		return emojiRegisterFailed, errMemoryUnavailable
	}

	fullPath, err := filepath.Abs(strings.TrimSpace(filename))
	if err != nil {
		return emojiRegisterFailed, err
	}
	if !fileExists(fullPath) {
		zap.L().Error("[register_emoji] 表情包文件不存在", zap.String("path", fullPath))
		return emojiRegisterFailed, nil
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		zap.L().Error("[register_emoji] 读取表情包文件失败", zap.String("path", fullPath), zap.Error(err))
		return emojiRegisterFailed, nil
	}
	hash := sha256Bytes(content)
	format, err := detectImageFormat(content)
	if err != nil {
		zap.L().Error("[register_emoji] 无法识别表情包格式", zap.String("path", fullPath), zap.Error(err))
		return emojiRegisterFailed, nil
	}
	fullPath, fileName, err := normalizeEmojiFilePath(stickerDir, fullPath, filepath.Base(fullPath), format, hash)
	if err != nil {
		zap.L().Error("[register_emoji] 修正表情包文件名失败", zap.String("path", fullPath), zap.Error(err))
		return emojiRegisterFailed, nil
	}

	row, rowErr := mem.GetEmojiRecordByHash(ctx, hash)
	if rowErr != nil {
		zap.L().Error("[register_emoji] 查询表情包记录失败", zap.String("path", fullPath), zap.Error(rowErr))
		return emojiRegisterFailed, nil
	}
	if row != nil && row.IsBanned {
		return emojiRegisterSkipped, nil
	}
	if row != nil && row.IsRegistered && mem.EmojiRecordFileExists(row) {
		return emojiRegisterSkipped, nil
	}

	description := ""
	queryCount := 0
	if row != nil {
		description = strings.TrimSpace(row.Description)
		queryCount = row.QueryCount
	}
	if description != "" {
		description = strings.Join(NormalizeEmotionTags(description), ",")
	}
	if mgr.InPool(hash) {
		return emojiRegisterSkipped, nil
	}

	if description == "" {
		built, buildErr := r.describeEmojiFile(ctx, fullPath, format, hash, fileName)
		if buildErr != nil {
			zap.L().Warn("[register_emoji] 生成表情包描述失败", zap.String("path", fullPath), zap.Error(buildErr))
			markStickerProcessed(ctx, mem, hash, fullPath, "")
			return emojiRegisterFailed, nil
		}
		description = built
	}
	// 对齐 Python：描述构建完成后无条件标记 VLM 已处理
	markStickerProcessed(ctx, mem, hash, fullPath, description)

	if opts.ContentFiltration || r.filtrationEnabled() {
		approved, reviewErr := r.reviewEmojiContent(ctx, fullPath, format)
		if reviewErr != nil {
			zap.L().Warn("[register_emoji] 表情包内容审核失败", zap.String("path", fullPath), zap.Error(reviewErr))
			markStickerProcessed(ctx, mem, hash, fullPath, description)
			return emojiRegisterFailed, nil
		}
		if !approved {
			zap.L().Warn("[register_emoji] 表情包未通过内容审核", zap.String("path", fullPath))
			markStickerProcessed(ctx, mem, hash, fullPath, description)
			return emojiRegisterFailed, nil
		}
	}

	if mgr.Count() >= opts.MaxRegNum && opts.DoReplace {
		zap.L().Warn("[register_emoji] 表情包数量已达上限，尝试替换", zap.Int("max", opts.MaxRegNum))
		replacement, chooseErr := r.chooseReplacement(ctx, description, opts)
		if chooseErr != nil {
			zap.L().Warn("[register_emoji] 选择替换表情失败", zap.Error(chooseErr))
			return emojiRegisterFailed, nil
		}
		if replacement == nil {
			return emojiRegisterFailed, nil
		}
		// 对齐 Python：替换前先把新表情标记为 VLM 已处理，避免替换失败后重复识别
		markStickerProcessed(ctx, mem, hash, fullPath, description)
		if unregisterErr := mem.UnregisterEmoji(ctx, replacement.ID); unregisterErr != nil {
			zap.L().Warn("[register_emoji] 取消注册旧表情失败", zap.Uint("id", replacement.ID), zap.Error(unregisterErr))
			return emojiRegisterFailed, nil
		}
		status, saveErr := mem.RegisterEmojiToDB(ctx, memory.EmojiRecordInput{
			ImageHash:   hash,
			FullPath:    fullPath,
			Description: description,
			QueryCount:  queryCount,
		})
		if saveErr != nil {
			zap.L().Warn("[register_emoji] 写入替换表情失败", zap.Error(saveErr))
			return emojiRegisterFailed, nil
		}
		if status == emojiRegisterRegistered {
			zap.L().Info("表情包替换完成", zap.String("old", replacement.FileName), zap.String("new", fileName))
		}
		return status, nil
	}

	status, saveErr := mem.RegisterEmojiToDB(ctx, memory.EmojiRecordInput{
		ImageHash:   hash,
		FullPath:    fullPath,
		Description: description,
		QueryCount:  queryCount,
	})
	if saveErr != nil {
		return emojiRegisterFailed, saveErr
	}
	if status == emojiRegisterRegistered {
		zap.L().Info("表情包注册成功", zap.String("file", fileName), zap.String("hash", hash))
	}
	return status, nil
}

// normalizeEmojiFilePath 按真实图片格式修正扩展名，对齐 _rename_file_to_match_format
func normalizeEmojiFilePath(dir, sourcePath, fileName, format, hash string) (string, string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return sourcePath, fileName, fmt.Errorf("图片格式为空")
	}
	ext := fileExtensionForFormat(format)
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	desired := base + ext
	target := filepath.Join(dir, desired)
	source := filepath.Join(dir, fileName)
	if strings.EqualFold(source, target) {
		return target, desired, nil
	}
	if fileExists(target) {
		targetHash, err := sha256File(target)
		if err == nil && targetHash == hash {
			if !strings.EqualFold(source, target) {
				_ = os.Remove(source)
			}
			return target, desired, nil
		}
		if err != nil {
			return "", "", err
		}
		for i := 1; ; i++ {
			candidate := filepath.Join(dir, fmt.Sprintf("%s_%s_%d%s", base, hash[:8], i, ext))
			if !fileExists(candidate) {
				target = candidate
				desired = filepath.Base(candidate)
				break
			}
		}
	}
	if err := os.Rename(source, target); err != nil {
		return "", "", err
	}
	return target, desired, nil
}

// fileExtensionForFormat 对齐 Python 的 image_format 扩展名（jpeg 保留 .jpeg）
func fileExtensionForFormat(format string) string {
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		return ".jpeg"
	case "png":
		return ".png"
	case "gif":
		return ".gif"
	case "webp":
		return ".webp"
	case "bmp":
		return ".bmp"
	case "avif":
		return ".avif"
	case "mpo":
		return ".mpo"
	default:
		return "." + strings.ToLower(format)
	}
}
