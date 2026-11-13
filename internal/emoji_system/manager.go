package emojisystem

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	imagesystem "maibot-go/internal/chat/image_system"
	"maibot-go/internal/common/database"

	"go.uber.org/zap"
)

// Emoji 对齐 Python MaiEmoji（src/common/data_models/image_data_model.py:168-216）
//
// Python 字段：full_path / file_name / file_hash / description / emotion /
// query_count / register_time / last_used_time。
// file_name 在 Python 里由 full_path 派生，Go 同样在构造时算好，不做独立存储。
type Emoji struct {
	ID           uint       `json:"id"`
	FileHash     string     `json:"file_hash"`
	FullPath     string     `json:"full_path"`
	FileName     string     `json:"file_name"`
	Description  string     `json:"description"`
	Emotions     []string   `json:"emotion"`
	QueryCount   int        `json:"query_count"`
	RegisterTime *time.Time `json:"register_time,omitempty"`
	LastUsedTime *time.Time `json:"last_used_time,omitempty"`
	IsRegistered bool       `json:"is_registered"`
}

// emojiFromRecord 对齐 MaiEmoji.from_db_instance
//
// 情绪标签用 MaiEmoji.from_db_instance 的去重口径（只换 ，。、，按原值去重），
// 与 _normalize_emoji_tag_text 的小写归一不同，这里是展示语义。
func emojiFromRecord(record memory.EmojiRecord) Emoji {
	description := record.Description
	return Emoji{
		ID:           record.ID,
		FileHash:     record.ImageHash,
		FullPath:     record.FullPath,
		FileName:     filepath.Base(filepath.FromSlash(record.FullPath)),
		Description:  description,
		Emotions:     EmojiTagsFromDescription(description),
		QueryCount:   record.QueryCount,
		RegisterTime: record.RegisterTime,
		LastUsedTime: record.LastUsedTime,
		IsRegistered: record.IsRegistered,
	}
}

// Manager 表情包选择与管理，内存池 + PG
type Manager struct {
	mu         sync.RWMutex
	saveMu     sync.Mutex
	mem        *memory.Manager
	cached     []Emoji
	stickerDir string
}

// NewManager 创建 Manager
func NewManager(mem *memory.Manager) *Manager {
	return &Manager{mem: mem}
}

// NewManagerWithPool 用给定表情池构造 Manager，mem 可为 nil
// 供测试与离线快照场景使用；表情文件仍由 SetStickerDir 指定的目录提供
func NewManagerWithPool(mem *memory.Manager, items []Emoji) *Manager {
	return &Manager{mem: mem, cached: append([]Emoji(nil), items...)}
}

// SetStickerDir 记录表情目录，用于在 FullPath 缺失时兜底定位文件
func (m *Manager) SetStickerDir(dir string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stickerDir = stringsTrim(dir)
	m.mu.Unlock()
}

func (m *Manager) stickerDirectory() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stickerDir
}

// instanceRoot 返回实例根目录，full_path 的相对路径按它解析
func (m *Manager) instanceRoot() string {
	if m == nil || m.mem == nil {
		return ""
	}
	return m.mem.InstanceRoot()
}

// ReloadFromDB 对齐 load_emojis_from_db：加载全部已注册未封禁记录，
// 文件缺失或 no_file_flag 的已注册记录直接删除
func (m *Manager) ReloadFromDB(ctx context.Context) error {
	if m.mem == nil {
		return errMemoryUnavailable
	}
	records, err := m.mem.ListRegisteredEmojiRecords(ctx)
	if err != nil {
		return err
	}
	items := make([]Emoji, 0, len(records))
	for _, record := range records {
		if !m.mem.EmojiRecordFileExists(&record) {
			zap.L().Warn("已注册表情包缺少实际文件，删除破损注册记录",
				zap.Uint("id", record.ID), zap.String("path", record.FullPath))
			if err := m.mem.DeleteEmojiRecord(ctx, record.ID); err != nil {
				zap.L().Warn("删除破损表情包记录失败", zap.Uint("id", record.ID), zap.Error(err))
			}
			continue
		}
		items = append(items, emojiFromRecord(record))
	}
	m.mu.Lock()
	m.cached = items
	m.mu.Unlock()
	return nil
}

func (m *Manager) pool() []Emoji {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Emoji, len(m.cached))
	copy(out, m.cached)
	return out
}

// InPool 哈希是否已在可发送内存池
func (m *Manager) InPool(hash string) bool {
	hash = stringsTrim(hash)
	if hash == "" {
		return false
	}
	for _, item := range m.pool() {
		if item.FileHash == hash {
			return true
		}
	}
	return false
}

// Count 当前内存池数量
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.cached)
}

// GetByHash 内存池优先，未命中则查库
func (m *Manager) GetByHash(ctx context.Context, hash string) (*Emoji, error) {
	hash = stringsTrim(hash)
	if hash == "" {
		return nil, errInvalidHash
	}
	for _, item := range m.pool() {
		if item.FileHash == hash {
			copy := item
			return &copy, nil
		}
	}
	if m.mem == nil {
		return nil, errMemoryUnavailable
	}
	record, err := m.mem.GetEmojiRecordByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errEmojiNotFound
	}
	item := emojiFromRecord(*record)
	return &item, nil
}

// EmojiFilePath 返回表情文件的真实路径，对齐 resolve_stored_image_path
func (m *Manager) EmojiFilePath(emoji *Emoji) string {
	if emoji == nil {
		return ""
	}
	if stored := stringsTrim(emoji.FullPath); stored != "" {
		if resolution, err := imagesystem.ResolveStoredImagePath(m.instanceRoot(), stored); err == nil {
			return resolution
		}
	}
	return stickerFilePath(m.stickerDirectory(), emoji.FileName)
}

