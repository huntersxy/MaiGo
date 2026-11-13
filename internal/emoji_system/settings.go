package emojisystem

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/config/maibot"

	"github.com/pelletier/go-toml/v2"
)

// EmojiCacheCleanupSettings 对齐 MaiBot emoji.cache_cleanup 配置
type EmojiCacheCleanupSettings struct {
	Enabled                   bool
	CheckIntervalHours        float64
	EmojiFileRetentionDays    int
	NoFileRecordRetentionDays int
}

// EmojiSettings 对齐 MaiBot global_config.emoji 中运行期需要的字段
type EmojiSettings struct {
	StealEmoji bool
	// EmojiSendNum 对齐 emoji.emoji_send_num：单次候选数量，对应 Python Field(default=25, ge=1, le=64)
	EmojiSendNum      int
	MaxRegNum         int
	DoReplace         bool
	CheckInterval     time.Duration
	MaxEmojiSizeBytes int64
	ContentFiltration bool
	CacheCleanup      EmojiCacheCleanupSettings
}

// DefaultEmojiSettings 返回 MaiBot official_configs 的 emoji 段默认值
func DefaultEmojiSettings() EmojiSettings {
	return EmojiSettings{
		StealEmoji:        true,
		EmojiSendNum:      25,
		MaxRegNum:         64,
		DoReplace:         true,
		CheckInterval:     10 * time.Minute,
		MaxEmojiSizeBytes: 5 * 1024 * 1024,
		ContentFiltration: false,
		CacheCleanup: EmojiCacheCleanupSettings{
			Enabled:                   true,
			CheckIntervalHours:        6,
			EmojiFileRetentionDays:    30,
			NoFileRecordRetentionDays: 30,
		},
	}
}

// Normalize 修正非法配置，保持与 MaiBot 一致的兜底范围
func (s EmojiSettings) Normalize() EmojiSettings {
	def := DefaultEmojiSettings()
	if s.MaxRegNum <= 0 {
		s.MaxRegNum = def.MaxRegNum
	}
	if s.EmojiSendNum <= 0 {
		s.EmojiSendNum = def.EmojiSendNum
	}
	if s.EmojiSendNum > 64 {
		s.EmojiSendNum = 64
	}
	if s.CheckInterval <= 0 {
		s.CheckInterval = def.CheckInterval
	}
	if s.CacheCleanup.CheckIntervalHours <= 0 {
		s.CacheCleanup.CheckIntervalHours = def.CacheCleanup.CheckIntervalHours
	}
	if s.CacheCleanup.CheckIntervalHours < 1.0/60.0 {
		s.CacheCleanup.CheckIntervalHours = 1.0 / 60.0
	}
	if s.CacheCleanup.EmojiFileRetentionDays <= 0 {
		s.CacheCleanup.EmojiFileRetentionDays = def.CacheCleanup.EmojiFileRetentionDays
	}
	if s.CacheCleanup.NoFileRecordRetentionDays <= 0 {
		s.CacheCleanup.NoFileRecordRetentionDays = def.CacheCleanup.NoFileRecordRetentionDays
	}
	return s
}

// CacheCleanupInterval 清理检查间隔，最短 60 秒
func (s EmojiCacheCleanupSettings) CacheCleanupInterval() time.Duration {
	seconds := s.CheckIntervalHours * 3600
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds * float64(time.Second))
}

var (
	settingsCacheMu   sync.Mutex
	settingsCacheKey  string
	settingsCacheMod  time.Time
	settingsCacheData EmojiSettings
)

// LoadEmojiSettings 从 MAIBOT_INSTANCE_DIR/config/bot_config.toml 读取 [emoji]
//
// 直接解析 TOML 而不走 maibot.LoadFromInstanceDir，避免升级回写副作用；
// 读取失败或缺少 [emoji] 段时返回 MaiBot 官方默认值
func LoadEmojiSettings() EmojiSettings {
	dir := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if dir == "" {
		return DefaultEmojiSettings()
	}
	path := filepath.Join(dir, "config", "bot_config.toml")
	info, err := os.Stat(path)
	if err != nil {
		return DefaultEmojiSettings()
	}
	settingsCacheMu.Lock()
	defer settingsCacheMu.Unlock()
	if settingsCacheKey == path && settingsCacheMod.Equal(info.ModTime()) {
		return settingsCacheData
	}
	data := loadEmojiSettingsFile(path)
	settingsCacheKey = path
	settingsCacheMod = info.ModTime()
	settingsCacheData = data
	return data
}

