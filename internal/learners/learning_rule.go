package learners

import "strings"

// ChatLearningRule 对齐 MaiBot LearningItem，表示一条聊天级学习开关规则
type ChatLearningRule struct {
	Platform string
	ItemID   string
	RuleType string
	Use      bool
	Learn    bool
}

// NormalizeChatLearningRules 对齐 _learning_target_values：platform/item_id 的 * 与留空等价
func NormalizeChatLearningRules(rules []ChatLearningRule) []ChatLearningRule {
	out := make([]ChatLearningRule, 0, len(rules))
	for _, rule := range rules {
		platform := strings.TrimSpace(rule.Platform)
		itemID := strings.TrimSpace(rule.ItemID)
		if platform == "*" {
			platform = ""
		}
		if itemID == "*" {
			itemID = ""
		}
		// 对齐 Python _learning_target_values：空 rule_type 保持空串，精确匹配环被跳过
		ruleType := strings.ToLower(strings.TrimSpace(rule.RuleType))
		out = append(out, ChatLearningRule{
			Platform: platform,
			ItemID:   itemID,
			RuleType: ruleType,
			Use:      rule.Use,
			Learn:    rule.Learn,
		})
	}
	return out
}

// ResolveChatLearningRule 对齐 ChatConfigUtils.find_learning_config_item：
// 指定聊天流 > 平台默认 > 全局默认；未命中规则时返回 ok=false，由调用方按 Python 默认 True/True 处理
func ResolveChatLearningRule(rules []ChatLearningRule, scope LearningSessionScope, hasScope bool) (ChatLearningRule, bool) {
	rules = NormalizeChatLearningRules(rules)
	chatType := ""
	if hasScope {
		chatType = strings.ToLower(strings.TrimSpace(scope.ChatType))
	}
	if chatType != "" {
		platform := strings.TrimSpace(scope.Platform)
		targetID := strings.TrimSpace(scope.TargetID)
		var anyPlatform *ChatLearningRule
		var platformDefault *ChatLearningRule
		for i := range rules {
			rule := &rules[i]
			if rule.RuleType != chatType {
				continue
			}
			if rule.ItemID != "" {
				if rule.ItemID != targetID {
					continue
				}
				if rule.Platform == platform {
					return *rule, true
				}
				if rule.Platform == "" && anyPlatform == nil {
					anyPlatform = rule
				}
				continue
			}
			if rule.Platform != "" && rule.Platform == platform && platformDefault == nil {
				platformDefault = rule
			}
		}
		if anyPlatform != nil {
			return *anyPlatform, true
		}
		if platformDefault != nil {
			return *platformDefault, true
		}
	}

	var fallback *ChatLearningRule
	for i := range rules {
		rule := &rules[i]
		if rule.Platform != "" || rule.ItemID != "" {
			continue
		}
		if chatType != "" && rule.RuleType == chatType {
			return *rule, true
		}
		if fallback == nil {
			fallback = rule
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return ChatLearningRule{}, false
}
