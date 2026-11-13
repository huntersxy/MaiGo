package focus

import (
	"strings"

	"maibot-go/internal/config/maibot"
)

// TargetMatchesSession 精确匹配 platform/item_id/rule_type
func TargetMatchesSession(target maibot.TargetItem, session SessionInfo) bool {
	if session.SessionID == "" {
		return false
	}
	platform := strings.TrimSpace(target.Platform)
	itemID := strings.TrimSpace(target.ItemId)
	ruleType := strings.TrimSpace(target.RuleType)
	if platform == "" || itemID == "" {
		return false
	}

	var configIsGroup bool
	var streamTarget string
	switch ruleType {
	case "group":
		configIsGroup = true
		streamTarget = strings.TrimSpace(session.GroupID)
	case "private":
		configIsGroup = false
		streamTarget = strings.TrimSpace(session.UserID)
	default:
		return false
	}
	if configIsGroup != session.IsGroup {
		return false
	}
	if streamTarget == "" {
		return false
	}
	return strings.TrimSpace(session.Platform) == platform && streamTarget == itemID
}

// TargetMatchesSessionWithWildcards 允许 platform/item_id 为 *
func TargetMatchesSessionWithWildcards(target maibot.TargetItem, session SessionInfo) bool {
	if session.SessionID == "" {
		return false
	}
	platform := strings.TrimSpace(target.Platform)
	itemID := strings.TrimSpace(target.ItemId)
	ruleType := strings.TrimSpace(target.RuleType)
	if platform == "" || itemID == "" {
		return false
	}

	var configIsGroup bool
	var streamTarget string
	switch ruleType {
	case "group":
		configIsGroup = true
		streamTarget = strings.TrimSpace(session.GroupID)
	case "private":
		configIsGroup = false
		streamTarget = strings.TrimSpace(session.UserID)
	default:
		return false
	}
	if configIsGroup != session.IsGroup {
		return false
	}
	if streamTarget == "" {
		return false
	}
	if !IsWildcardTarget(target) {
		return TargetMatchesSession(target, session)
	}
	platformMatches := platform == "*" || strings.TrimSpace(session.Platform) == platform
	itemMatches := itemID == "*" || streamTarget == itemID
	return platformMatches && itemMatches
}

// IsWildcardTarget platform 或 item_id 含通配
func IsWildcardTarget(target maibot.TargetItem) bool {
	return strings.TrimSpace(target.Platform) == "*" || strings.TrimSpace(target.ItemId) == "*"
}
