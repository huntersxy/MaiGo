package emojisystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"MaiGo/internal/common/database"

	"golang.org/x/image/webp"
	"gorm.io/gorm"
)

// WebUIEmojiUpdate 表情包增量更新
type WebUIEmojiUpdate struct {
	Description  *string
	Emotion      *string
	IsRegistered *bool
	IsBanned     *bool
}

// FiledCount 返回本次实际写入的字段数（对齐 Python len(update_data)）
func (u WebUIEmojiUpdate) FiledCount() int {
	fields := map[string]struct{}{}
	if u.Description != nil || u.Emotion != nil {
		fields["description"] = struct{}{}
	}
	if u.IsRegistered != nil {
		fields["is_registered"] = struct{}{}
	}
	if u.IsBanned != nil {
		fields["is_banned"] = struct{}{}
	}
	return len(fields)
}

// Empty 是否没有任何需要更新的字段
func (u WebUIEmojiUpdate) Empty() bool { return u.FiledCount() == 0 }

// WebUIEmojiUpload 上传表情包入参
type WebUIEmojiUpload struct {
	Content      []byte
	FileName     string
	Description  string
	Emotion      string
	IsRegistered bool
}

// ReviewForRegistration 注册前内容审核；未启用内容过滤时直接通过
func (s *WebUIService) ReviewForRegistration(ctx context.Context, path, format string) error {
	if !s.registry.filtrationEnabled() {
		return nil
	}
	passed, err := s.registry.reviewEmojiContent(ctx, path, format)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEmojiReviewRejected, err)
	}
	if !passed {
		return ErrEmojiReviewRejected
	}
	return nil
}

// Update 增量更新表情包，返回更新后视图与字段数
func (s *WebUIService) Update(ctx context.Context, id uint, update WebUIEmojiUpdate) (WebUIEmoji, int, error) {
	var row memory.EmojiRecord
	if err := emojiScopeQuery(s.db(ctx)).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WebUIEmoji{}, 0, ErrEmojiNotFound
		}
		return WebUIEmoji{}, 0, err
	}
	if update.Empty() {
		return WebUIEmoji{}, 0, errors.New("未提供任何需要更新的字段")
	}
	fieldCount := update.FiledCount()
	updates := map[string]any{}
	// 对齐 Python：新注册时补 register_time
	if update.IsRegistered != nil && *update.IsRegistered && !row.IsRegistered {
		path, err := s.ResolveStoredPath(row.FullPath)
		if err != nil {
			return WebUIEmoji{}, 0, err
		}
		if err := s.ReviewForRegistration(ctx, path, emojiFormat(row.FullPath)); err != nil {
			return WebUIEmoji{}, 0, err
		}
		updates["register_time"] = time.Now()
		fieldCount++
	}
	if update.Description != nil || update.Emotion != nil {
		description := ""
		if update.Description != nil {
			description = *update.Description
		}
		emotion := ""
		if update.Emotion != nil {
			emotion = *update.Emotion
		}
		updates["description"] = NormalizeDescription(description, emotion)
	}
	if update.IsRegistered != nil {
		updates["is_registered"] = *update.IsRegistered
	}
	if update.IsBanned != nil {
		updates["is_banned"] = *update.IsBanned
	}
	if err := s.db(ctx).Model(&memory.EmojiRecord{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		return WebUIEmoji{}, 0, err
	}
	current, err := s.mem.GetEmojiRecordByID(ctx, row.ID)
	if err != nil {
		return WebUIEmoji{}, 0, err
	}
	// 通过更新接口封禁或退注册时从内存池移除，避免继续被选中
	if (update.IsBanned != nil && *update.IsBanned) || (update.IsRegistered != nil && !*update.IsRegistered) {
		if mgr := s.registry.Manager(); mgr != nil {
			mgr.RemoveFromPool(current.ImageHash)
		}
	}
	return s.toWebUI(*current), fieldCount, nil
}

