package runtime

import (
	"strconv"
	"strings"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/prompt"
)

// configureMaisakaChatLoop 从实例配置注入 Planner 模板需要的名字、人格与聊天注意事项
func (a *Agent) configureMaisakaChatLoop(loop *maisaka.ChatLoopService, isGroup bool, kind string, targetID int64) {
	if loop == nil {
		return
	}
	loop.Locale = prompt.InitialLocale()
	loop.PlannerMessageSteering = a.plannerMessageSteeringEnabled()
	loop.MaxImageSide = replyer.ContextImageMaxSide("")
	bot := a.instanceBotConfig()
	if bot == nil {
		return
	}
	loop.MaxImageSide = replyer.ContextImageMaxSide(bot.Visual.ImageQuality)
	loop.MaxImageNum = bot.Visual.MaxImageNum
	if isGroup {
		loop.UseTokenLimit = bot.Chat.UseTokenLimit
		loop.MaxContextTokens = bot.Chat.MaxContextTokens
	} else {
		loop.UseTokenLimit = bot.Chat.PrivateUseTokenLimit
		loop.MaxContextTokens = bot.Chat.PrivateMaxContextTokens
	}
	loop.LimitImages = a.limitPlannerImages
	loop.PlannerSubagent = bot.Capabilities.PlannerSubagent
	loop.Personality = strings.TrimSpace(bot.Personality.Personality)
	loop.BehaviorStyle = strings.TrimSpace(bot.Personality.BehaviorStyle)
	if isGroup {
		loop.GroupChatAttention = strings.TrimSpace(bot.Chat.ReplyStyle.GroupChatPrompt)
	} else {
		loop.GroupChatAttention = strings.TrimSpace(bot.Chat.ReplyStyle.PrivateChatPrompts)
	}
	loop.CurrentChatPrompt = a.chatPromptForSession(bot.Chat.ReplyStyle.ChatPrompts, kind, targetID, isGroup)
	aliases := make([]string, 0, len(bot.Bot.AliasNames))
	for _, alias := range bot.Bot.AliasNames {
		if text := strings.TrimSpace(alias); text != "" {
			aliases = append(aliases, text)
		}
	}
	if len(aliases) == 0 {
		return
	}
	switch prompt.InitialLocale() {
	case "en-US":
		loop.BotAliasNotice = "; some people also call you " + strings.Join(aliases, ", ")
	case "ja-JP":
		loop.BotAliasNotice = "（" + strings.Join(aliases, "、") + "と呼ぶ人もいます）"
	default:
		loop.BotAliasNotice = "，还有人叫你" + strings.Join(aliases, "、")
	}
}

// chatPromptForSession 从 chat_prompts 配置中匹配当前聊天的额外注意事项
// 对齐 ChatConfigUtils.get_chat_prompt_for_chat；多条命中时按配置顺序用换行拼接
func (a *Agent) chatPromptForSession(items []any, kind string, targetID int64, isGroup bool) string {
	if len(items) == 0 || targetID <= 0 {
		return ""
	}
	platform := strings.TrimSpace(a.conversationPlatform(kind, targetID))
	target := strconv.FormatInt(targetID, 10)
	return strings.Join(matchChatPromptContents(items, platform, target, isGroup), "\n")
}

// matchChatPromptContents 对齐 _iter_matching_chat_prompts：
// rule_type 必须是 group/private；platform/item_id/prompt 均非空；精确相等，无通配
func matchChatPromptContents(items []any, platform, targetID string, isGroup bool) []string {
	contents := make([]string, 0, len(items))
	for _, raw := range items {
		entryPlatform, itemID, ruleType, content := parseChatPromptItem(raw)
		if entryPlatform == "" || itemID == "" || content == "" {
			continue
		}
		switch ruleType {
		case "group":
			if !isGroup {
				continue
			}
		case "private":
			if isGroup {
				continue
			}
		default:
			continue
		}
		if entryPlatform != platform || itemID != targetID {
			continue
		}
		contents = append(contents, content)
	}
	return contents
}

func parseChatPromptItem(raw any) (platform, itemID, ruleType, content string) {
	switch value := raw.(type) {
	case string:
		parts := strings.SplitN(value, ":", 4)
		if len(parts) != 4 {
			return "", "", "", ""
		}
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.ToLower(strings.TrimSpace(parts[2])), strings.TrimSpace(parts[3])
	case map[string]any:
		return stringValueOf(value["platform"]), stringValueOf(value["item_id"]), strings.ToLower(firstNonEmptyString(stringValueOf(value["rule_type"]), stringValueOf(value["type"]))), stringValueOf(value["prompt"])
	default:
		return "", "", "", ""
	}
}
