package maibot

import (
	"errors"
	"strings"
)

const defaultGroupChatPrompt = "下面是聊天中的的内容，聊天中包含文字，图片和表情包等消息。\n" +
	"回复尽量简短一些。最好一次对一个话题进行回复，但必须考虑不同群友发言之间的交互，免得啰嗦或者回复内容太乱。\n" +
	"根据聊天内容自由发挥，但是要日常不浮夸，不要刻意找话题。\n" +
	"不用刻意回复其他人发送的表情包。你可以适当发送表情包表达情绪。如果有人不喜欢你或者不理你，就不要强行回复，回复前读空气。不要每个人的消息都回复，适当回复其他话题。可以适当回过头回复之前用户的消息。\n"

// upgradeResetGroupChatPrompt 对齐 8.10.11：重置群聊提示词为官方默认
func upgradeResetGroupChatPrompt(bot *BotConfigFile) []string {
	if bot.Chat.ReplyStyle.GroupChatPrompt == defaultGroupChatPrompt {
		return nil
	}
	bot.Chat.ReplyStyle.GroupChatPrompt = defaultGroupChatPrompt
	// Python 在原始 dict 中写入该键，后续 8.14.19 split 会看到 reply_style 已存在而不再搬旧根键；
	// Go 的 LoadMeta 是加载时快照，需要同步记录这个内存中新增的键
	if bot.LoadMeta.ChatReplyStyleKeys == nil {
		bot.LoadMeta.ChatReplyStyleKeys = make(map[string]struct{})
	}
	bot.LoadMeta.ChatReplyStyleKeys["group_chat_prompt"] = struct{}{}
	return []string{"chat.reply_style.group_chat_prompt"}
}

// upgradeSplitJargonConfigFromExpression 对齐 8.10.18
func upgradeSplitJargonConfigFromExpression(bot *BotConfigFile) []string {
	var reasons []string
	expr := &bot.Expression
	jargon := &bot.Jargon

	if !hasKey(bot.LoadMeta.JargonKeys, "learning_list") {
		var jargonLearning []LearningItem
		for i := range expr.LearningList {
			item := &expr.LearningList[i]
			if item.EnableJargonLearning == nil {
				continue
			}
			ruleType := item.Type
			if ruleType == "" {
				ruleType = item.RuleType
			}
			if ruleType == "" {
				ruleType = "group"
			}
			enable := *item.EnableJargonLearning
			jargonLearning = append(jargonLearning, LearningItem{
				Platform:       item.Platform,
				ItemId:         item.ItemId,
				RuleType:       ruleType,
				EnableLearning: &enable,
			})
		}
		if len(jargonLearning) > 0 {
			jargon.LearningList = jargonLearning
			reasons = append(reasons, "jargon.learning_list")
		}
	}

	removedJargonFlag := false
	for i := range expr.LearningList {
		if expr.LearningList[i].EnableJargonLearning != nil {
			expr.LearningList[i].EnableJargonLearning = nil
			removedJargonFlag = true
		}
	}
	if removedJargonFlag {
		reasons = append(reasons, "expression.learning_list.enable_jargon_learning")
	}

	if expr.AllGlobalJargon != nil && *expr.AllGlobalJargon && !hasKey(bot.LoadMeta.JargonKeys, "jargon_groups") && jargon.JargonGroupsLegacyRaw == nil {
		jargon.JargonGroups = []ChatStreamGroup{{
			LegacyInnerJargonGroups: []TargetItem{{
				Platform: "*", ItemId: "*", RuleType: "group",
			}},
		}}
		reasons = append(reasons, "jargon.jargon_groups")
	}
	if expr.AllGlobalJargon != nil {
		expr.AllGlobalJargon = nil
		reasons = append(reasons, "expression.all_global_jargon")
	}

	if migrateJargonGroups(jargon) {
		reasons = append(reasons, "jargon.jargon_groups")
	}
	return reasons
}

