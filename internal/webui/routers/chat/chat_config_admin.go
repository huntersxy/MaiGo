package chat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/chat/session"
)

// chatConfigDoc 读取实例 bot_config.toml；缺失时返回空文档，详情回落默认值
func (d Deps) chatConfigDoc() map[string]any {
	path, ok := d.botConfigPath()
	if !ok {
		return map[string]any{}
	}
	doc, err := d.readTOMLDocument(path)
	if err != nil {
		return map[string]any{}
	}
	return doc
}

func chatConfigTable(root map[string]any, key string) map[string]any {
	table, _ := root[key].(map[string]any)
	if table == nil {
		return map[string]any{}
	}
	return table
}

func chatConfigFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func chatConfigBool(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}

func chatConfigString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func chatConfigRules(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			out = append(out, typed)
		case string:
			parts := strings.SplitN(typed, ":", 4)
			if len(parts) != 4 {
				continue
			}
			out = append(out, map[string]any{
				"platform": strings.TrimSpace(parts[0]),
				"item_id":  strings.TrimSpace(parts[1]),
				"type":     strings.TrimSpace(parts[2]),
				"prompt":   strings.TrimSpace(parts[3]),
			})
		}
	}
	return out
}

func chatRuleTargets(rule map[string]any) (platform, itemID, ruleType string) {
	platform = chatConfigString(rule["platform"])
	itemID = chatConfigString(rule["item_id"])
	ruleType = chatConfigString(rule["rule_type"])
	if ruleType == "" {
		ruleType = chatConfigString(rule["type"])
	}
	return platform, itemID, ruleType
}

func chatLearningTargets(rule map[string]any) (platform, itemID, ruleType string) {
	platform, itemID, ruleType = chatRuleTargets(rule)
	if platform == "*" {
		platform = ""
	}
	if itemID == "*" {
		itemID = ""
	}
	return platform, itemID, ruleType
}

// findChatLearningRule 按“指定聊天流 > 平台默认 > 全局默认”查找学习规则
func findChatLearningRule(rules []map[string]any, rec *chatsession.Record) map[string]any {
	chatType := chatTypeOf(rec)
	platform := strings.TrimSpace(rec.Platform)
	targetID := chatTargetID(rec)
	var anyPlatform map[string]any
	var platformDefault map[string]any
	for _, rule := range rules {
		rulePlatform, ruleItem, ruleType := chatLearningTargets(rule)
		if ruleType != chatType {
			continue
		}
		if ruleItem != "" {
			if ruleItem != targetID {
				continue
			}
			if rulePlatform == platform {
				return rule
			}
			if rulePlatform == "" && anyPlatform == nil {
				anyPlatform = rule
			}
		} else if rulePlatform != "" && rulePlatform == platform && platformDefault == nil {
			platformDefault = rule
		}
	}
	if anyPlatform != nil {
		return anyPlatform
	}
	if platformDefault != nil {
		return platformDefault
	}
	var defaultRule map[string]any
	for _, rule := range rules {
		rulePlatform, ruleItem, ruleType := chatLearningTargets(rule)
		if rulePlatform != "" || ruleItem != "" {
			continue
		}
		if ruleType == chatType {
			return rule
		}
		if defaultRule == nil {
			defaultRule = rule
		}
	}
	return defaultRule
}

func chatLearningSwitches(doc map[string]any, section string, rec *chatsession.Record) (bool, bool, map[string]any) {
	table := chatConfigTable(doc, section)
	rules := chatConfigRules(table["learning_list"])
	rule := findChatLearningRule(rules, rec)
	if rule == nil {
		return true, true, nil
	}
	use := chatConfigBool(rule["use"], true)
	learn := chatConfigBool(rule["learn"], true)
	return use, learn, rule
}

func chatTargetConfigDetail(rule map[string]any) map[string]any {
	platform, itemID, ruleType := chatRuleTargets(rule)
	normalizedPlatform, normalizedItem, _ := chatLearningTargets(rule)
	return map[string]any{
		"platform":            platform,
		"item_id":             itemID,
		"type":                ruleType,
		"use":                 chatConfigBool(rule["use"], true),
		"learn":               chatConfigBool(rule["learn"], true),
		"is_default":          normalizedPlatform == "" && normalizedItem == "",
		"is_platform_default": normalizedPlatform != "" && normalizedItem == "",
		"is_wildcard":         platform == "*" || itemID == "*",
	}
}

// parseChatTimeRange 解析 HH:MM-HH:MM 为分钟区间
func parseChatTimeRange(value string) (int, int, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	parse := func(raw string) (int, bool) {
		clock := strings.Split(strings.TrimSpace(raw), ":")
		if len(clock) != 2 {
			return 0, false
		}
		hour, errHour := strconv.Atoi(strings.TrimSpace(clock[0]))
		minute, errMinute := strconv.Atoi(strings.TrimSpace(clock[1]))
		if errHour != nil || errMinute != nil {
			return 0, false
		}
		return hour*60 + minute, true
	}
	start, okStart := parse(parts[0])
	end, okEnd := parse(parts[1])
	if !okStart || !okEnd {
		return 0, 0, false
	}
	return start, end, true
}

