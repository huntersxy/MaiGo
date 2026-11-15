package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"MaiGo/internal/common/data_models/messagecomponent"
)

// MentionDetectInput 对齐 is_mentioned_bot_in_message 所需的最小输入面。
type MentionDetectInput struct {
	Text             string
	Platform         string
	AdditionalConfig map[string]any
	IsAt             bool
	IsMentioned      bool
	RawMessage       *messagecomponent.MessageSequence
	BotAccounts      []string
	Nickname         string
	AliasNames       []string
	InevitableAt     bool
	MentionedReply   bool
	IsBotSelf        func(platform, userID string) bool
}

// IsMentionedBotInMessage 对齐 MaiBot chat/utils/utils.py:is_mentioned_bot_in_message。
// 返回 (is_mentioned, is_at, reply_probability_boost)。
func IsMentionedBotInMessage(in MentionDetectInput) (bool, bool, float64) {
	text := in.Text
	platform := strings.ToLower(strings.TrimSpace(in.Platform))
	accounts := uniqueNonEmpty(in.BotAccounts)
	keywords := make([]string, 0, 1+len(in.AliasNames))
	if nick := strings.TrimSpace(in.Nickname); nick != "" {
		keywords = append(keywords, nick)
	}
	for _, alias := range in.AliasNames {
		if v := strings.TrimSpace(alias); v != "" {
			keywords = append(keywords, v)
		}
	}

	replyProbability := 0.0
	isAt := false
	isMentioned := false

	// 1) additional_config.at_bot / is_mentioned
	if cfg := in.AdditionalConfig; cfg != nil {
		if truthy(cfg["at_bot"]) {
			isMentioned = true
			isAt = true
		}
		if raw, ok := cfg["is_mentioned"]; ok && !isNilOrEmpty(raw) {
			isMentioned = true
			if _, isBool := raw.(bool); !isBool {
				if boost, ok := asFloat(raw); ok {
					replyProbability = boost
				}
			}
		}
	}

	// 2) 上游已设置的 is_at / is_mentioned
	if in.IsAt {
		isAt = true
		isMentioned = true
	}
	if in.IsMentioned {
		isMentioned = true
	}

	// 3) message_segment mention_bot / seglist 递归
	if hasMentionBotSegment(in.RawMessage) {
		isAt = true
		isMentioned = true
	}

	// 4) 结构化 @ 组件指向 bot
	if !isAt && hasAtComponentTargetingBot(in.RawMessage, platform, in.IsBotSelf) {
		isAt = true
		isMentioned = true
	}

	// 5) 文本 @ 账号
	if len(accounts) > 0 && !isAt && !isMentioned {
		for _, account := range accounts {
			var pattern *regexp.Regexp
			if platform == "qq" {
				pattern = regexp.MustCompile(`(?i)@<(.+?):` + regexp.QuoteMeta(account) + `>`)
			} else {
				pattern = regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(account) + `(\b|$)`)
			}
			if pattern.MatchString(text) {
				isAt = true
				isMentioned = true
				break
			}
		}
	}

	// 6) 回复标记
	if !isMentioned {
		if regexp.MustCompile(`\[回复 .*?\(你\)：`).MatchString(text) ||
			regexp.MustCompile(`\[回复 .*?（你）：`).MatchString(text) {
			isMentioned = true
		} else if len(accounts) > 0 {
			for _, account := range accounts {
				re := regexp.QuoteMeta(account)
				if regexp.MustCompile(`\[回复 (.+?)\(` + re + `\)：(.+?)\]，说：`).MatchString(text) {
					isMentioned = true
					break
				}
				// Python 使用 lookahead；Go RE2 无 lookahead，展开为显式 :account>
				if regexp.MustCompile(`\[回复<(.+?):` + re + `>：(.+?)\]，说：`).MatchString(text) {
					isMentioned = true
					break
				}
			}
		}
	}

	// 7) 昵称/别名（去掉 @/回复标记后匹配）
	if !isMentioned && len(keywords) > 0 {
		msgContent := text
		msgContent = regexp.MustCompile(`@(.+?)（(\d+)）`).ReplaceAllString(msgContent, "")
		msgContent = regexp.MustCompile(`@<(.+?):(\d+)>`).ReplaceAllString(msgContent, "")
		msgContent = regexp.MustCompile(`\[回复 (.+?)\(((\d+)|未知id|你)\)：(.+?)\]，说：`).ReplaceAllString(msgContent, "")
		msgContent = regexp.MustCompile(`\[回复<(.+?):(\d+)>：(.+?)\]，说：`).ReplaceAllString(msgContent, "")
		for _, kw := range keywords {
			if kw != "" && strings.Contains(msgContent, kw) {
				isMentioned = true
				break
			}
		}
	}

	// 8) 概率设置
	if isAt && in.InevitableAt {
		replyProbability = 1.0
	} else if isMentioned && in.MentionedReply {
		if replyProbability < 1.0 {
			replyProbability = 1.0
		}
	}
	return isMentioned, isAt, replyProbability
}

func hasMentionBotSegment(seq *messagecomponent.MessageSequence) bool {
	if seq == nil {
		return false
	}
	return walkMentionBot(seq.Components)
}

func walkMentionBot(components []messagecomponent.Component) bool {
	for _, raw := range components {
		if raw == nil {
			continue
		}
		switch typed := raw.(type) {
		case messagecomponent.DictComponent:
			if strings.EqualFold(typed.FormatName(), "mention_bot") {
				return true
			}
			if strings.EqualFold(strings.TrimSpace(typed.Type), "seglist") {
				if nested := dictNestedComponents(typed.Data); walkMentionBot(nested) {
					return true
				}
			}
		case *messagecomponent.DictComponent:
			if typed != nil && walkMentionBot([]messagecomponent.Component{*typed}) {
				return true
			}
		default:
			if strings.EqualFold(raw.FormatName(), "mention_bot") {
				return true
			}
		}
	}
	return false
}

func dictNestedComponents(data map[string]any) []messagecomponent.Component {
	if data == nil {
		return nil
	}
	raw, ok := data["items"].([]any)
	if !ok {
		raw, ok = data["data"].([]any)
	}
	if !ok {
		return nil
	}
	out := make([]messagecomponent.Component, 0, len(raw))
	for _, item := range raw {
		if child, ok := item.(messagecomponent.Component); ok {
			out = append(out, child)
		}
	}
	return out
}

func hasAtComponentTargetingBot(seq *messagecomponent.MessageSequence, platform string, isBotSelf func(string, string) bool) bool {
	if seq == nil || isBotSelf == nil {
		return false
	}
	for _, raw := range seq.Components {
		switch typed := raw.(type) {
		case messagecomponent.AtComponent:
			if isBotSelf(platform, typed.TargetUserID) {
				return true
			}
		case *messagecomponent.AtComponent:
			if typed != nil && isBotSelf(platform, typed.TargetUserID) {
				return true
			}
		}
	}
	return false
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		s := strings.TrimSpace(typed)
		return s != "" && !strings.EqualFold(s, "false") && s != "0"
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return fmt.Sprint(value) != "" && fmt.Sprint(value) != "0" && fmt.Sprint(value) != "false"
	}
}

func isNilOrEmpty(value any) bool {
	if value == nil {
		return true
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
