package maibot

import (
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/maisaka"
)

// IdleBackoffFromTiming 把 chat.reply_timing 映射为 Maisaka 空闲退避参数
func IdleBackoffFromTiming(rt ChatReplyTimingConfig) maisaka.IdleBackoffConfig {
	start := rt.NoActionBackoffStartCount
	if start < 1 {
		start = 2
	}
	return maisaka.IdleBackoffConfig{
		BaseSeconds:        rt.NoActionBackoffBaseSeconds,
		CapSeconds:         rt.NoActionBackoffCapSeconds,
		StartCount:         start,
		BypassPendingCount: rt.NoActionBackoffBypassPendingCount,
	}
}

// DefaultIdleBackoffConfig 无 InstanceHolder 时的退避默认（对齐 MaiBot 官方默认）
func DefaultIdleBackoffConfig() maisaka.IdleBackoffConfig {
	return IdleBackoffFromTiming(ChatReplyTimingConfig{
		NoActionBackoffBaseSeconds:        15,
		NoActionBackoffCapSeconds:         300,
		NoActionBackoffStartCount:         2,
		NoActionBackoffBypassPendingCount: 6,
	})
}

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

// GroupTalkFrequency 群聊 talk_value，含时段规则（对齐 ChatConfigUtils.get_talk_value 群聊分支）
func GroupTalkFrequency(rt ChatReplyTimingConfig, platform string, groupID int64) float64 {
	rt.EnsureTalkRuleGroups()
	base := rt.TalkValue
	rules := rt.TalkRuleGroups.ActiveRules()
	if len(rules) == 0 {
		return base
	}
	nowMin := time.Now().Hour()*60 + time.Now().Minute()
	groupStr := strconv.FormatInt(groupID, 10)
	if matched, ok := selectTalkRuleValue(rules, platform, groupStr, true, nowMin); ok {
		return matched
	}
	return base
}

// PrivateTalkFrequency 私聊频率；暂停时为 0（静默消费）
func PrivateTalkFrequency(rt ChatReplyTimingConfig, platform string, userID int64) float64 {
	if privateReplyPaused(rt, platform, userID) {
		return 0
	}
	return 1
}

func privateReplyPaused(rt ChatReplyTimingConfig, platform string, userID int64) bool {
	userStr := strconv.FormatInt(userID, 10)
	for _, rule := range rt.PrivateReplyPauseRules {
		// 对齐 Python find_private_reply_pause_rule：仅精确 platform+user_id，无通配
		if strings.TrimSpace(rule.Platform) == platform && strings.TrimSpace(rule.ItemId) == userStr {
			return rule.Paused
		}
	}
	return rt.PrivateReplyPaused
}

// GroupTalkFrequencyFromBot 从实例 bot 配置读取群聊频率
func GroupTalkFrequencyFromBot(bot BotConfigFile, groupID int64) float64 {
	return GroupTalkFrequency(bot.Chat.ReplyTiming, strings.TrimSpace(bot.Bot.Platform), groupID)
}

// PrivateTalkFrequencyFromBot 从实例 bot 配置读取私聊频率
func PrivateTalkFrequencyFromBot(bot BotConfigFile, userID int64) float64 {
	return PrivateTalkFrequency(bot.Chat.ReplyTiming, strings.TrimSpace(bot.Bot.Platform), userID)
}

func selectTalkRuleValue(rules []TalkRulesItem, platform, itemID string, isGroup bool, nowMin int) (float64, bool) {
	selectedPriority := 0
	var selectedValue float64
	found := false
	for _, rule := range rules {
		if !rule.RuleEnabled() {
			continue
		}
		ruleType := strings.TrimSpace(rule.RuleType)
		if isGroup && ruleType != "group" {
			continue
		}
		if !isGroup && ruleType != "private" {
			continue
		}
		targetPriority := talkRuleTargetPriority(rule, platform, itemID, isGroup)
		if targetPriority == 0 {
			continue
		}
		timePriority := talkRuleTimePriority(rule.Time, nowMin)
		if timePriority == 0 {
			continue
		}
		priority := targetPriority*10 + timePriority
		if !found || priority > selectedPriority {
			selectedPriority = priority
			selectedValue = rule.Value
			found = true
		}
	}
	return selectedValue, found
}

func talkRuleTargetPriority(rule TalkRulesItem, platform, itemID string, isGroup bool) int {
	ruleType := strings.TrimSpace(rule.RuleType)
	if ruleType == "group" && !isGroup {
		return 0
	}
	if ruleType == "private" && isGroup {
		return 0
	}
	if ruleType != "group" && ruleType != "private" {
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

func talkRuleTimePriority(ruleTime string, nowMin int) int {
	ruleTime = strings.TrimSpace(ruleTime)
	if ruleTime == "" {
		return 1
	}
	if ruleTime == "*" {
		return 3
	}
	start, end, ok := parseTalkTimeRange(ruleTime)
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

func parseTalkTimeRange(value string) (int, int, bool) {
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
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	return hour*60 + min, true
}