func migrateJargonGroups(jargon *JargonConfig) bool {
	if jargon.JargonGroupsLegacyRaw != nil {
		migrated, groups := migrateJargonGroupsRaw(jargon.JargonGroupsLegacyRaw)
		if migrated {
			jargon.JargonGroups = groups
			jargon.JargonGroupsLegacyRaw = nil
			return true
		}
	}
	if len(jargon.JargonGroups) == 0 {
		return false
	}
	if jargonGroupsAlreadyNormalized(jargon.JargonGroups) {
		return false
	}
	migrated := false
	for i := range jargon.JargonGroups {
		if normalizeLegacyJargonGroupEntry(&jargon.JargonGroups[i]) {
			migrated = true
		}
	}
	return migrated
}

func jargonGroupsAlreadyNormalized(groups []ChatStreamGroup) bool {
	for _, g := range groups {
		if len(g.LegacyInnerJargonGroups) > 0 && len(g.Targets) == 0 {
			return false
		}
	}
	return true
}

func normalizeLegacyJargonGroupEntry(group *ChatStreamGroup) bool {
	if len(group.Targets) > 0 {
		return false
	}
	if len(group.LegacyInnerJargonGroups) == 0 {
		return false
	}
	group.Targets = append([]TargetItem(nil), group.LegacyInnerJargonGroups...)
	group.LegacyInnerJargonGroups = nil
	return true
}

func migrateJargonGroupsRaw(raw any) (bool, []ChatStreamGroup) {
	switch v := raw.(type) {
	case string:
		normalized := strings.TrimSpace(v)
		if normalized == "" {
			return true, nil
		}
		parsed := parseJargonGroupTarget(normalized)
		if parsed == nil {
			return false, nil
		}
		return true, []ChatStreamGroup{{
			LegacyInnerJargonGroups: []TargetItem{*parsed},
		}}
	case []any:
		if len(v) > 0 && allMap(v) {
			groups, err := decodeChatStreamGroups(v)
			if err == nil && len(groups) > 0 {
				changed := false
				for i := range groups {
					if normalizeLegacyJargonGroupEntry(&groups[i]) {
						changed = true
					}
				}
				if !changed && jargonGroupsAlreadyNormalized(groups) {
					return false, groups
				}
				return true, groups
			}
		}
		var migrated []ChatStreamGroup
		for _, group := range v {
			groupItems, ok := group.([]any)
			if !ok {
				return false, nil
			}
			var targets []TargetItem
			for _, item := range groupItems {
				parsed := parseJargonGroupTarget(strings.TrimSpace(anyString(item)))
				if parsed == nil {
					return false, nil
				}
				targets = append(targets, *parsed)
			}
			migrated = append(migrated, ChatStreamGroup{LegacyInnerJargonGroups: targets})
		}
		for i := range migrated {
			normalizeLegacyJargonGroupEntry(&migrated[i])
		}
		return true, migrated
	default:
		return false, nil
	}
}

func allMap(list []any) bool {
	for _, item := range list {
		if asMap(item) == nil {
			return false
		}
	}
	return true
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}

func parseJargonGroupTarget(value string) *TargetItem {
	normalized := strings.TrimSpace(value)
	if normalized == "*" {
		return &TargetItem{Platform: "*", ItemId: "*", RuleType: "group"}
	}
	parts := strings.SplitN(normalized, ":", 3)
	if len(parts) != 3 {
		return nil
	}
	ruleType := parts[2]
	if ruleType != "group" && ruleType != "private" {
		return nil
	}
	return &TargetItem{Platform: parts[0], ItemId: parts[1], RuleType: ruleType}
}

// upgradeNormalizeLearningItemFields 对齐 8.10.19
func upgradeNormalizeLearningItemFields(bot *BotConfigFile) []string {
	var reasons []string
	if normalizeLearningListFields(&bot.Expression.LearningList, []string{"use_expression", "use"}, []string{"enable_learning", "learn"}, true, true) {
		reasons = append(reasons, "expression.learning_list")
	}
	if normalizeJargonLearningListFields(&bot.Jargon.LearningList) {
		reasons = append(reasons, "jargon.learning_list")
	}
	return reasons
}

