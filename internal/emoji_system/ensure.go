package emojisystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	imagesystem "MaiGo/internal/chat/image_system"
	"MaiGo/internal/chat/image_system/schema"
	"MaiGo/internal/common/database"
)

// schemaImageTypeEmoji 表情包记录类型，取值对齐 Python ImageType 枚举名
const schemaImageTypeEmoji = schema.ImageTypeEmoji

const emojiBytesPerMiB = 1024 * 1024

// sha256Bytes 计算表情包内容哈希，对齐 MaiBot hashlib.sha256
func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sha256File 计算表情包文件哈希，对齐 MaiEmoji.calculate_hash_format
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// EnsureEmojiSaved 缓存表情包文件与未注册记录，对齐 ensure_emoji_saved
func (r *Registry) EnsureEmojiSaved(ctx context.Context, content []byte, hash string) (*Emoji, bool, error) {
	if r == nil {
		return nil, false, errMemoryUnavailable
	}
	settings := r.Settings()
	r.mu.RLock()
	mgr := r.manager
	r.mu.RUnlock()
	if mgr == nil {
		return nil, false, errMemoryUnavailable
	}
	return mgr.ensureSaved(ctx, content, hash, settings.MaxEmojiSizeBytes)
}

// EnsureEmojiSaved 使用全局 Registry 缓存表情包
func EnsureEmojiSaved(ctx context.Context, content []byte, hash string) (*Emoji, bool, error) {
	return DefaultRegistry().EnsureEmojiSaved(ctx, content, hash)
}

func (m *Manager) ensureSaved(ctx context.Context, content []byte, hash string, maxSizeBytes int64) (*Emoji, bool, error) {
	if len(content) == 0 {
		return nil, false, fmt.Errorf("emojisystem: 表情图片为空")
	}
	if maxSizeBytes > 0 && int64(len(content)) > maxSizeBytes {
		limit := strconv.FormatFloat(float64(maxSizeBytes)/emojiBytesPerMiB, 'g', -1, 64)
		return nil, false, fmt.Errorf("表情包大小超过收集上限 %s MB，跳过缓存", limit)
	}
	// 对齐 MaiEmoji.calculate_hash_format：最终哈希始终由内容计算
	hash = sha256Bytes(content)
	if m == nil || m.mem == nil {
		return nil, false, errMemoryUnavailable
	}
	dir := m.stickerDirectory()
	if dir == "" {
		return nil, false, errDescribeDeps
	}

	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	// 已存在可用记录时直接复用，对齐 _ensure_emoji_saved_locked 的提前返回
	row, rowErr := m.mem.GetEmojiRecordByHash(ctx, hash)
	if rowErr != nil {
		return nil, false, fmt.Errorf("emojisystem: 查询表情包记录失败: %w", rowErr)
	}
	if row != nil && !row.NoFileFlag && m.mem.EmojiRecordFileExists(row) {
		item := emojiFromRecord(*row)
		return &item, false, nil
	}

	format, err := detectImageFormat(content)
	if err != nil {
		return nil, false, fmt.Errorf("emojisystem: 无法识别表情图片格式: %w", err)
	}
	fileName := hash + fileExtensionForFormat(format)
	target := stickerFilePath(dir, fileName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, fmt.Errorf("emojisystem: 创建表情包目录失败: %w", err)
	}
	tmpPath := filepath.Join(dir, hash+".tmp")
	if err := os.WriteFile(tmpPath, content, 0o644); err != nil {
		return nil, false, fmt.Errorf("emojisystem: 写入表情包临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return nil, false, fmt.Errorf("emojisystem: 保存表情包文件失败: %w", err)
	}

	// 落库：先转相对实例根目录的存储路径，再按是否存在决定新建或回填
	storedPath, err := imagesystem.SerializeStoredImagePath(m.instanceRoot(), target)
	if err != nil {
		return nil, false, fmt.Errorf("emojisystem: 序列化表情包路径失败: %w", err)
	}
	created := false
	if row == nil {
		now := time.Now()
		row = &memory.EmojiRecord{
			ImageHash:    hash,
			FullPath:     storedPath,
			Description:  "",
			ImageType:    schemaImageTypeEmoji,
			QueryCount:   0,
			IsRegistered: false,
			IsBanned:     false,
			NoFileFlag:   false,
			RecordTime:   now,
		}
		if err := m.mem.WithContext(ctx).GetDB().Create(row).Error; err != nil {
			return nil, false, fmt.Errorf("emojisystem: 写入表情包记录失败: %w", err)
		}
		created = true
	} else {
		if err := m.mem.WithContext(ctx).GetDB().Model(&memory.EmojiRecord{}).Where("id = ?", row.ID).
			Updates(map[string]any{"full_path": storedPath, "no_file_flag": false}).Error; err != nil {
			return nil, false, fmt.Errorf("emojisystem: 回填表情包文件路径失败: %w", err)
		}
		row.FullPath = storedPath
		row.NoFileFlag = false
	}
	item := emojiFromRecord(*row)
	return &item, created, nil
}

// RegisterEmoji 把已缓存的表情包注册入库，对齐 register_emoji_to_db
//
// 返回 "registered" / "skipped" / "failed"，与 Python EmojiRegisterStatus 一致。
func (r *Registry) RegisterEmoji(ctx context.Context, emoji *Emoji, description string) (string, error) {
	if r == nil || emoji == nil {
		return memory.EmojiRegisterFailed, nil
	}
	r.mu.RLock()
	mgr := r.manager
	r.mu.RUnlock()
	if mgr == nil || mgr.mem == nil {
		return memory.EmojiRegisterFailed, errMemoryUnavailable
	}
	fullPath := mgr.EmojiFilePath(emoji)
	if fullPath == "" {
		return memory.EmojiRegisterFailed, errStickerFileMissing
	}
	if desc := strings.TrimSpace(description); desc != "" {
		emoji.Description = desc
	}
	status, err := mgr.mem.RegisterEmojiToDB(ctx, memory.EmojiRecordInput{
		ImageHash:    emoji.FileHash,
		FullPath:     fullPath,
		Description:  emoji.Description,
		QueryCount:   emoji.QueryCount,
		RegisterTime: emoji.RegisterTime,
		LastUsedTime: emoji.LastUsedTime,
	})
	if err == nil && status == memory.EmojiRegisterRegistered {
		ScheduleEmojiIndex(emoji.FileHash)
	}
	return status, err
}

// SetEmojiDescription 直接写入表情包描述，描述为空时不做变更
func (r *Registry) SetEmojiDescription(ctx context.Context, hash, description string) (*Emoji, error) {
	if r == nil {
		return nil, errMemoryUnavailable
	}
	r.mu.RLock()
	mgr := r.manager
	r.mu.RUnlock()
	if mgr == nil || mgr.mem == nil {
		return nil, errMemoryUnavailable
	}
	hash = strings.TrimSpace(hash)
	description = strings.Join(NormalizeEmotionTags(description), ",")
	if hash == "" || description == "" {
		return nil, errInvalidHash
	}
	row, err := mgr.mem.GetEmojiRecordByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errEmojiNotFound
	}
	if err := mgr.mem.UpdateEmojiDescription(ctx, row.ID, description); err != nil {
		return nil, err
	}
	row.Description = description
	item := emojiFromRecord(*row)
	if err := mgr.ReloadFromDB(ctx); err != nil {
		return &item, err
	}
	return &item, nil
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
