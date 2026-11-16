package maibot

import "strings"

// registerDefaultUpgrades 注册与 MaiBot config_upgrade_hooks.py 对齐的版本锚点
//
// Python 侧按 target_version 在 (old_ver, new_ver] 区间依次执行 migrate；
// Go 侧用 from→to 链逐步抬升 inner.version 至 BotConfigTargetVersion / ModelConfigTargetVersion。
func registerDefaultUpgrades(r *UpgradeRegistry) {
	// bot_config：版本锚点见 MaiBot BOT_CONFIG_UPGRADE_HOOKS
	r.Register("0.0.0", "8.10.11", upgradeResetGroupChatPrompt)
	r.Register("8.10.11", "8.10.18", upgradeSplitJargonConfigFromExpression)
	r.Register("8.10.18", "8.10.19", upgradeNormalizeLearningItemFields)
	r.Register("8.10.19", "8.10.20", upgradeNormalizeGroupItemFields)
	r.Register("8.10.20", "8.12.1", upgradeExpressionLearningDefaults)
	r.Register("8.12.1", "8.14.19", upgradeSplitChatConfigSections)
	r.Register("8.14.19", "8.14.29", upgradeCopyPersonalityToBehaviorStyle)
	r.Register("8.14.29", "8.14.40", upgradeMigrateExpressionSelectionMode)
	r.Register("8.14.40", "8.14.58", upgradeResetExpressionDefaults)
	r.Register("8.14.58", "8.14.60", upgradeMergeWildcardLearningRulesIntoDefaults)
	r.Register("8.14.60", "8.15.37", upgradeMovePlannerMessageSteering)
	r.Register("8.15.37", "8.15.62", upgradeMigrateWebUIHTTPBindings)
	r.Register("8.15.62", "8.16.9", upgradeRemoveGlobalTalkValueRules)
	r.Register("8.16.9", "8.16.11", upgradeRemoveTalkValueRulesSwitch)
	r.Register("8.16.11", "8.16.15", noopBotUpgrade)
	r.Register("8.16.15", BotConfigTargetVersion, noopBotUpgrade)

	// model_config：锚点 1.17.17（MODEL_CONFIG_UPGRADE_HOOKS）
	r.RegisterModel("0.0.0", "1.17.17", upgradeLabelLegacyModelTypes)
	r.RegisterModel("1.17.17", ModelConfigTargetVersion, noopModelUpgrade)
}

// upgradeRemoveGlobalTalkValueRules 对齐 8.16.9：全局默认频率改用总体 talk_value
func upgradeRemoveGlobalTalkValueRules(bot *BotConfigFile) []string {
	rules := bot.Chat.ReplyTiming.TalkValueRules
	if len(rules) == 0 {
		return nil
	}
	filtered := make([]TalkRulesItem, 0, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.RuleType) == "group" {
			platformGlobal := strings.TrimSpace(rule.Platform) == "" || strings.TrimSpace(rule.Platform) == "*"
			itemGlobal := strings.TrimSpace(rule.ItemId) == "" || strings.TrimSpace(rule.ItemId) == "*"
			if platformGlobal && itemGlobal {
				continue
			}
		}
		filtered = append(filtered, rule)
	}
	if len(filtered) == len(rules) {
		return nil
	}
	bot.Chat.ReplyTiming.TalkValueRules = filtered
	return []string{"chat.reply_timing.talk_value_rules"}
}

// upgradeRemoveTalkValueRulesSwitch 对齐 8.16.11：移除规则总开关，关闭时清空规则
func upgradeRemoveTalkValueRulesSwitch(bot *BotConfigFile) []string {
	timing := &bot.Chat.ReplyTiming
	if timing.EnableTalkValueRules == nil {
		return nil
	}
	enabled := *timing.EnableTalkValueRules
	timing.EnableTalkValueRules = nil
	reasons := []string{"chat.reply_timing.enable_talk_value_rules"}
	if !enabled && len(timing.TalkValueRules) > 0 {
		timing.TalkValueRules = nil
		reasons = append(reasons, "chat.reply_timing.talk_value_rules")
	}
	return reasons
}

// upgradeCopyPersonalityToBehaviorStyle 对齐 8.14.29：Planner 行为风格首次拆分时沿用原 personality 字段
func upgradeCopyPersonalityToBehaviorStyle(bot *BotConfigFile) []string {
	p := &bot.Personality
	if hasKey(bot.LoadMeta.PersonalityKeys, "behavior_style") {
		return nil
	}
	if p.Personality == "" {
		return nil
	}
	p.BehaviorStyle = p.Personality
	return []string{"personality.behavior_style"}
}
