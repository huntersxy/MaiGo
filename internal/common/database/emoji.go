package memory

// 表情包数据访问，对齐 MaiBot src/emoji_system/emoji_manager.py。
//
// 表情包与普通图片共用 images 表（database_model.Images），用 image_type='EMOJI'
// 区分。列语义：file_hash→image_hash、file_name→full_path（相对实例根的 POSIX
// 路径）、use_count→query_count、updated_at→register_time/last_used_time。
import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	imagesystem "MaiGo/internal/chat/image_system"
	"MaiGo/internal/chat/image_system/schema"
)

// EmojiRecord 表情包数据库记录（images 表，image_type='EMOJI'）
type EmojiRecord = schema.ImageRecord

// EmojiRecordInput 写入表情包记录所需的字段，对应 Python MaiEmoji 落库部分
type EmojiRecordInput struct {
	ImageHash    string
	FullPath     string // 实例内绝对路径，写库前会序列化为相对路径
	Description  string
	QueryCount   int
	RegisterTime *time.Time
	LastUsedTime *time.Time
}

// InstanceRoot 返回实例根目录，对齐 Python src/common/paths.py get_instance_root
//
// 优先 MAIBOT_INSTANCE_DIR，否则取进程工作目录；与本进程启动期解析口径一致。
func (m *Manager) InstanceRoot() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		return value
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// emojiScope 限定表情包记录范围，避免与普通图片互相干扰
func emojiScope(db *gorm.DB) *gorm.DB {
	return db.Where("image_type = ?", schema.ImageTypeEmoji)
}

// GetEmojiRecordByHash 按图片哈希取表情包记录；不存在时返回 (nil, nil)，对齐 Python `.first()`
func (m *Manager) GetEmojiRecordByHash(ctx context.Context, imageHash string) (*EmojiRecord, error) {
	imageHash = strings.TrimSpace(imageHash)
	if imageHash == "" {
		return nil, nil
	}
	var record EmojiRecord
	err := emojiScope(m.db.WithContext(ctx)).Where("image_hash = ?", imageHash).Limit(1).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// GetEmojiRecordByID 按主键取表情包记录
func (m *Manager) GetEmojiRecordByID(ctx context.Context, id uint) (*EmojiRecord, error) {
	var record EmojiRecord
	if err := emojiScope(m.db.WithContext(ctx)).Where("id = ?", id).Take(&record).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

// ListEmojiRecords 取全部表情包记录（含未注册、已封禁、无文件标记）
func (m *Manager) ListEmojiRecords(ctx context.Context) ([]EmojiRecord, error) {
	var records []EmojiRecord
	if err := emojiScope(m.db.WithContext(ctx)).Order("id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListRegisteredEmojiRecords 取已注册且未封禁的表情包记录，对齐 load_emojis_from_db 的筛选条件
//
// Python 刻意保留 no_file_flag 记录，交由上层清理破损注册记录。
func (m *Manager) ListRegisteredEmojiRecords(ctx context.Context) ([]EmojiRecord, error) {
	var records []EmojiRecord
	if err := emojiScope(m.db.WithContext(ctx)).
		Where("is_registered = ? AND is_banned = ?", true, false).
		Order("id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// RegisterEmojiToDB 对齐 register_emoji_to_db：表情包注册入库，不移动源文件
//
// 分支与 Python 一致：
//   - 记录已封禁 → "skipped"
//   - 记录已注册且文件仍可用 → "skipped"
//   - 记录存在但需重新激活 → 写 full_path/description/query_count/register_time，
//     is_registered=true、is_banned=false、no_file_flag=false，返回 "registered"
//   - 记录不存在 → 新建，is_registered=true，register_time=now，返回 "registered"
func (m *Manager) RegisterEmojiToDB(ctx context.Context, input EmojiRecordInput) (string, error) {
	imageHash := strings.TrimSpace(input.ImageHash)
	if imageHash == "" {
		return EmojiRegisterFailed, nil
	}
	root := m.InstanceRoot()
	storedPath, err := imagesystem.SerializeStoredImagePath(root, input.FullPath)
	if err != nil {
		return EmojiRegisterFailed, err
	}
	record, err := m.GetEmojiRecordByHash(ctx, imageHash)
	if err != nil {
		return EmojiRegisterFailed, err
	}
	now := time.Now()
	if record != nil {
		if record.IsBanned {
			return EmojiRegisterSkipped, nil
		}
		if record.IsRegistered && m.emojiRecordFileAvailable(record) {
			return EmojiRegisterSkipped, nil
		}
		description := strings.TrimSpace(input.Description)
		if description == "" {
			description = record.Description
		}
		queryCount := record.QueryCount
		if input.QueryCount > queryCount {
			queryCount = input.QueryCount
		}
		lastUsedTime := record.LastUsedTime
		if input.LastUsedTime != nil {
			lastUsedTime = input.LastUsedTime
		}
		registerTime := record.RegisterTime
		if registerTime == nil {
			registerTime = &now
		}
		updates := map[string]any{
			"full_path":      storedPath,
			"description":    description,
			"query_count":    queryCount,
			"last_used_time": lastUsedTime,
			"is_registered":  true,
			"is_banned":      false,
			"no_file_flag":   false,
			"register_time":  registerTime,
		}
		if err := m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return EmojiRegisterFailed, err
		}
		return EmojiRegisterRegistered, nil
	}
	created := &EmojiRecord{
		ImageHash:    imageHash,
		Description:  strings.TrimSpace(input.Description),
		FullPath:     storedPath,
		ImageType:    schema.ImageTypeEmoji,
		QueryCount:   input.QueryCount,
		IsRegistered: true,
		IsBanned:     false,
		NoFileFlag:   false,
		RecordTime:   now,
		RegisterTime: &now,
		LastUsedTime: input.LastUsedTime,
	}
	if err := m.db.WithContext(ctx).Create(created).Error; err != nil {
		return EmojiRegisterFailed, err
	}
	return EmojiRegisterRegistered, nil
}

// UnregisterEmoji 取消注册但保留文件与描述，对齐 unregister_emoji：只写 is_registered=false
func (m *Manager) UnregisterEmoji(ctx context.Context, id uint) error {
	return requireAffected(m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", id).Updates(map[string]any{
		"is_registered": false,
	}))
}

// MarkEmojiVLMProcessed 记录已消耗 VLM 额度的表情包，对齐 _mark_emoji_vlm_processed
//
// 记录不存在时按未注册缓存记录新建（is_registered=false、query_count=0、
// register_time/last_used_time 为空）；存在时统一回写 full_path、no_file_flag=false。
func (m *Manager) MarkEmojiVLMProcessed(ctx context.Context, imageHash, fullPath, description string) error {
	imageHash = strings.TrimSpace(imageHash)
	if imageHash == "" {
		return nil
	}
	storedPath := ""
	if strings.TrimSpace(fullPath) != "" {
		serialized, err := imagesystem.SerializeStoredImagePath(m.InstanceRoot(), fullPath)
		if err != nil {
			return err
		}
		storedPath = serialized
	}
	record, err := m.GetEmojiRecordByHash(ctx, imageHash)
	if err != nil {
		return err
	}
	if record == nil {
		now := time.Now()
		created := &EmojiRecord{
			ImageHash:    imageHash,
			Description:  strings.TrimSpace(description),
			FullPath:     storedPath,
			ImageType:    schema.ImageTypeEmoji,
			QueryCount:   0,
			IsRegistered: false,
			IsBanned:     false,
			NoFileFlag:   false,
			RecordTime:   now,
			VLMProcessed: true,
		}
		return m.db.WithContext(ctx).Create(created).Error
	}
	updates := map[string]any{
		"vlm_processed": true,
		"no_file_flag":  false,
	}
	if storedPath != "" {
		updates["full_path"] = storedPath
	}
	if text := strings.TrimSpace(description); text != "" {
		updates["description"] = text
	}
	return m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", record.ID).Updates(updates).Error
}

// UpdateEmojiDescription 更新表情包描述
func (m *Manager) UpdateEmojiDescription(ctx context.Context, id uint, description string) error {
	return requireAffected(m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", id).Updates(map[string]any{
		"description": description,
	}))
}

// UpdateEmojiUsage 更新表情包使用记录：query_count+1 并刷新 last_used_time
func (m *Manager) UpdateEmojiUsage(ctx context.Context, id uint) error {
	return m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", id).Updates(map[string]any{
		"query_count":    gorm.Expr("query_count + 1"),
		"last_used_time": time.Now(),
	}).Error
}