func normalizeJargonLearningListFields(list *[]LearningItem) bool {
	return normalizeLearningListFields(list, []string{"use"}, []string{"enable_learning", "learn"}, true, true)
}

func normalizeLearningListFields(list *[]LearningItem, useKeys, learnKeys []string, defaultUse, defaultLearn bool) bool {
	if list == nil || len(*list) == 0 {
		return false
	}
	migrated := false
	for i := range *list {
		item := &(*list)[i]
		nType := item.Type
		if nType == "" {
			nType = item.RuleType
		}
		if nType == "" {
			nType = "group"
		}
		nUse := firstExistingBool(item, useKeys, defaultUse)
		nLearn := firstExistingBool(item, learnKeys, defaultLearn)

		if item.RuleType != "" {
			item.RuleType = ""
			migrated = true
		}
		if item.UseExpression != nil {
			item.UseExpression = nil
			migrated = true
		}
		if item.EnableLearning != nil {
			item.EnableLearning = nil
			migrated = true
		}
		if item.Type != nType {
			item.Type = nType
			migrated = true
		}
		if item.Use != nUse {
			item.Use = nUse
			migrated = true
		}
		if item.Learn != nLearn {
			item.Learn = nLearn
			migrated = true
		}
	}
	return migrated
}

func firstExistingBool(item *LearningItem, keys []string, defaultVal bool) bool {
	for _, key := range keys {
		switch key {
		case "use_expression":
			if item.UseExpression != nil {
				return *item.UseExpression
			}
		case "use":
			// 对齐 Python: key "use" in item → bool(item["use"])
			// Use 为非指针；解码/defaults 后已承载有效值
			return item.Use
		case "enable_learning":
			if item.EnableLearning != nil {
				return *item.EnableLearning
			}
		case "learn":
			return item.Learn
		}
	}
	return defaultVal
}

// upgradeNormalizeGroupItemFields 对齐 8.10.20
func upgradeNormalizeGroupItemFields(bot *BotConfigFile) []string {
	var reasons []string
	if normalizeGroupListFields(&bot.Expression.ExpressionGroups, "expression_groups") {
		reasons = append(reasons, "expression.expression_groups")
	}
	if normalizeGroupListFields(&bot.Jargon.JargonGroups, "jargon_groups") {
		reasons = append(reasons, "jargon.jargon_groups")
	}
	return reasons
}

func normalizeGroupListFields(groups *[]ChatStreamGroup, innerKey string) bool {
	if groups == nil || len(*groups) == 0 {
		return false
	}
	migrated := false
	for i := range *groups {
		g := &(*groups)[i]
		var legacy []TargetItem
		switch innerKey {
		case "expression_groups":
			legacy = g.LegacyInnerExpressionGroups
		case "jargon_groups":
			legacy = g.LegacyInnerJargonGroups
		}
		if len(g.Targets) == 0 && len(legacy) > 0 {
			g.Targets = append([]TargetItem(nil), legacy...)
			migrated = true
		}
		if len(g.LegacyInnerExpressionGroups) > 0 {
			g.LegacyInnerExpressionGroups = nil
			migrated = true
		}
		if len(g.LegacyInnerJargonGroups) > 0 {
			g.LegacyInnerJargonGroups = nil
			migrated = true
		}
	}
	return migrated
}

// upgradeExpressionLearningDefaults 对齐 8.12.1
func upgradeExpressionLearningDefaults(bot *BotConfigFile) []string {
	e := &bot.Expression
	var reasons []string
	if !hasKey(bot.LoadMeta.ExpressionKeys, "max_expression_learner") || e.MaxExpressionLearner == 2 {
		e.MaxExpressionLearner = 3
		reasons = append(reasons, "expression.max_expression_learner")
	}
	if !hasKey(bot.LoadMeta.ExpressionKeys, "expression_self_reflect") {
		e.ExpressionSelfReflect = true
		reasons = append(reasons, "expression.expression_self_reflect")
	}
	return reasons
}

