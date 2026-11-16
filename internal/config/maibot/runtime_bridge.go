package maibot

import (
	"strconv"
	"strings"

	"MaiGo/internal/config"

	"go.uber.org/zap"
)

// ApplyInstanceToRuntime 把 bot_config 中与 RuntimeConfig 明确重叠的字段合并进内存快照，不覆盖其它动态配置 section
func ApplyInstanceToRuntime(h *InstanceHolder) error {
	if h == nil {
		return nil
	}
	cfg := h.Config()
	if cfg == nil {
		return nil
	}
	return applyBotToRuntime(cfg.Bot)
}

// WireInstanceRuntimeBridge 首次桥接并在 bot_config 热重载后刷新
func WireInstanceRuntimeBridge(h *InstanceHolder) error {
	if h == nil {
		return nil
	}
	if err := ApplyInstanceToRuntime(h); err != nil {
		return err
	}
	h.RegisterReloadCallback(func(_ *InstanceConfig, scopes []ReloadScope) {
		botChanged := false
		for _, scope := range scopes {
			if scope == ReloadScopeBot {
				botChanged = true
				break
			}
		}
		if !botChanged {
			return
		}
		if err := ApplyInstanceToRuntime(h); err != nil {
			zap.L().Warn("bot_config 热重载后桥接 RuntimeConfig 失败，保留上一快照", zap.Error(err))
		}
	})
	return nil
}

func applyBotToRuntime(bot BotConfigFile) error {
	platform := strings.TrimSpace(bot.Bot.Platform)
	rt := bot.Chat.ReplyTiming
	pp := bot.ResponsePostProcess
	visual := bot.Visual

	return config.MergeInMemorySnapshot(func(cfg *config.RuntimeConfig) {
		mergeChatFromReplyTiming(cfg, rt, platform)
		mergeChatFromPostProcess(cfg, pp)
		mergeMediaFromVisual(cfg, visual)
	})
}

func mergeChatFromReplyTiming(cfg *config.RuntimeConfig, rt ChatReplyTimingConfig, platform string) {
	if rt.TalkValue >= 0 && rt.TalkValue <= 1 {
		cfg.Chat.TalkFrequency = rt.TalkValue
	}
	cfg.Chat.EnableTimeRules = len(rt.TalkValueRules) > 0
	if cfg.Chat.EnableTimeRules {
		cfg.Chat.TimeRules = talkRulesForRuntime(rt.TalkValueRules, platform)
	}
}

func mergeChatFromPostProcess(cfg *config.RuntimeConfig, pp ResponsePostProcessConfig) {
	// 与 speak_pipeline 的 legacyTyping 及 replyer TypingSpeed 缩放（/6）对齐 MaiBot response_post_process
	cfg.Chat.TypingSimulation = pp.EnableResponsePostProcess
	if pp.TypingSpeed > 0 {
		scaled := int(pp.TypingSpeed * 6)
		if scaled < 1 {
			scaled = 1
		}
		cfg.Chat.TypingSpeed = scaled
	}
}

func mergeMediaFromVisual(cfg *config.RuntimeConfig, visual VisualConfig) {
	if visual.MaxImageSizeMb > 0 {
		mb := int(visual.MaxImageSizeMb)
		if mb < 1 {
			mb = 1
		}
		cfg.Media.MaxBytesMB = mb
	}
}

func talkRulesForRuntime(rules []TalkRulesItem, platform string) []config.TimeRuleConfig {
	out := make([]config.TimeRuleConfig, 0, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.RuleType) != "group" {
			continue
		}
		rulePlatform := strings.TrimSpace(rule.Platform)
		if rulePlatform != "" && rulePlatform != "*" && rulePlatform != platform {
			continue
		}
		timeRange := strings.TrimSpace(rule.Time)
		if timeRange == "" || timeRange == "*" {
			continue
		}
		out = append(out, config.TimeRuleConfig{
			TimeRange: timeRange,
			GroupID:   parseTalkRuleGroupID(rule.ItemId),
			TalkValue: rule.Value,
		})
	}
	return out
}

func parseTalkRuleGroupID(itemID string) int64 {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" || itemID == "*" {
		return 0
	}
	gid, err := strconv.ParseInt(itemID, 10, 64)
	if err != nil {
		return 0
	}
	return gid
}
