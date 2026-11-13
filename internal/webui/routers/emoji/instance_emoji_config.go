package emoji

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"maibot-go/internal/config/maibot"

	"github.com/pelletier/go-toml/v2"
)

// emojiInstanceSettings MaiBot 实例 [emoji] 段中与 Dashboard 相关的开关
type emojiInstanceSettings struct {
	StealEmoji        bool
	ContentFiltration bool
}

var emojiSettingsCache sync.Map // instanceDir -> emojiInstanceSettings

// emojiSettings 读取实例 bot_config.toml 的 [emoji] 段
//
// 直接解析 TOML 而不走 maibot.LoadFromInstanceDir，避免加载器的版本升级回写副作用；
// 字段缺省取 MaiBot 官方默认值（steal_emoji=true、content_filtration=false）
func (d Deps) emojiSettings() emojiInstanceSettings {
	defaults := emojiInstanceSettings{StealEmoji: true, ContentFiltration: false}
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return defaults
	}
	if cached, ok := emojiSettingsCache.Load(dir); ok {
		settings, _ := cached.(emojiInstanceSettings)
		return settings
	}
	settings := defaults
	raw, err := os.ReadFile(filepath.Join(dir, "config", "bot_config.toml"))
	if err == nil {
		var file maibot.BotConfigFile
		if err := toml.Unmarshal(raw, &file); err == nil {
			settings.StealEmoji = file.Emoji.StealEmoji
			settings.ContentFiltration = file.Emoji.ContentFiltration
			// TOML 未写 steal_emoji 时沿用 MaiBot 默认 true
			var probe map[string]any
			if err := toml.Unmarshal(raw, &probe); err == nil {
				if emojiSection, ok := probe["emoji"].(map[string]any); ok {
					if _, exists := emojiSection["steal_emoji"]; !exists {
						settings.StealEmoji = defaults.StealEmoji
					}
					if _, exists := emojiSection["content_filtration"]; !exists {
						settings.ContentFiltration = defaults.ContentFiltration
					}
				} else {
					settings = defaults
				}
			}
		}
	}
	emojiSettingsCache.Store(dir, settings)
	return settings
}

// stealEmojiEnabled 对齐 global_config.emoji.steal_emoji
func (d Deps) stealEmojiEnabled() bool {
	return d.emojiSettings().StealEmoji
}

// applyEmojiFiltration 把实例内容过滤开关同步到 emojisystem Registry
func (d Deps) applyEmojiFiltration() {
	if emojisystemRegistry := emojiRegistry(); emojisystemRegistry != nil {
		emojisystemRegistry.SetContentFiltration(d.emojiSettings().ContentFiltration)
	}
}