// ReadEmojiBytes 读取表情文件字节，供拼图等需要原始数据的场景使用
func (m *Manager) ReadEmojiBytes(emoji *Emoji) ([]byte, error) {
	path := m.EmojiFilePath(emoji)
	if path == "" {
		return nil, fmt.Errorf("表情包缺少文件路径")
	}
	return os.ReadFile(path)
}

// GetByID 按主键读取
func (m *Manager) GetByID(id uint) (*Emoji, error) {
	if m.mem == nil {
		return nil, errMemoryUnavailable
	}
	record, err := m.mem.GetEmojiRecordByID(context.Background(), id)
	if err != nil {
		return nil, err
	}
	item := emojiFromRecord(*record)
	return &item, nil
}

// RecordUsage 对齐 update_emoji_usage
func (m *Manager) RecordUsage(id uint) error {
	if m.mem == nil {
		return errMemoryUnavailable
	}
	if err := m.mem.UpdateEmojiUsage(context.Background(), id); err != nil {
		return err
	}
	now := time.Now()
	m.mu.Lock()
	for i := range m.cached {
		if m.cached[i].ID == id {
			m.cached[i].QueryCount++
			m.cached[i].LastUsedTime = &now
			break
		}
	}
	m.mu.Unlock()
	return nil
}

// RecordUsageByHash 对齐 update_emoji_usage_by_hash：按 file_hash 记录一次真实发送
func (m *Manager) RecordUsageByHash(ctx context.Context, hash string) (bool, error) {
	if m.mem == nil {
		return false, errMemoryUnavailable
	}
	hash = stringsTrim(hash)
	if hash == "" {
		return false, errInvalidHash
	}
	record, err := m.mem.GetEmojiRecordByHash(ctx, hash)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, nil
	}
	if err := m.mem.UpdateEmojiUsage(ctx, record.ID); err != nil {
		return false, err
	}
	now := time.Now()
	m.mu.Lock()
	for i := range m.cached {
		if m.cached[i].FileHash == hash {
			m.cached[i].QueryCount++
			m.cached[i].LastUsedTime = &now
			break
		}
	}
	m.mu.Unlock()
	return true, nil
}

type scoredEmoji struct {
	emoji   Emoji
	score   float64
	matched string
}

// SelectForEmotion 对齐 get_emoji_for_emotion：在前 10 相似度中随机取一
func (m *Manager) SelectForEmotion(emotionLabel string) (*Emoji, string, error) {
	pool := m.pool()
	if len(pool) == 0 {
		return nil, "", errEmptyPool
	}
	label := stringsTrim(emotionLabel)
	if label == "" {
		return nil, "", errEmptyEmotion
	}

	var scored []scoredEmoji
	for _, item := range pool {
		tags := item.Emotions
		if len(tags) == 0 {
			continue
		}
		best := 0.0
		matched := ""
		for _, tag := range tags {
			s := emotionSimilarity(label, tag)
			if s > best {
				best = s
				matched = tag
			}
		}
		scored = append(scored, scoredEmoji{emoji: item, score: best, matched: matched})
	}
	if len(scored) == 0 {
		return nil, "", errNoMatch
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	topN := scored
	if len(topN) > 10 {
		topN = topN[:10]
	}
	pick := topN[rand.Intn(len(topN))]
	matchedTag := pick.matched
	if matchedTag == "" && len(pick.emoji.Emotions) > 0 {
		matchedTag = pick.emoji.Emotions[0]
	}
	copy := pick.emoji
	return &copy, matchedTag, nil
}

func stringsTrim(s string) string {
	return strings.TrimSpace(s)
}

// SampleCandidates 对齐 show_emoji_list / send_emoji 候选采样
func (m *Manager) SampleCandidates(limit int) []Emoji {
	pool := m.pool()
	if len(pool) == 0 || limit <= 0 {
		return nil
	}
	if limit >= len(pool) {
		return append([]Emoji(nil), pool...)
	}
	indices := rand.Perm(len(pool))[:limit]
	out := make([]Emoji, 0, limit)
	for _, idx := range indices {
		out = append(out, pool[idx])
	}
	return out
}

// Search 关键词分页，桥接 memory.SearchEmojiPage
func (m *Manager) Search(ctx context.Context, keyword, cursor string, limit int) (memory.EmojiPage, error) {
	if m.mem == nil {
		return memory.EmojiPage{}, errMemoryUnavailable
	}
	page, err := m.mem.SearchEmojiPage(ctx, keyword, cursor, limit)
	if err != nil {
		return page, err
	}
	items := page.Items[:0]
	for _, item := range page.Items {
		if item.IsRegistered && !item.IsBanned {
			items = append(items, item)
		}
	}
	page.Items = items
	return page, nil
}

// SerializeForHook 对齐 _serialize_emoji_for_hook：full_path 为实例内绝对路径
func (m *Manager) SerializeForHook(emoji *Emoji) map[string]any {
	if emoji == nil {
		return nil
	}
	description := emoji.Description
	return map[string]any{
		"file_hash":   stringsTrim(emoji.FileHash),
		"file_name":   emoji.FileName,
		"full_path":   m.EmojiFilePath(emoji),
		"description": description,
		"emotions":    append([]string(nil), NormalizeEmotionTags(description)...),
		"query_count": emoji.QueryCount,
	}
}

var _ = errors.Is
