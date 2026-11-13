package maibot

import (
	"fmt"
	"strings"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
)

// PersonalitySwitchResult 人设切换结果（对齐 MaiBot personality_service.switch_profile）
type PersonalitySwitchResult struct {
	Changed bool
	Current PersonalityProfile
}

var personalitySwitchMu sync.Mutex

// CurrentPersonalityProfile 返回当前生效的人设字段快照
func CurrentPersonalityProfile(personality PersonalityConfig, replyStyle ChatReplyStyleConfig) PersonalityProfile {
	name := strings.TrimSpace(personality.SelectedProfile)
	if name == "" {
		name = "当前人设"
	}
	return PersonalityProfile{
		Name:                name,
		Personality:         personality.Personality,
		BehaviorStyle:       personality.BehaviorStyle,
		ReplyStyle:          personality.ReplyStyle,
		GroupChatPrompt:     replyStyle.GroupChatPrompt,
		PrivateChatPrompts:  replyStyle.PrivateChatPrompts,
		MultipleReplyStyle:  append([]string(nil), personality.MultipleReplyStyle...),
		MultipleProbability: personality.MultipleProbability,
	}
}

func findPersonalityProfile(profiles []PersonalityProfile, name string) int {
	for i := range profiles {
		if profiles[i].Name == name {
			return i
		}
	}
	return -1
}

func uniqueBackupProfileName(profiles []PersonalityProfile, preferred string) string {
	existing := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		existing[profile.Name] = struct{}{}
	}
	if _, ok := existing[preferred]; !ok {
		return preferred
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s %d", preferred, i)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

func setPersonalitySectionField(lines []string, key string, value any) []string {
	return setTOMLSectionScalar(lines, "personality", key, encodeTOMLValue(value))
}

// PersistPersonalitySwitch 按 Python persist_switch 语义保留注释地写入 TOML
func PersistPersonalitySwitch(path, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("人设名称不能为空")
	}
	raw, err := readTOMLText(path)
	if err != nil {
		return false, fmt.Errorf("读取人设配置失败: %w", err)
	}
	var doc botConfigDocument
	if err := toml.Unmarshal([]byte(raw), &doc); err != nil {
		return false, fmt.Errorf("解析人设配置失败: %w", err)
	}
	personality := doc.Personality
	for index, profile := range personality.Profiles {
		if profile.Name == name {
			return persistPersonalitySwitch(&doc, path, raw, index, name)
		}
	}
	return false, fmt.Errorf("人设不存在: %s", name)
}

func persistPersonalitySwitch(doc *botConfigDocument, path, raw string, targetIndex int, name string) (bool, error) {
	personality := &doc.Personality
	if strings.TrimSpace(personality.SelectedProfile) == name {
		return false, nil
	}
	profiles := append([]PersonalityProfile(nil), personality.Profiles...)
	current := CurrentPersonalityProfile(*personality, doc.Chat.ReplyStyle)
	previous := strings.TrimSpace(personality.SelectedProfile)
	if previous != "" {
		if index := findPersonalityProfile(profiles, previous); index >= 0 {
			profiles[index] = current
		} else {
			backup := current
			backup.Name = uniqueBackupProfileName(profiles, "切换前人设")
			profiles = append(profiles, backup)
		}
	} else {
		backup := current
		backup.Name = uniqueBackupProfileName(profiles, "切换前人设")
		profiles = append(profiles, backup)
	}
	if targetIndex < 0 || targetIndex >= len(personality.Profiles) {
		return false, fmt.Errorf("人设不存在: %s", name)
	}
	target := personality.Profiles[targetIndex]
	lines := splitTOMLText(raw)
	for _, field := range []struct {
		key   string
		value any
	}{
		{"personality", target.Personality},
		{"behavior_style", target.BehaviorStyle},
		{"reply_style", target.ReplyStyle},
		{"multiple_reply_style", target.MultipleReplyStyle},
		{"multiple_probability", target.MultipleProbability},
	} {
		lines = setPersonalitySectionField(lines, field.key, field.value)
	}
	lines = setTOMLSectionScalar(lines, "chat.reply_style", "group_chat_prompt", encodeTOMLValue(target.GroupChatPrompt))
	lines = setTOMLSectionScalar(lines, "chat.reply_style", "private_chat_prompts", encodeTOMLValue(target.PrivateChatPrompts))
	lines = setTOMLPersonalityProfiles(lines, profiles)
	lines = setPersonalitySectionField(lines, "selected_profile", name)
	if err := writeTOMLTextAtomic(path, joinTOMLText(lines)); err != nil {
		return false, err
	}
	return true, nil
}

// SwitchPersonality 保存人设切换并触发热重载，失败时返回真实错误
func SwitchPersonality(holder *InstanceHolder, name string) (PersonalitySwitchResult, error) {
	if holder == nil {
		return PersonalitySwitchResult{}, fmt.Errorf("实例配置尚未初始化")
	}
	personalitySwitchMu.Lock()
	defer personalitySwitchMu.Unlock()
	changed, err := PersistPersonalitySwitch(holder.botConfigPath(), name)
	if err != nil {
		return PersonalitySwitchResult{}, err
	}
	if changed {
		if err := holder.ReloadFromDisk([]ReloadScope{ReloadScopeBot}); err != nil {
			return PersonalitySwitchResult{}, fmt.Errorf("人设配置已保存，但运行时热重载失败，请检查日志并重试: %w", err)
		}
	}
	cfg := holder.Config()
	if cfg == nil {
		return PersonalitySwitchResult{}, fmt.Errorf("实例配置尚未初始化")
	}
	current := CurrentPersonalityProfile(cfg.Bot.Personality, cfg.Bot.Chat.ReplyStyle)
	if strings.TrimSpace(cfg.Bot.Personality.SelectedProfile) != strings.TrimSpace(name) {
		return PersonalitySwitchResult{}, fmt.Errorf("人设切换校验失败: selected_profile=%q", cfg.Bot.Personality.SelectedProfile)
	}
	return PersonalitySwitchResult{Changed: changed, Current: current}, nil
}
