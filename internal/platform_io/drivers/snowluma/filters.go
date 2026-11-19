package snowluma

import (
	"regexp"
	"strings"
)

// MessageFilters 入站过滤骨架（P0 stub，可编译）
type MessageFilters struct {
	banUsers           map[string]struct{}
	banQQBot           bool
	regexEnabled       bool
	regexMode          string
	regexPatterns      []*regexp.Regexp
	showDropped        bool
	ignoreSelfMessage  bool
}

func NewMessageFilters(settings Settings) *MessageFilters {
	f := &MessageFilters{
		banUsers:          map[string]struct{}{},
		banQQBot:          settings.Filters.BanQQBot,
		regexEnabled:      settings.Filters.RegexFilterEnabled,
		regexMode:         settings.Filters.RegexFilterMode,
		showDropped:       settings.Filters.RegexFilterShowDropped,
		ignoreSelfMessage: settings.Debug.IgnoreSelfMessage,
	}
	for _, id := range settings.Filters.BanUserID {
		if text := strings.TrimSpace(id); text != "" {
			f.banUsers[text] = struct{}{}
		}
	}
	for _, pattern := range settings.Filters.RegexFilterPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		f.regexPatterns = append(f.regexPatterns, compiled)
	}
	return f
}

// ShouldDrop 返回是否丢弃；reason 仅诊断
func (f *MessageFilters) ShouldDrop(userID, plainText string, isBot bool, isSelf bool) (bool, string) {
	if f == nil {
		return false, ""
	}
	if f.ignoreSelfMessage && isSelf {
		return true, "ignore_self_message"
	}
	if _, banned := f.banUsers[userID]; banned {
		return true, "ban_user_id"
	}
	if f.banQQBot && isBot {
		return true, "ban_qq_bot"
	}
	if !f.regexEnabled || len(f.regexPatterns) == 0 {
		return false, ""
	}
	matched := false
	for _, pattern := range f.regexPatterns {
		if pattern.MatchString(plainText) {
			matched = true
			break
		}
	}
	if f.regexMode == "whitelist" {
		if !matched {
			return true, "regex_whitelist"
		}
		return false, ""
	}
	if matched {
		return true, "regex_blacklist"
	}
	return false, ""
}