// DeleteEmojiRecord 物理删除表情包记录，对齐 delete_emoji(keep_desc=False)
func (m *Manager) DeleteEmojiRecord(ctx context.Context, id uint) error {
	return requireAffected(emojiScope(m.db.WithContext(ctx)).Where("id = ?", id).Delete(&EmojiRecord{}))
}

// MarkEmojiNoFile 保留描述缓存，只标记文件已不存在，对齐 delete_emoji(keep_desc=True)
func (m *Manager) MarkEmojiNoFile(ctx context.Context, id uint) error {
	return requireAffected(m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", id).Updates(map[string]any{
		"no_file_flag": true,
	}))
}

// ClearEmojiNoFileFlag 文件重新出现时清除缺失标记，对齐缓存清理里的 restored_file_flags
func (m *Manager) ClearEmojiNoFileFlag(ctx context.Context, id uint) error {
	return requireAffected(m.db.WithContext(ctx).Model(&EmojiRecord{}).Where("id = ?", id).Updates(map[string]any{
		"no_file_flag": false,
	}))
}

// SerializeStoredPath 把实例内绝对路径序列化为相对实例根目录的存储路径
// 对齐 Python serialize_stored_image_path
func (m *Manager) SerializeStoredPath(fullPath string) (string, error) {
	return imagesystem.SerializeStoredImagePath(m.InstanceRoot(), fullPath)
}

// ResolveStoredPath 把存储路径解析为实例内绝对路径，对齐 resolve_stored_image_path
func (m *Manager) ResolveStoredPath(storedPath string) (string, error) {
	return imagesystem.ResolveStoredImagePath(m.InstanceRoot(), storedPath)
}

// emojiRecordFileAvailable 判断记录对应文件当前是否可用，对齐 _is_available_emoji_record
func (m *Manager) emojiRecordFileAvailable(record *EmojiRecord) bool {
	if record == nil || record.NoFileFlag {
		return false
	}
	return m.EmojiRecordFileExists(record)
}

// EmojiRecordFileExists 判断记录对应文件是否存在，对齐 _resolve_existing_emoji_path
func (m *Manager) EmojiRecordFileExists(record *EmojiRecord) bool {
	if record == nil || strings.TrimSpace(record.FullPath) == "" {
		return false
	}
	path, err := imagesystem.ResolveStoredImagePath(m.InstanceRoot(), record.FullPath)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