// upgradeSplitChatConfigSections 对齐 8.14.19
func upgradeSplitChatConfigSections(bot *BotConfigFile) []string {
	chat := &bot.Chat
	legacy := &chat.ChatLegacyFlatFields
	rootKeys := bot.LoadMeta.ChatRootKeys
	timingKeys := bot.LoadMeta.ChatReplyTimingKeys
	styleKeys := bot.LoadMeta.ChatReplyStyleKeys
	var moved []string

	rt := &chat.ReplyTiming
	rs := &chat.ReplyStyle

	moveRootToTiming := func(key string, apply func()) {
		if !hasKey(rootKeys, key) {
			return
		}
		if hasKey(timingKeys, key) {
			moved = append(moved, "chat.reply_timing."+key)
			return
		}
		apply()
		moved = append(moved, "chat.reply_timing."+key)
	}
	moveRootToStyle := func(key string, apply func()) {
		if !hasKey(rootKeys, key) {
			return
		}
		if hasKey(styleKeys, key) {
			moved = append(moved, "chat.reply_style."+key)
			return
		}
		apply()
		moved = append(moved, "chat.reply_style."+key)
	}

	moveRootToTiming("talk_value", func() { rt.TalkValue = legacy.TalkValue })
	moveRootToTiming("mentioned_bot_reply", func() {
		if legacy.MentionedBotReply != nil {
			rt.MentionedBotReply = *legacy.MentionedBotReply
		}
	})
	moveRootToTiming("inevitable_at_reply", func() {
		if legacy.InevitableAtReply != nil {
			rt.InevitableAtReply = *legacy.InevitableAtReply
		}
	})
	// Python 的 reply_timing/reply_style 子节未声明这两个键，升级后会被 pydantic 当冗余键丢弃；
	// 这里只记录迁移路径，不写入字段，避免升级产物多出 Python 没有的键
	moveRootToTiming("max_consecutive_wait_count", func() {})
	moveRootToTiming("enable_talk_value_rules", func() {
		if legacy.EnableTalkValueRules != nil {
			rt.EnableTalkValueRules = legacy.EnableTalkValueRules
		}
	})
	moveRootToTiming("talk_value_rules", func() {
		if len(legacy.TalkValueRules) > 0 {
			rt.TalkValueRules = legacy.TalkValueRules
		}
	})

	// 同上：reply_style 未声明 enable_reply_quote，只记录迁移路径
	moveRootToStyle("enable_reply_quote", func() {})
	moveRootToStyle("group_chat_prompt", func() { rs.GroupChatPrompt = legacy.GroupChatPrompt })
	moveRootToStyle("private_chat_prompts", func() { rs.PrivateChatPrompts = legacy.PrivateChatPrompts })
	moveRootToStyle("chat_prompts", func() {
		if len(legacy.ChatPrompts) > 0 {
			rs.ChatPrompts = legacy.ChatPrompts
		}
	})

	chat.ChatLegacyFlatFields = ChatLegacyFlatFields{}
	return moved
}

func decodeChatStreamGroups(raw any) ([]ChatStreamGroup, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errInvalidGroups
	}
	out := make([]ChatStreamGroup, 0, len(list))
	for _, item := range list {
		m := asMap(item)
		if m == nil {
			return nil, errInvalidGroups
		}
		var group ChatStreamGroup
		if targets, ok := m["targets"]; ok {
			group.Targets = decodeTargetItems(targets)
		}
		if inner, ok := m["expression_groups"]; ok {
			group.LegacyInnerExpressionGroups = decodeTargetItems(inner)
		}
		if inner, ok := m["jargon_groups"]; ok {
			group.LegacyInnerJargonGroups = decodeTargetItems(inner)
		}
		out = append(out, group)
	}
	return out, nil
}

var errInvalidGroups = errors.New("invalid chat stream groups")

func decodeTargetItems(raw any) []TargetItem {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]TargetItem, 0, len(list))
	for _, item := range list {
		m := asMap(item)
		if m == nil {
			continue
		}
		out = append(out, TargetItem{
			Platform: anyString(m["platform"]),
			ItemId:   anyString(m["item_id"]),
			RuleType: firstNonEmpty(anyString(m["rule_type"]), anyString(m["type"])),
		})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
