package maibot

import (
	"strconv"
	"strings"
	"time"
)

// GroupDebounceMode 群聊防抖档位
func GroupDebounceMode(rt ChatReplyTimingConfig) string {
	mode := strings.TrimSpace(rt.MessageDebounceMode)
	if mode == "" {
		return "natural"
	}
	return mode
}

// PrivateDebounceMode 私聊防抖档位
func PrivateDebounceMode(rt ChatReplyTimingConfig) string {
	mode := strings.TrimSpace(rt.PrivateMessageDebounceMode)
	if mode == "" {
		return "natural"
	}
	return mode
}

// ActiveRules 返回当前生效规则组内的规则
func (groups TalkRuleGroups) ActiveRules() []TalkRulesItem {
	active := strings.TrimSpace(groups.ActiveGroup)
	for _, group := range groups.Groups {
		if strings.TrimSpace(group.Name) == active {
			return group.Rules
		}
	}
	if len(groups.Groups) > 0 {
		return groups.Groups[0].Rules
	}
	return nil
}

// EnsureTalkRuleGroups 为旧配置补齐默认规则组
func (rt *ChatReplyTimingConfig) EnsureTalkRuleGroups() {
	if rt == nil {
		return
	}
	if len(rt.TalkRuleGroups.Groups) == 0 {
		name := DefaultTalkRuleGroupName
		groups := []TalkRuleGroup{}
		if len(rt.TalkValueRules) > 0 {
			groups = append(groups, TalkRuleGroup{Name: name, Rules: rt.TalkValueRules})
		}
		rt.TalkRuleGroups = TalkRuleGroups{ActiveGroup: name, Groups: groups}
		return
	}
	if strings.TrimSpace(rt.TalkRuleGroups.ActiveGroup) == "" {
		rt.TalkRuleGroups.ActiveGroup = strings.TrimSpace(rt.TalkRuleGroups.Groups[0].Name)
	}
}

const DefaultTalkRuleValue = 0.5

func (rule TalkRulesItem) Frequency() float64 {
	if rule.Value == nil {
		return DefaultTalkRuleValue
	}
	return *rule.Value
}

func (rule TalkRulesItem) ChatType() string {
	if rule.RuleType == "" {
		return "group"
	}
	return rule.RuleType
}

// GroupTalkFrequency 群聊 talk_value，含时段规则（对齐 ChatConfigUtils.get_talk_value 群聊分支）
func GroupTalkFrequency(rt ChatReplyTimingConfig, platform, groupID string) float64 {
	rt.EnsureTalkRuleGroups()
	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	selectedPriority := 0
	value := rt.TalkValue
	for _, rule := range rt.TalkRuleGroups.ActiveRules() {
		if !rule.RuleEnabled() {
			continue
		}
		targetPriority := GroupTalkRuleTargetPriority(rule, platform, groupID)
		if targetPriority == 0 {
			continue
		}
		timePriority := TalkRuleTimePriority(rule.Time, nowMin)
		priority := targetPriority*10 + timePriority
		if timePriority > 0 && priority > selectedPriority {
			selectedPriority = priority
			value = rule.Frequency()
		}
	}
	return value
}

// PrivateTalkFrequency 私聊频率；暂停时为 0（静默消费）
func PrivateTalkFrequency(rt ChatReplyTimingConfig, platform, userID string) float64 {
	if privateReplyPaused(rt, platform, userID) {
		return 0
	}
	return 1
}

func privateReplyPaused(rt ChatReplyTimingConfig, platform, userID string) bool {
	for _, rule := range rt.PrivateReplyPauseRules {
		// 对齐 Python find_private_reply_pause_rule：仅精确 platform+user_id，无通配
		if rule.Platform == platform && rule.ItemId == userID {
			return rule.Paused == nil || *rule.Paused
		}
	}
	return rt.PrivateReplyPaused
}

func GroupTalkRuleTargetPriority(rule TalkRulesItem, platform, itemID string) int {
	if rule.ChatType() != "group" || itemID == "" {
		return 0
	}
	rulePlatform := strings.TrimSpace(rule.Platform)
	ruleItem := strings.TrimSpace(rule.ItemId)
	if rulePlatform == "" && ruleItem == "" {
		return 1
	}
	platformMatches := rulePlatform == "" || rulePlatform == "*" || rulePlatform == platform
	itemMatches := ruleItem == "" || ruleItem == "*" || ruleItem == itemID
	if !platformMatches || !itemMatches {
		return 0
	}
	if rulePlatform != "" && ruleItem != "" && rulePlatform != "*" && ruleItem != "*" {
		return 5
	}
	if rulePlatform == "*" || ruleItem == "*" {
		return 4
	}
	return 3
}

func TalkRuleTimePriority(ruleTime string, nowMin int) int {
	ruleTime = strings.TrimSpace(ruleTime)
	if ruleTime == "" {
		return 1
	}
	if ruleTime == "*" {
		return 3
	}
	start, end, ok := ParseTalkTimeRange(ruleTime)
	if !ok {
		return 0
	}
	if start <= end {
		if nowMin >= start && nowMin <= end {
			return 2
		}
		return 0
	}
	if nowMin >= start || nowMin <= end {
		return 2
	}
	return 0
}

func ParseTalkTimeRange(value string) (int, int, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, ok := parseClockMinutes(strings.TrimSpace(parts[0]))
	if !ok {
		return 0, 0, false
	}
	end, ok := parseClockMinutes(strings.TrimSpace(parts[1]))
	if !ok {
		return 0, 0, false
	}
	return start, end, true
}

func parseClockMinutes(value string) (int, bool) {
	// 对齐 Python int(x)：整段必须是 HH:MM，尾随字符视为非法
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, false
	}
	min, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, false
	}
	return hour*60 + min, true
}