func chatTimePriority(ruleTime string, nowMin int) (int, bool) {
	ruleTime = strings.TrimSpace(ruleTime)
	if ruleTime == "" {
		return 1, true
	}
	if ruleTime == "*" {
		return 3, true
	}
	start, end, ok := parseChatTimeRange(ruleTime)
	if !ok {
		return 0, false
	}
	if start <= end {
		return 2, nowMin >= start && nowMin <= end
	}
	return 2, nowMin >= start || nowMin <= end
}

func chatTalkTargetPriority(rule map[string]any, rec *chatsession.Record) (int, bool) {
	platform, itemID, ruleType := chatRuleTargets(rule)
	if ruleType != "group" || !rec.IsGroup() {
		return 0, false
	}
	if platform == "" && itemID == "" {
		return 1, true
	}
	wildcard := platform == "*" || itemID == "*"
	streamPlatform := strings.TrimSpace(rec.Platform)
	streamTarget := chatTargetID(rec)
	platformMatches := platform == "" || platform == "*" || platform == streamPlatform
	itemMatches := itemID == "" || itemID == "*" || itemID == streamTarget
	if !platformMatches || !itemMatches {
		return 0, false
	}
	if platform != "" && itemID != "" && !wildcard {
		return 5, true
	}
	if wildcard {
		return 4, true
	}
	return 3, true
}

func formatChatFrequency(value float64) string {
	if value < 0 {
		value = 0
	}
	return fmt.Sprintf("%.2f（%.1f%%）", value, value*100)
}

func chatTalkRuleDetail(rule map[string]any, rec *chatsession.Record, nowMin int) (map[string]any, bool) {
	targetPriority, ok := chatTalkTargetPriority(rule, rec)
	if !ok {
		return nil, false
	}
	platform, itemID, ruleType := chatRuleTargets(rule)
	ruleTime := chatConfigString(rule["time"])
	timePriority, active := chatTimePriority(ruleTime, nowMin)
	value := chatConfigFloat(rule["value"], 0)
	return map[string]any{
		"platform":          platform,
		"item_id":           itemID,
		"type":              ruleType,
		"time":              ruleTime,
		"value":             value,
		"enabled":           chatConfigBool(rule["enabled"], true),
		"value_label":       formatChatFrequency(value),
		"target_priority":   targetPriority,
		"time_priority":     timePriority,
		"time_active":       active,
		"is_effective":      false,
		"is_default_target": platform == "" && itemID == "",
	}, true
}

func chatTalkDetails(doc map[string]any, rec *chatsession.Record) map[string]any {
	replyTiming := chatConfigTable(chatConfigTable(doc, "chat"), "reply_timing")
	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	currentTime := now.Format("15:04")
	if !rec.IsGroup() {
		paused := chatPrivateReplyPaused(doc, rec)
		value := 1.0
		label := "正常回复"
		if paused {
			value = 0
			label = "已暂停"
		}
		return map[string]any{
			"base_value": value, "base_value_label": label,
			"effective_value": value, "effective_value_label": label,
			"current_time": currentTime, "active_group": nil, "matched_rules": []any{},
		}
	}
	baseValue := chatConfigFloat(replyTiming["talk_value"], 1.0)
	effectiveValue := baseValue
	groups := chatTalkRuleGroups(replyTiming)
	activeGroupName := chatConfigString(groups["active_group"])
	rules := []map[string]any{}
	if group := chatActiveTalkGroup(groups); group != nil {
		rules = chatConfigRules(group["rules"])
	}
	details := make([]map[string]any, 0, len(rules))
	selectedIndex := -1
	selectedPriority := [2]int{0, 0}
	selectedValue := baseValue
	for _, rule := range rules {
		detail, ok := chatTalkRuleDetail(rule, rec, nowMin)
		if !ok {
			continue
		}
		if detail["time_active"].(bool) && detail["enabled"].(bool) {
			priority := [2]int{detail["target_priority"].(int), detail["time_priority"].(int)}
			if priority[0] > selectedPriority[0] || (priority[0] == selectedPriority[0] && priority[1] > selectedPriority[1]) {
				selectedPriority = priority
				selectedIndex = len(details)
				selectedValue = chatConfigFloat(rule["value"], baseValue)
			}
		}
		details = append(details, detail)
	}
	if selectedIndex >= 0 {
		details[selectedIndex]["is_effective"] = true
		effectiveValue = selectedValue
	}
	sort.SliceStable(details, func(i, j int) bool {
		left, right := details[i], details[j]
		leftEffective, rightEffective := left["is_effective"].(bool), right["is_effective"].(bool)
		if leftEffective != rightEffective {
			return leftEffective
		}
		leftTarget, rightTarget := left["target_priority"].(int), right["target_priority"].(int)
		if leftTarget != rightTarget {
			return leftTarget > rightTarget
		}
		return left["time_priority"].(int) > right["time_priority"].(int)
	})
	return map[string]any{
		"base_value": baseValue, "base_value_label": formatChatFrequency(baseValue),
		"effective_value": effectiveValue, "effective_value_label": formatChatFrequency(effectiveValue),
		"current_time": currentTime, "active_group": activeGroupName, "matched_rules": details,
	}
}

