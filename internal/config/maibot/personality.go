package maibot

import "strings"

// PersonalityPersonaCard 返回 bot_config [personality] 中当前生效的人格设定文本
//
// MaiBot 的 profiles/selected_profile 只是人设库与它正在编辑的条目，运行时人格始终取平铺的 personality 字段
// （见 src/config/personality_service.py current_profile）；去空白后为空表示实例没有配置人格
func PersonalityPersonaCard(bot BotConfigFile) (string, bool) {
	card := strings.TrimSpace(bot.Personality.Personality)
	if card == "" {
		return "", false
	}
	return card, true
}

// PersonaCardFor 返回内置人格要使用的卡片文本：实例 TOML 声明人格时以 TOML 为准，否则回退代码内置文本
func PersonaCardFor(holder *InstanceHolder, fallback string) string {
	if holder == nil {
		return fallback
	}
	cfg := holder.Config()
	if cfg == nil {
		return fallback
	}
	if card, ok := PersonalityPersonaCard(cfg.Bot); ok {
		return card
	}
	return fallback
}
