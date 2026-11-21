package prompt

// 内置回复模板按场景命名，旧名称继续用于 custom_prompts 存储与 API 兼容。

var promptNameAliases = map[string]string{
	"maisaka_chat":                    "planner_default_group",
	"maisaka_chat_passive":            "planner_passive_group",
	"maisaka_chat_private":            "planner_default_private",
	"maisaka_chat_private_passive":    "planner_passive_private",
	"maisaka_chat_focus":              "planner_focus",
	"reply_creative_group":            "reply_group",
	"reply_creative_private":          "reply_private",
	"reply_creative_private_followup": "reply_private_followup",
	"retro_replyer":                   "reply_group",
	"retro_replyer_light":             "reply_group",
	"retro_private_replyer":           "reply_private",
	"retro_private_replyer_self":      "reply_private_followup",
}

var customPromptStorageNames = map[string]string{
	"planner_default_group":   "maisaka_chat",
	"planner_passive_group":   "maisaka_chat_passive",
	"planner_default_private": "maisaka_chat_private",
	"planner_passive_private": "maisaka_chat_private_passive",
	"planner_focus":           "maisaka_chat_focus",
	"reply_group":             "retro_replyer",
	"reply_private":           "retro_private_replyer",
	"reply_private_followup":  "retro_private_replyer_self",
}

// CanonicalPromptName 把历史名称映射为当前内置模板名
func CanonicalPromptName(name string) string {
	if mapped, ok := promptNameAliases[name]; ok {
		return mapped
	}
	return name
}

// CustomPromptStorageName 返回 custom_prompts 中沿用旧版本的存储名
func CustomPromptStorageName(name string) string {
	if mapped, ok := customPromptStorageNames[name]; ok {
		return mapped
	}
	return name
}
