package host

import (
	"strings"

	maibotcfg "MaiGo/internal/config/maibot"
)

// PersonalityConfigPort 人设配置端口，对齐 MaiBot personality_service
//
// 由实例 TOML 配置实现；personality.switch 必须通过该端口保留注释地写回配置并热重载
type PersonalityConfigPort interface {
	PersonalityProfiles() []map[string]any
	CurrentPersonalityProfile() map[string]any
	SwitchPersonalityProfile(name string) (map[string]any, error)
}

func personalityProfilePayload(profile maibotcfg.PersonalityProfile) map[string]any {
	return map[string]any{
		"name":                 profile.Name,
		"personality":          profile.Personality,
		"behavior_style":       profile.BehaviorStyle,
		"reply_style":          profile.ReplyStyle,
		"group_chat_prompt":    profile.GroupChatPrompt,
		"private_chat_prompts": profile.PrivateChatPrompts,
		"multiple_reply_style": append([]string(nil), profile.MultipleReplyStyle...),
		"multiple_probability": profile.MultipleProbability,
	}
}

// PersonalityProfiles 返回配置中声明的全部人设
func (i InstanceBotConfig) PersonalityProfiles() []map[string]any {
	if i.Holder == nil {
		return nil
	}
	cfg := i.Holder.Config()
	if cfg == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(cfg.Bot.Personality.Profiles))
	for _, profile := range cfg.Bot.Personality.Profiles {
		out = append(out, personalityProfilePayload(profile))
	}
	return out
}

// CurrentPersonalityProfile 返回当前生效人设字段，对齐 current_profile
func (i InstanceBotConfig) CurrentPersonalityProfile() map[string]any {
	if i.Holder == nil {
		return nil
	}
	cfg := i.Holder.Config()
	if cfg == nil {
		return nil
	}
	profile := maibotcfg.CurrentPersonalityProfile(cfg.Bot.Personality, cfg.Bot.Chat.ReplyStyle)
	return map[string]any{
		"name":                 profile.Name,
		"personality":          profile.Personality,
		"behavior_style":       profile.BehaviorStyle,
		"reply_style":          profile.ReplyStyle,
		"group_chat_prompt":    profile.GroupChatPrompt,
		"private_chat_prompts": profile.PrivateChatPrompts,
		"multiple_reply_style": append([]string(nil), profile.MultipleReplyStyle...),
		"multiple_probability": profile.MultipleProbability,
	}
}

// SwitchPersonalityProfile 保注释写回并触发热重载
func (i InstanceBotConfig) SwitchPersonalityProfile(name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errPersonalityNameRequired()
	}
	result, err := maibotcfg.SwitchPersonality(i.Holder, name)
	if err != nil {
		return nil, err
	}
	cfg := i.Holder.Config()
	if cfg == nil {
		return nil, errPersonalityConfigUnavailable()
	}
	return map[string]any{
		"changed": result.Changed,
		"current": i.CurrentPersonalityProfile(),
	}, nil
}

func errPersonalityNameRequired() error { return personalityConfigError("人设名称不能为空") }
func errPersonalityConfigUnavailable() error {
	return personalityConfigError("实例配置尚未初始化")
}

type personalityConfigError string

func (e personalityConfigError) Error() string { return string(e) }
