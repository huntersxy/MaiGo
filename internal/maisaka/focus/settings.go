package focus

import (
	"encoding/json"

	"maibot-go/internal/config/maibot"
)

// Settings Focus 模式配置快照，对齐 experimental 段
type Settings struct {
	FocusMode             bool
	FocusOnPrivate        bool
	FocusChatWhitelist    []maibot.TargetItem
	FocusGroups           []maibot.ChatStreamGroup
	PrivateReplyPausedAll bool
}

// SettingsFromExperimental 从 MaiBot bot_config.experimental 构造
func SettingsFromExperimental(exp maibot.ExperimentalConfig, chat maibot.ChatConfig) Settings {
	return Settings{
		FocusMode:             exp.FocusMode,
		FocusOnPrivate:        exp.FocusOnPrivate,
		FocusChatWhitelist:    ParseTargetItems(exp.FocusChatWhitelist),
		FocusGroups:           ParseFocusGroups(exp.FocusGroups),
		PrivateReplyPausedAll: chat.ReplyTiming.PrivateReplyPaused,
	}
}

// ParseTargetItems 解析 TOML 中 []any 形式的 targets / whitelist
func ParseTargetItems(raw []any) []maibot.TargetItem {
	if len(raw) == 0 {
		return nil
	}
	out := make([]maibot.TargetItem, 0, len(raw))
	for _, item := range raw {
		if t, ok := decodeTargetItem(item); ok {
			out = append(out, t)
		}
	}
	return out
}

// ParseFocusGroups 解析 focus_groups
func ParseFocusGroups(raw []any) []maibot.ChatStreamGroup {
	if len(raw) == 0 {
		return nil
	}
	out := make([]maibot.ChatStreamGroup, 0, len(raw))
	for _, item := range raw {
		group, ok := decodeFocusGroup(item)
		if ok {
			out = append(out, group)
		}
	}
	return out
}

func decodeTargetItem(item any) (maibot.TargetItem, bool) {
	switch v := item.(type) {
	case maibot.TargetItem:
		return v, true
	case map[string]any:
		return maibot.TargetItem{
			Platform: stringField(v, "platform"),
			ItemId:   stringField(v, "item_id"),
			RuleType: stringField(v, "rule_type"),
		}, true
	default:
		b, err := json.Marshal(item)
		if err != nil {
			return maibot.TargetItem{}, false
		}
		var t maibot.TargetItem
		if err := json.Unmarshal(b, &t); err != nil {
			return maibot.TargetItem{}, false
		}
		return t, t.Platform != "" && t.ItemId != "" && t.RuleType != ""
	}
}

func decodeFocusGroup(item any) (maibot.ChatStreamGroup, bool) {
	switch v := item.(type) {
	case maibot.ChatStreamGroup:
		return v, true
	case map[string]any:
		rawTargets, _ := v["targets"].([]any)
		return maibot.ChatStreamGroup{Targets: ParseTargetItems(rawTargets)}, true
	default:
		b, err := json.Marshal(item)
		if err != nil {
			return maibot.ChatStreamGroup{}, false
		}
		var g maibot.ChatStreamGroup
		if err := json.Unmarshal(b, &g); err != nil {
			return maibot.ChatStreamGroup{}, false
		}
		return g, len(g.Targets) > 0
	}
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
