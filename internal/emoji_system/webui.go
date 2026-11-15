package emojisystem

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/common/database"

	"gorm.io/gorm"
)

// WebUIEmoji Dashboard 表情包视图，字段对齐 routers/emoji/schemas.py EmojiResponse
type WebUIEmoji struct {
	ID           uint
	FullPath     string
	Format       string
	Hash         string
	Description  string
	QueryCount   int
	UsageCount   int
	IsRegistered bool
	IsBanned     bool
	Status       string
	Emotion      string
	RecordTime   float64
	RegisterTime *float64
	LastUsedTime *float64
}

// WebUIEmojiQuery 表情包列表查询
type WebUIEmojiQuery struct {
	Page         int
	PageSize     int
	Search       string
	IsRegistered *bool
	IsBanned     *bool
	Status       string
	Format       string
	SortBy       string
	SortOrder    string
}

// WebUIEmojiPage 分页结果
type WebUIEmojiPage struct {
	Items []WebUIEmoji
	Total int
}

// WebUIEmojiStats 表情包统计
type WebUIEmojiStats struct {
	Total       int
	RecentCount int
	Registered  int
	Banned      int
	Known       int
	Unknown     int
	Formats     map[string]int
	TopUsed     []WebUIEmojiUsage
}

// WebUIEmojiUsage 高频使用表情
type WebUIEmojiUsage struct {
	ID          uint   `json:"id"`
	EmojiHash   string `json:"emoji_hash"`
	Description string `json:"description"`
	UsageCount  int    `json:"usage_count"`
}

// ErrEmojiNotFound 表情包不存在
var ErrEmojiNotFound = errors.New("emojisystem: 表情包不存在")

// ErrEmojiFileMissing 表情包本地文件不存在
var ErrEmojiFileMissing = errors.New("emojisystem: 表情包文件不存在")

// ErrEmojiReviewRejected 表情包未通过注册前内容审核
var ErrEmojiReviewRejected = errors.New("emojisystem: 表情包未通过内容审核，无法注册")

// ErrEmojiStatusInvalid 表情包状态筛选值无效
var ErrEmojiStatusInvalid = errors.New("emojisystem: 无效的表情包状态")

// WebUIService 表情包 Dashboard 查询与写操作，绑定记忆管理器与 sticker 目录
type WebUIService struct {
	registry *Registry
	mem      *memory.Manager
	dir      string
}

// WebUI 返回表情包 Dashboard 服务；记忆管理器未注入时返回 nil
func (r *Registry) WebUI() *WebUIService {
	r.mu.RLock()
	mem := r.mem
	dir := r.stickerDir
	r.mu.RUnlock()
	if mem == nil {
		return nil
	}
	return &WebUIService{registry: r, mem: mem, dir: dir}
}

// StickerDir 返回 sticker 目录
func (r *Registry) StickerDir() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stickerDir
}

// Memory 返回记忆管理器
func (r *Registry) Memory() *memory.Manager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mem
}

func (s *WebUIService) db(ctx context.Context) *gorm.DB {
	return s.mem.WithContext(ctx).GetDB()
}

// FilePath 返回表情文件的完整路径
func (s *WebUIService) FilePath(fileName string) string {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return ""
	}
	if filepath.IsAbs(fileName) {
		return fileName
	}
	return filepath.Join(s.dir, fileName)
}

func emojiStatus(desc string, isRegistered, isBanned bool) string {
	switch {
	case isBanned:
		return "discarded"
	case isRegistered:
		return "adopted"
	case strings.TrimSpace(desc) != "":
		return "known"
	default:
		return "unknown"
	}
}

func emojiFormat(fileName string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))
	if ext == "" {
		return "unknown"
	}
	return ext
}