// Register 注册表情包
func (s *WebUIService) Register(ctx context.Context, id uint) (WebUIEmoji, string, error) {
	var row memory.EmojiRecord
	if err := emojiScopeQuery(s.db(ctx)).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WebUIEmoji{}, "", ErrEmojiNotFound
		}
		return WebUIEmoji{}, "", err
	}
	if row.IsRegistered {
		return s.toWebUI(row), "表情包已注册", nil
	}
	path, err := s.ResolveStoredPath(row.FullPath)
	if err != nil {
		return WebUIEmoji{}, "", err
	}
	if err := s.ReviewForRegistration(ctx, path, emojiFormat(row.FullPath)); err != nil {
		return WebUIEmoji{}, "", err
	}
	now := time.Now()
	if err := s.db(ctx).Model(&memory.EmojiRecord{}).Where("id = ?", row.ID).Updates(map[string]any{
		"is_registered": true,
		"is_banned":     false,
		"register_time": now,
	}).Error; err != nil {
		return WebUIEmoji{}, "", err
	}
	row.IsRegistered = true
	row.IsBanned = false
	row.RegisterTime = &now
	s.registry.reloadQuietly(ctx)
	return s.toWebUI(row), "表情包注册成功", nil
}

// Ban 禁用表情包
func (s *WebUIService) Ban(ctx context.Context, id uint) (WebUIEmoji, error) {
	var row memory.EmojiRecord
	if err := emojiScopeQuery(s.db(ctx)).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WebUIEmoji{}, ErrEmojiNotFound
		}
		return WebUIEmoji{}, err
	}
	if err := s.db(ctx).Model(&memory.EmojiRecord{}).Where("id = ?", row.ID).Updates(map[string]any{
		"is_banned":     true,
		"is_registered": false,
	}).Error; err != nil {
		return WebUIEmoji{}, err
	}
	row.IsBanned = true
	row.IsRegistered = false
	// 同步从内存列表移除，确保插件等消费者不会继续选中已封禁表情包
	if mgr := s.registry.Manager(); mgr != nil {
		mgr.RemoveFromPool(row.ImageHash)
	}
	s.registry.reloadQuietly(ctx)
	return s.toWebUI(row), nil
}

// Delete 删除表情包文件与记录
func (s *WebUIService) Delete(ctx context.Context, id uint) (string, error) {
	var row memory.EmojiRecord
	if err := emojiScopeQuery(s.db(ctx)).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrEmojiNotFound
		}
		return "", err
	}
	if err := s.deleteStoredFile(row.FullPath); err != nil {
		return "", err
	}
	if err := s.db(ctx).Delete(&memory.EmojiRecord{}, row.ID).Error; err != nil {
		return "", err
	}
	_ = ClearThumbnailForHash(row.ImageHash)
	if row.IsRegistered {
		if mgr := s.registry.Manager(); mgr != nil {
			mgr.RemoveFromPool(row.ImageHash)
		}
		s.registry.reloadQuietly(ctx)
	}
	return row.ImageHash, nil
}

// DeleteBatch 批量删除，返回成功数量与失败 ID
func (s *WebUIService) DeleteBatch(ctx context.Context, ids []uint) (int, []uint) {
	deleted := 0
	failed := make([]uint, 0)
	for _, id := range ids {
		if _, err := s.Delete(ctx, id); err != nil {
			failed = append(failed, id)
			continue
		}
		deleted++
	}
	return deleted, failed
}

func (s *WebUIService) deleteStoredFile(storedPath string) error {
	path, err := s.ResolveStoredPath(storedPath)
	if err != nil {
		if errors.Is(err, ErrEmojiFileMissing) {
			return nil
		}
		return nil
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return statErr
	}
	if info.IsDir() {
		return fmt.Errorf("%w: 路径不是文件 %s", ErrEmojiFileMissing, path)
	}
	return os.Remove(path)
}

