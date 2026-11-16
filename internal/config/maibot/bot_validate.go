package maibot

// Bot 配置约束校验，对齐 official_configs.py 的 Field(ge/le/Literal/pattern) 与 model_post_init

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	allowedDebounceModes = map[string]struct{}{"off": {}, "fast": {}, "natural": {}, "steady": {}}
	allowedQuoteFreq     = map[string]struct{}{"never": {}, "occasionally": {}, "often": {}}
)

// ValidateBotConfig 校验 bot_config 中 Python 会在加载期拒绝的非法值
func ValidateBotConfig(cfg BotConfigFile) error {
	if err := validatePersonality(cfg.Personality); err != nil {
		return err
	}
	if err := validateChatConfig(cfg.Chat); err != nil {
		return err
	}
	if err := validateMessageReceive(cfg.MessageReceive); err != nil {
		return err
	}
	if err := validateAMemorix(cfg.AMemorix); err != nil {
		return err
	}
	if err := validateAttentionDrift(cfg.Experimental.GroupAttentionDrift); err != nil {
		return fmt.Errorf("experimental.group_attention_drift: %w", err)
	}
	if err := validateAttentionDrift(cfg.Experimental.PrivateAttentionDrift); err != nil {
		return fmt.Errorf("experimental.private_attention_drift: %w", err)
	}
	return nil
}

func validateAttentionDrift(raw any) error {
	value, ok := raw.(map[string]any)
	if !ok || value == nil {
		return nil
	}
	if text, ok := value["drift_level"].(string); ok && strings.TrimSpace(text) != "" {
		switch strings.TrimSpace(text) {
		case "subtle", "active", "scattered", "wild":
		default:
			return fmt.Errorf("drift_level 非法: %s", text)
		}
	}
	if text, ok := value["anchor_policy"].(string); ok && strings.TrimSpace(text) != "" {
		switch strings.TrimSpace(text) {
		case "strict", "balanced", "loose":
		default:
			return fmt.Errorf("anchor_policy 非法: %s", text)
		}
	}
	if text, ok := value["reaction_style"].(string); ok && strings.TrimSpace(text) != "" {
		switch strings.TrimSpace(text) {
		case "reserved", "natural", "lively":
		default:
			return fmt.Errorf("reaction_style 非法: %s", text)
		}
	}
	return nil
}

func validatePersonality(cfg PersonalityConfig) error {
	names := make(map[string]struct{}, len(cfg.Profiles))
	for index, profile := range cfg.Profiles {
		name := strings.TrimSpace(profile.Name)
		if name == "" || !regexp.MustCompile(`\S`).MatchString(profile.Name) {
			return fmt.Errorf("personality.profiles[%d] 的名称不能为空", index)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("备选人设名称不能重复: %q", name)
		}
		names[name] = struct{}{}
		if profile.MultipleProbability < 0 || profile.MultipleProbability > 1 {
			return fmt.Errorf("personality.profiles[%d].multiple_probability 必须在 0 到 1 之间", index)
		}
	}
	if cfg.MultipleProbability < 0 || cfg.MultipleProbability > 1 {
		return fmt.Errorf("personality.multiple_probability 必须在 0 到 1 之间")
	}
	return nil
}

func validateChatConfig(cfg ChatConfig) error {
	if cfg.ReplyTiming.TalkValue < 0 || cfg.ReplyTiming.TalkValue > 1 {
		return fmt.Errorf("chat.reply_timing.talk_value 必须在 0 到 1 之间")
	}
	if mode := strings.TrimSpace(cfg.ReplyTiming.MessageDebounceMode); mode != "" {
		if _, ok := allowedDebounceModes[mode]; !ok {
			return fmt.Errorf("chat.reply_timing.message_debounce_mode 非法: %s", mode)
		}
	}
	if mode := strings.TrimSpace(cfg.ReplyTiming.PrivateMessageDebounceMode); mode != "" {
		if _, ok := allowedDebounceModes[mode]; !ok {
			return fmt.Errorf("chat.reply_timing.private_message_debounce_mode 非法: %s", mode)
		}
	}
	if value := strings.TrimSpace(cfg.ReplyStyle.GroupQuoteFrequency); value != "" {
		if _, ok := allowedQuoteFreq[value]; !ok {
			return fmt.Errorf("chat.reply_style.group_quote_frequency 非法: %s", value)
		}
	}
	if value := strings.TrimSpace(cfg.ReplyStyle.PrivateQuoteFrequency); value != "" {
		if _, ok := allowedQuoteFreq[value]; !ok {
			return fmt.Errorf("chat.reply_style.private_quote_frequency 非法: %s", value)
		}
	}
	for index, rule := range cfg.ReplyTiming.TalkValueRules {
		if strings.TrimSpace(rule.RuleType) != "group" {
			continue
		}
		platformGlobal := strings.TrimSpace(rule.Platform) == "" || strings.TrimSpace(rule.Platform) == "*"
		itemGlobal := strings.TrimSpace(rule.ItemId) == "" || strings.TrimSpace(rule.ItemId) == "*"
		if platformGlobal && itemGlobal {
			return fmt.Errorf("chat.reply_timing.talk_value_rules[%d] 必须指定平台或群聊 ID；所有群的默认频率请使用总体发言频率", index)
		}
	}
	return nil
}