func (s *WebUIService) toWebUI(row memory.EmojiRecord) WebUIEmoji {
	recordTime := 0.0
	if !row.RecordTime.IsZero() {
		recordTime = float64(row.RecordTime.Unix())
	}
	fullPath := row.FullPath
	item := WebUIEmoji{
		ID:           row.ID,
		FullPath:     fullPath,
		Format:       emojiFormat(fullPath),
		Hash:         row.ImageHash,
		Description:  row.Description,
		QueryCount:   row.QueryCount,
		UsageCount:   row.QueryCount,
		IsRegistered: row.IsRegistered,
		IsBanned:     row.IsBanned,
		Status:       emojiStatus(row.Description, row.IsRegistered, row.IsBanned),
		RecordTime:   recordTime,
	}
	if tags := WebUIEmotionTags(row.Description); len(tags) > 0 {
		item.Emotion = strings.Join(tags, ",")
	}
	if row.RegisterTime != nil {
		registered := float64(row.RegisterTime.Unix())
		item.RegisterTime = &registered
	}
	if row.LastUsedTime != nil {
		lastUsed := float64(row.LastUsedTime.Unix())
		item.LastUsedTime = &lastUsed
	}
	return item
}

// WebUIEmotionTags 对齐 emoji_to_response 的情绪标签口径：
// 按 [,，、;；\s]+ 切分，按原值保序去重
func WebUIEmotionTags(description string) []string {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	parts := webuiEmotionTagSplit.Split(description, -1)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		duplicated := false
		for _, existing := range out {
			if existing == trimmed {
				duplicated = true
				break
			}
		}
		if !duplicated {
			out = append(out, trimmed)
		}
	}
	return out
}

// emojiScopeQuery 限定在表情包记录范围内，对齐 routes.py 的 image_type==ImageType.EMOJI
func emojiScopeQuery(query *gorm.DB) *gorm.DB {
	return query.Where("image_type = ?", schemaImageTypeEmoji)
}

// List 表情包列表，status 非法时返回 ErrEmojiStatusInvalid
func (s *WebUIService) List(ctx context.Context, q WebUIEmojiQuery) (WebUIEmojiPage, error) {
	if q.Status != "" && !IsValidEmojiStatus(q.Status) {
		return WebUIEmojiPage{}, ErrEmojiStatusInvalid
	}
	apply := func(query *gorm.DB) *gorm.DB {
		query = emojiScopeQuery(query)
		if search := strings.TrimSpace(q.Search); search != "" {
			like := "%" + search + "%"
			query = query.Where("description LIKE ? OR image_hash LIKE ?", like, like)
		}
		switch q.Status {
		case "discarded":
			query = query.Where("is_banned = ?", true)
		case "adopted":
			query = query.Where("is_registered = ? AND is_banned = ?", true, false)
		case "known":
			query = query.Where("is_registered = ? AND is_banned = ? AND length(trim(coalesce(description, ''))) > 0", false, false)
		case "unknown":
			query = query.Where("is_registered = ? AND is_banned = ? AND length(trim(coalesce(description, ''))) = 0", false, false)
		default:
			if q.IsRegistered != nil {
				query = query.Where("is_registered = ?", *q.IsRegistered)
			}
			if q.IsBanned != nil {
				query = query.Where("is_banned = ?", *q.IsBanned)
			}
		}
		// 格式筛选按 full_path 后缀，对齐 _apply_emoji_format_filter
		if format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Format), ".")); format != "" {
			if format == "unknown" {
				query = query.Where("full_path NOT LIKE '%.%'")
			} else {
				query = query.Where("lower(full_path) LIKE ?", "%."+format)
			}
		}
		return query
	}
	var total int64
	if err := apply(s.db(ctx).Model(&memory.EmojiRecord{})).Count(&total).Error; err != nil {
		return WebUIEmojiPage{}, err
	}
	// 排序字段映射对齐 sort_field_map，默认 register_time
	column := "register_time"
	switch q.SortBy {
	case "usage_count", "query_count":
		column = "query_count"
	case "record_time":
		column = "record_time"
	case "last_used_time":
		column = "last_used_time"
	}
	direction := "DESC"
	if strings.EqualFold(q.SortOrder, "asc") {
		direction = "ASC"
	}
	page, size := normalizeEmojiPage(q.Page, q.PageSize)
	var rows []memory.EmojiRecord
	if err := apply(s.db(ctx).Model(&memory.EmojiRecord{})).
		Order(column + " " + direction).
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return WebUIEmojiPage{}, err
	}
	items := make([]WebUIEmoji, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toWebUI(row))
	}
	return WebUIEmojiPage{Items: items, Total: int(total)}, nil
}

func normalizeEmojiPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

// Get 读取单个表情包
func (s *WebUIService) Get(ctx context.Context, id uint) (WebUIEmoji, error) {
	var row memory.EmojiRecord
	if err := emojiScopeQuery(s.db(ctx)).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WebUIEmoji{}, ErrEmojiNotFound
		}
		return WebUIEmoji{}, err
	}
	return s.toWebUI(row), nil
}

// Stats 表情包统计
func (s *WebUIService) Stats(ctx context.Context) (WebUIEmojiStats, error) {
	stats := WebUIEmojiStats{Formats: map[string]int{}}
	db := s.db(ctx)
	countWhere := func(where string, args ...any) (int, error) {
		var n int64
		q := emojiScopeQuery(db.Model(&memory.EmojiRecord{}))
		if where != "" {
			q = q.Where(where, args...)
		}
		if err := q.Count(&n).Error; err != nil {
			return 0, err
		}
		return int(n), nil
	}
	var err error
	if stats.Total, err = countWhere(""); err != nil {
		return stats, err
	}
	if stats.RecentCount, err = countWhere("record_time >= ?", time.Now().Add(-7*24*time.Hour)); err != nil {
		return stats, err
	}
	if stats.Registered, err = countWhere("is_registered = ?", true); err != nil {
		return stats, err
	}
	if stats.Banned, err = countWhere("is_banned = ?", true); err != nil {
		return stats, err
	}
	if stats.Known, err = countWhere("is_registered = ? AND is_banned = ? AND length(trim(coalesce(description, ''))) > 0", false, false); err != nil {
		return stats, err
	}
	if stats.Unknown, err = countWhere("is_registered = ? AND is_banned = ? AND length(trim(coalesce(description, ''))) = 0", false, false); err != nil {
		return stats, err
	}
	var paths []string
	if err := emojiScopeQuery(db.Model(&memory.EmojiRecord{})).Pluck("full_path", &paths).Error; err != nil {
		return stats, err
	}
	for _, path := range paths {
		stats.Formats[emojiFormat(path)]++
	}
	var top []memory.EmojiRecord
	if err := emojiScopeQuery(db.Model(&memory.EmojiRecord{})).Order("query_count DESC").Limit(10).Find(&top).Error; err != nil {
		return stats, err
	}
	stats.TopUsed = make([]WebUIEmojiUsage, 0, len(top))
	for _, row := range top {
		stats.TopUsed = append(stats.TopUsed, WebUIEmojiUsage{
			ID:          row.ID,
			EmojiHash:   row.ImageHash,
			Description: row.Description,
			UsageCount:  row.QueryCount,
		})
	}
	return stats, nil
}

var webuiEmotionTagSplit = regexp.MustCompile("[,，、;；\\s]+")

// NormalizeDescription 对齐 _normalize_emoji_description
//
// 与标签归一化不同：WebUI 上传的情绪允许用空格分隔，且不去重
func NormalizeDescription(description, emotion string) string {
	desc := strings.TrimSpace(description)
	if desc != "" {
		return desc
	}
	emotion = strings.TrimSpace(emotion)
	if emotion == "" {
		return ""
	}
	parts := webuiEmotionTagSplit.Split(emotion, -1)
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			tags = append(tags, tag)
		}
	}
	return strings.Join(tags, ",")
}

// SortByTopUsage 稳定排序 top_used 列表（保持 Python 口径：使用次数倒序）
func SortByTopUsage(items []WebUIEmojiUsage) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].UsageCount > items[j].UsageCount })
}

func wrapEmojiFileError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrEmojiFileMissing, err)
}

// emojiStatusValues WebUI 表情包状态枚举，对齐 EmojiStatus Literal
var emojiStatusValues = map[string]struct{}{"known": {}, "unknown": {}, "adopted": {}, "discarded": {}}

// IsValidEmojiStatus 判断状态筛选值是否合法
func IsValidEmojiStatus(status string) bool {
	_, ok := emojiStatusValues[status]
	return ok
}