// Upload 上传并注册表情包；existed 表示命中已有哈希记录
func (s *WebUIService) Upload(ctx context.Context, upload WebUIEmojiUpload) (WebUIEmoji, string, bool, error) {
	if len(upload.Content) == 0 {
		return WebUIEmoji{}, "", false, errors.New("文件内容为空")
	}
	format, err := detectImageFormat(upload.Content)
	if err != nil {
		return WebUIEmoji{}, "", false, fmt.Errorf("%w: %v", ErrEmojiInvalidImage, err)
	}
	hash := sha256Bytes(upload.Content)
	description := NormalizeDescription(upload.Description, upload.Emotion)
	now := time.Now()

	existing, err := s.mem.GetEmojiRecordByHash(ctx, hash)
	if err != nil {
		return WebUIEmoji{}, "", false, err
	}
	if existing != nil {
		storedPath := existing.FullPath
		// 文件缺失或标记为无文件时重新落盘，对齐 _resolve_existing_emoji_file_path 分支
		if existing.NoFileFlag || !s.mem.EmojiRecordFileExists(existing) {
			saved, saveErr := s.saveUploadedFile(upload.Content, hash, format)
			if saveErr != nil {
				return WebUIEmoji{}, "", false, saveErr
			}
			storedPath = saved
		}
		if err := s.db(ctx).Model(&memory.EmojiRecord{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"full_path":     storedPath,
			"no_file_flag":  false,
			"description":   description,
			"is_registered": true,
			"is_banned":     false,
			"register_time": now,
		}).Error; err != nil {
			return WebUIEmoji{}, "", false, err
		}
		current, loadErr := s.mem.GetEmojiRecordByID(ctx, existing.ID)
		if loadErr != nil {
			return WebUIEmoji{}, "", false, loadErr
		}
		s.registry.reloadQuietly(ctx)
		return s.toWebUI(*current), "表情包已存在，已标记为据为己用", true, nil
	}
	storedPath, err := s.saveUploadedFile(upload.Content, hash, format)
	if err != nil {
		return WebUIEmoji{}, "", false, err
	}
	row := memory.EmojiRecord{
		ImageHash:    hash,
		Description:  description,
		FullPath:     storedPath,
		ImageType:    schemaImageTypeEmoji,
		QueryCount:   0,
		IsRegistered: true,
		IsBanned:     false,
		NoFileFlag:   false,
		RecordTime:   now,
		RegisterTime: &now,
	}
	if err := s.db(ctx).Create(&row).Error; err != nil {
		return WebUIEmoji{}, "", false, err
	}
	s.registry.reloadQuietly(ctx)
	return s.toWebUI(row), "表情包上传成功并已注册", false, nil
}

func (s *WebUIService) fileExists(fileName string) bool {
	path := s.FilePath(fileName)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *WebUIService) saveUploadedFile(content []byte, hash, format string) (string, error) {
	if strings.TrimSpace(s.dir) == "" {
		return "", errors.New("sticker 目录未配置")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	timestamp := time.Now().Unix()
	base := fmt.Sprintf("emoji_%d_%s", timestamp, hash[:8])
	fileName := base + "." + format
	counter := 1
	for {
		path := filepath.Join(s.dir, fileName)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		fileName = fmt.Sprintf("%s_%d.%s", base, counter, format)
		counter++
		if counter > 1000 {
			return "", errors.New("无法生成唯一的表情包文件名")
		}
	}
	target := filepath.Join(s.dir, fileName)
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return "", err
	}
	// 落库统一存相对实例根目录的存储路径，对齐 serialize_stored_image_path
	return s.mem.SerializeStoredPath(target)
}

// detectImageFormat 识别图片格式，对齐 PIL img.format.lower()
func detectImageFormat(content []byte) (string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err == nil {
		_ = cfg
		return strings.ToLower(format), nil
	}
	if _, err := webp.DecodeConfig(bytes.NewReader(content)); err == nil {
		return "webp", nil
	}
	return "", err
}

// ResolveFilePath 查找表情包本地文件，缺失返回 ErrEmojiFileMissing
func (s *WebUIService) ResolveFilePath(fileName string) (string, error) {
	return s.ResolveStickerPath(fileName)
}

// ResolveStickerPath 按文件名或存储路径定位表情文件
//
// 传入值可能是库里的相对存储路径，也可能是旧的纯文件名；两种都兼容。
func (s *WebUIService) ResolveStickerPath(nameOrPath string) (string, error) {
	value := strings.TrimSpace(nameOrPath)
	if value == "" {
		return "", ErrEmojiFileMissing
	}
	if s.mem != nil {
		if path, err := s.mem.ResolveStoredPath(value); err == nil {
			if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	path := s.FilePath(filepath.Base(filepath.FromSlash(value)))
	if path == "" {
		return "", ErrEmojiFileMissing
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", ErrEmojiFileMissing
	}
	return path, nil
}

// ResolveStoredPath 解析记录的 full_path，缺失返回 ErrEmojiFileMissing
func (s *WebUIService) ResolveStoredPath(storedPath string) (string, error) {
	return s.ResolveStickerPath(storedPath)
}

// reloadQuietly 刷新内存池，失败不影响主流程
func (r *Registry) reloadQuietly(ctx context.Context) {
	if err := r.Reload(ctx); err != nil {
		// 内存池刷新失败只影响即时可用性，数据库已是真实状态
		return
	}
}

// RemoveFromPool 从内存池移除指定哈希（对齐 _remove_emoji_from_memory）
func (m *Manager) RemoveFromPool(hash string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := make([]Emoji, 0, len(m.cached))
	for _, item := range m.cached {
		if item.FileHash == hash {
			continue
		}
		filtered = append(filtered, item)
	}
	m.cached = filtered
}