func validateMessageReceive(cfg MessageReceiveConfig) error {
	for _, raw := range anyStringList(cfg.BanMsgsRegex) {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("Invalid regex pattern in ban_msgs_regex: '%s'", pattern)
		}
	}
	return nil
}

func validateAMemorix(cfg AMemorixConfig) error {
	if value := strings.TrimSpace(strings.ToLower(cfg.Embedding.QuantizationType)); value != "" && value != "int8" {
		return fmt.Errorf("a_memorix.embedding.quantization_type 仅允许 int8")
	}
	if value := strings.TrimSpace(strings.ToLower(cfg.Retrieval.Sparse.Backend)); value != "" && value != "fts5" {
		return fmt.Errorf("a_memorix.advanced.sparse.backend 仅允许 fts5")
	}
	if cfg.Threshold.MaxThreshold != 0 || cfg.Threshold.MinThreshold != 0 {
		if cfg.Threshold.MinThreshold >= cfg.Threshold.MaxThreshold {
			return fmt.Errorf("a_memorix.threshold.min_threshold 必须小于 max_threshold")
		}
	}
	if cfg.Memory.Enabled {
		if cfg.Memory.ReviveThreshold <= cfg.Memory.PruneThreshold {
			return fmt.Errorf("a_memorix.memory.revive_threshold 必须大于 prune_threshold")
		}
	}
	integration := cfg.Integration
	if integration.FuzzyModifyConfirmThreshold < 0 || integration.FuzzyModifyConfirmThreshold > 1 {
		return fmt.Errorf("a_memorix.integration.fuzzy_modify_confirm_threshold 必须在 [0, 1] 之间")
	}
	if integration.FuzzyModifyCandidateLimit < 1 {
		return fmt.Errorf("a_memorix.integration.fuzzy_modify_candidate_limit 必须至少为 1")
	}
	if integration.FuzzyModifyMaxTargets < 1 {
		return fmt.Errorf("a_memorix.integration.fuzzy_modify_max_targets 必须至少为 1")
	}
	if integration.FeedbackCorrectionEnabled {
		if integration.FeedbackCorrectionWindowHours <= 0 {
			return fmt.Errorf("a_memorix.integration.feedback_correction_window_hours 必须大于 0")
		}
		if integration.FeedbackCorrectionCheckIntervalMinutes < 1 {
			return fmt.Errorf("a_memorix.integration.feedback_correction_check_interval_minutes 必须至少为 1")
		}
		if integration.FeedbackCorrectionBatchSize < 1 {
			return fmt.Errorf("a_memorix.integration.feedback_correction_batch_size 必须至少为 1")
		}
		if integration.FeedbackCorrectionAutoApplyThreshold < 0 || integration.FeedbackCorrectionAutoApplyThreshold > 1 {
			return fmt.Errorf("a_memorix.integration.feedback_correction_auto_apply_threshold 必须在 [0, 1] 之间")
		}
		if integration.FeedbackCorrectionMaxFeedbackMessages < 1 {
			return fmt.Errorf("a_memorix.integration.feedback_correction_max_feedback_messages 必须至少为 1")
		}
	}
	return nil
}

func anyStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case string:
		return []string{typed}
	default:
		return nil
	}
}