func chatPrivateReplyPauseRule(doc map[string]any, rec *chatsession.Record) map[string]any {
	replyTiming := chatConfigTable(chatConfigTable(doc, "chat"), "reply_timing")
	rules := chatConfigRules(replyTiming["private_reply_pause_rules"])
	platform := strings.TrimSpace(rec.Platform)
	itemID := chatTargetID(rec)
	for _, rule := range rules {
		if chatConfigString(rule["platform"]) == platform && chatConfigString(rule["item_id"]) == itemID {
			return rule
		}
	}
	return nil
}

func chatPrivateReplyPaused(doc map[string]any, rec *chatsession.Record) bool {
	replyTiming := chatConfigTable(chatConfigTable(doc, "chat"), "reply_timing")
	globalPaused := chatConfigBool(replyTiming["private_reply_paused"], false)
	if rule := chatPrivateReplyPauseRule(doc, rec); rule != nil {
		return chatConfigBool(rule["paused"], globalPaused)
	}
	return globalPaused
}

func chatPrivateReplyDetails(doc map[string]any, rec *chatsession.Record) any {
	if rec.IsGroup() {
		return nil
	}
	replyTiming := chatConfigTable(chatConfigTable(doc, "chat"), "reply_timing")
	return map[string]any{
		"paused":        chatPrivateReplyPaused(doc, rec),
		"global_paused": chatConfigBool(replyTiming["private_reply_paused"], false),
		"has_override":  chatPrivateReplyPauseRule(doc, rec) != nil,
	}
}

func chatPromptItem(rule map[string]any) (map[string]any, bool) {
	platform, itemID, ruleType := chatRuleTargets(rule)
	prompt := chatConfigString(rule["prompt"])
	if platform == "" || itemID == "" || prompt == "" || (ruleType != "group" && ruleType != "private") {
		return nil, false
	}
	return map[string]any{"platform": platform, "item_id": itemID, "rule_type": ruleType, "prompt": prompt}, true
}

func chatPromptDetails(doc map[string]any, rec *chatsession.Record) map[string]any {
	replyStyle := chatConfigTable(chatConfigTable(doc, "chat"), "reply_style")
	isGroup := rec.IsGroup()
	baseType := "private"
	baseTitle := "私聊提示词"
	basePrompt := chatConfigString(replyStyle["private_chat_prompts"])
	if isGroup {
		baseType = "group"
		baseTitle = "群聊提示词"
		basePrompt = chatConfigString(replyStyle["group_chat_prompt"])
	}
	chatPrompts := make([]any, 0)
	for index, raw := range chatConfigRules(replyStyle["chat_prompts"]) {
		item, ok := chatPromptItem(raw)
		if !ok {
			continue
		}
		if item["platform"] != strings.TrimSpace(rec.Platform) || item["item_id"] != chatTargetID(rec) || item["rule_type"] != chatTypeOf(rec) {
			continue
		}
		chatPrompts = append(chatPrompts, map[string]any{
			"index": index, "platform": item["platform"], "item_id": item["item_id"],
			"rule_type": item["rule_type"], "prompt": item["prompt"],
		})
	}
	return map[string]any{
		"base_prompt_type": baseType, "base_prompt_title": baseTitle,
		"base_prompt": basePrompt, "chat_prompts": chatPrompts,
	}
}

// chatSessionDetailPayload 构建与 Python _chat_session_detail_to_response 对齐的详情
func (d Deps) chatSessionDetailPayload(rec *chatsession.Record) (map[string]any, error) {
	doc := d.chatConfigDoc()
	expressionUse, expressionLearn, expressionRule := chatLearningSwitches(doc, "expression", rec)
	jargonUse, jargonLearn, jargonRule := chatLearningSwitches(doc, "jargon", rec)
	var expressionMatched, jargonMatched any
	if expressionRule != nil {
		expressionMatched = chatTargetConfigDetail(expressionRule)
	}
	if jargonRule != nil {
		jargonMatched = chatTargetConfigDetail(jargonRule)
	}
	adapters, err := d.chatAdapterDetails(rec)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id":      rec.SessionID,
		"display_name":    ChatDisplayName(rec, nil),
		"chat_type":       chatTypeOf(rec),
		"platform":        rec.Platform,
		"account_id":      rec.AccountID,
		"scope":           rec.Scope,
		"target_id":       chatTargetID(rec),
		"external_target": chatTargetID(rec),
		"group_id":        rec.GroupID,
		"user_id":         rec.UserID,
		"expression": map[string]any{
			"use": expressionUse, "learn": expressionLearn, "matched_rule": expressionMatched,
		},
		"jargon": map[string]any{
			"use": jargonUse, "learn": jargonLearn, "matched_rule": jargonMatched,
		},
		"talk_frequency": chatTalkDetails(doc, rec),
		"private_reply":  chatPrivateReplyDetails(doc, rec),
		"prompts":        chatPromptDetails(doc, rec),
		"adapters":       adapters,
	}, nil
}