func loadEmojiSettingsFile(path string) EmojiSettings {
	settings := DefaultEmojiSettings()
	raw, err := os.ReadFile(path)
	if err != nil {
		return settings
	}
	var file maibot.BotConfigFile
	if err := toml.Unmarshal(raw, &file); err != nil {
		return settings
	}
	var probe map[string]any
	if err := toml.Unmarshal(raw, &probe); err != nil {
		return settings
	}
	section, ok := probe["emoji"].(map[string]any)
	if !ok {
		return settings
	}
	settings.StealEmoji = probeBool(section, "steal_emoji", settings.StealEmoji)
	settings.DoReplace = probeBool(section, "do_replace", settings.DoReplace)
	settings.ContentFiltration = probeBool(section, "content_filtration", settings.ContentFiltration)
	settings.MaxRegNum = probeInt(section, "max_reg_num", settings.MaxRegNum)
	settings.EmojiSendNum = probeInt(section, "emoji_send_num", settings.EmojiSendNum)
	if minutes := probeInt(section, "check_interval", int(settings.CheckInterval/time.Minute)); minutes > 0 {
		settings.CheckInterval = time.Duration(minutes) * time.Minute
	}
	if _, exists := section["max_emoji_size_mb"]; exists {
		mb := file.Emoji.MaxEmojiSizeMb
		if mb <= 0 {
			settings.MaxEmojiSizeBytes = 0
		} else {
			settings.MaxEmojiSizeBytes = int64(mb * 1024 * 1024)
		}
	}
	if cleanup, ok := section["cache_cleanup"].(map[string]any); ok {
		settings.CacheCleanup.Enabled = probeBool(cleanup, "enabled", settings.CacheCleanup.Enabled)
		settings.CacheCleanup.CheckIntervalHours = probeFloat(cleanup, "check_interval_hours", settings.CacheCleanup.CheckIntervalHours)
		settings.CacheCleanup.EmojiFileRetentionDays = probeInt(cleanup, "emoji_file_retention_days", settings.CacheCleanup.EmojiFileRetentionDays)
		settings.CacheCleanup.NoFileRecordRetentionDays = probeInt(cleanup, "no_file_record_retention_days", settings.CacheCleanup.NoFileRecordRetentionDays)
	}
	return settings.Normalize()
}

func probeBool(section map[string]any, key string, fallback bool) bool {
	if value, ok := section[key].(bool); ok {
		return value
	}
	return fallback
}

func probeInt(section map[string]any, key string, fallback int) int {
	switch value := section[key].(type) {
	case int64:
		return int(value)
	case int:
		return value
	case float64:
		return int(value)
	}
	return fallback
}

func probeFloat(section map[string]any, key string, fallback float64) float64 {
	switch value := section[key].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	}
	return fallback
}

// SetSettingsProvider 注入热重载配置来源，返回 nil 时回退文件读取
func (r *Registry) SetSettingsProvider(provider func() EmojiSettings) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.settingsProvider = provider
	r.mu.Unlock()
	r.notifySettingsChanged()
}

// Settings 返回当前表情包配置
func (r *Registry) Settings() EmojiSettings {
	if r == nil {
		return LoadEmojiSettings()
	}
	r.mu.RLock()
	provider := r.settingsProvider
	override := r.settings
	hasOverride := r.settingsSet
	r.mu.RUnlock()
	switch {
	case provider != nil:
		return provider().Normalize()
	case hasOverride:
		return override.Normalize()
	default:
		return LoadEmojiSettings()
	}
}

// SetSettings 设置进程内覆盖配置，主要供测试与无实例目录场景使用
func (r *Registry) SetSettings(settings EmojiSettings) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.settings = settings.Normalize()
	r.settingsSet = true
	r.mu.Unlock()
	r.notifySettingsChanged()
}

// settingsWake 返回配置热重载唤醒通道，零值 Registry 也可安全使用
func (r *Registry) settingsWake() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settingsChanged == nil {
		r.settingsChanged = make(chan struct{}, 1)
	}
	return r.settingsChanged
}

// notifySettingsChanged 非阻塞唤醒维护循环，对齐 Python 热重载事件
func (r *Registry) notifySettingsChanged() {
	select {
	case r.settingsWake() <- struct{}{}:
	default:
	}
}
