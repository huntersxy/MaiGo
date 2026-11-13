package utils

// A_memorix 反馈纠错策略，对齐 Python core/utils/feedback_policy.py
// Python 侧从 global_config.a_memorix.integration 读取配置
// Go 侧显式注入 FeedbackIntegrationConfig，nil 字段表示使用 Python 默认值

import (
	"math"
	"strconv"
	"strings"
)

// FeedbackIntegrationConfig 反馈纠错的集成配置
// 指针字段为 nil 时等价于 Python getattr 的默认值
type FeedbackIntegrationConfig struct {
	FeedbackCorrectionEnabled                    *bool
	FeedbackCorrectionWindowHours                *float64
	FeedbackCorrectionCheckIntervalMinutes       *float64
	FeedbackCorrectionBatchSize                  *float64
	FeedbackCorrectionAutoApplyThreshold         *float64
	FeedbackCorrectionMaxFeedbackMessages        *float64
	FeedbackCorrectionPrefilterEnabled           *bool
	FeedbackCorrectionParagraphMarkEnabled       *bool
	FeedbackCorrectionParagraphHardFilterEnabled *bool
	FeedbackCorrectionProfileRefreshEnabled      *bool
	FeedbackCorrectionProfileForceRefreshOnRead  *bool
	FeedbackCorrectionEpisodeRebuildEnabled      *bool
	FeedbackCorrectionEpisodeQueryBlockEnabled   *bool
	FeedbackCorrectionReconcileIntervalMinutes   *float64
	FeedbackCorrectionReconcileBatchSize         *float64
	FuzzyModifyEnabled                           *bool
	FuzzyModifyAutoExecuteEnabled                *bool
	FuzzyModifyConfirmThreshold                  *float64
	FuzzyModifyCandidateLimit                    *float64
	FuzzyModifyMaxTargets                        *float64
	FuzzyModifyAllowGlobalScope                  *bool
}

// FeedbackSignalTokens 对齐 feedback_signal_tokens
func FeedbackSignalTokens() []string {
	return []string{"不对", "错了", "你记错", "记错了", "不是", "并不是", "纠正", "更正", "改成", "应该是", "实际是", "说反了"}
}

// FeedbackContainsSignal 对齐 feedback_contains_signal
func FeedbackContainsSignal(text string) bool {
	content := pyLower(pyStrip(text))
	if content == "" {
		return false
	}
	for _, token := range FeedbackSignalTokens() {
		if strings.Contains(content, token) {
			return true
		}
	}
	return false
}

// FeedbackNoise 对齐 feedback_noise
func FeedbackNoise(text string) bool {
	content := pyStrip(text)
	if content == "" {
		return true
	}
	if FeedbackContainsSignal(content) {
		return false
	}
	if len([]rune(content)) <= 2 {
		return true
	}
	markers := []string{"哈哈", "好的", "收到", "谢谢", "嗯嗯", "晚安", "早安", "拜拜", "在吗"}
	if len([]rune(content)) <= 8 {
		for _, marker := range markers {
			if strings.Contains(content, marker) {
				return true
			}
		}
	}
	return false
}

// FeedbackCfgEnabled 对齐 feedback_cfg_enabled
func FeedbackCfgEnabled(cfg *FeedbackIntegrationConfig) bool {
	if cfg == nil {
		return false
	}
	return feedbackBool(cfg.FeedbackCorrectionEnabled, false)
}

// FeedbackCfgWindowHours 对齐 feedback_cfg_window_hours
func FeedbackCfgWindowHours(cfg *FeedbackIntegrationConfig) float64 {
	value := feedbackFloatOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FeedbackCorrectionWindowHours }), 12.0)
	if value < 0.1 {
		return 0.1
	}
	return value
}

// FeedbackCfgCheckIntervalSeconds 对齐 feedback_cfg_check_interval_seconds
func FeedbackCfgCheckIntervalSeconds(cfg *FeedbackIntegrationConfig) float64 {
	minutes := feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FeedbackCorrectionCheckIntervalMinutes }), 30, 1)
	return float64(minutes) * 60.0
}

// FeedbackCfgBatchSize 对齐 feedback_cfg_batch_size
func FeedbackCfgBatchSize(cfg *FeedbackIntegrationConfig) int {
	return feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FeedbackCorrectionBatchSize }), 20, 1)
}

// FeedbackCfgAutoApplyThreshold 对齐 feedback_cfg_auto_apply_threshold
// 该字段没有 "or 默认值" 语义，显式配置 0 会保留
func FeedbackCfgAutoApplyThreshold(cfg *FeedbackIntegrationConfig) float64 {
	value := 0.85
	if raw := cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 {
		return c.FeedbackCorrectionAutoApplyThreshold
	}); raw != nil {
		value = *raw
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return value
}

// FeedbackCfgMaxMessages 对齐 feedback_cfg_max_messages
func FeedbackCfgMaxMessages(cfg *FeedbackIntegrationConfig) int {
	return feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FeedbackCorrectionMaxFeedbackMessages }), 30, 1)
}

// FeedbackCfgPrefilterEnabled 对齐 feedback_cfg_prefilter_enabled
func FeedbackCfgPrefilterEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionPrefilterEnabled }, true)
}

// FeedbackCfgParagraphMarkEnabled 对齐 feedback_cfg_paragraph_mark_enabled
func FeedbackCfgParagraphMarkEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionParagraphMarkEnabled }, true)
}

// FeedbackCfgParagraphHardFilterEnabled 对齐 feedback_cfg_paragraph_hard_filter_enabled
func FeedbackCfgParagraphHardFilterEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionParagraphHardFilterEnabled }, true)
}

// FeedbackCfgProfileRefreshEnabled 对齐 feedback_cfg_profile_refresh_enabled
func FeedbackCfgProfileRefreshEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionProfileRefreshEnabled }, true)
}

// FeedbackCfgProfileForceRefreshOnRead 对齐 feedback_cfg_profile_force_refresh_on_read
func FeedbackCfgProfileForceRefreshOnRead(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionProfileForceRefreshOnRead }, true)
}

// FeedbackCfgEpisodeRebuildEnabled 对齐 feedback_cfg_episode_rebuild_enabled
func FeedbackCfgEpisodeRebuildEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionEpisodeRebuildEnabled }, true)
}

// FeedbackCfgEpisodeQueryBlockEnabled 对齐 feedback_cfg_episode_query_block_enabled
func FeedbackCfgEpisodeQueryBlockEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FeedbackCorrectionEpisodeQueryBlockEnabled }, true)
}

// FeedbackCfgReconcileIntervalSeconds 对齐 feedback_cfg_reconcile_interval_seconds
func FeedbackCfgReconcileIntervalSeconds(cfg *FeedbackIntegrationConfig) float64 {
	minutes := feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FeedbackCorrectionReconcileIntervalMinutes }), 5, 1)
	return float64(minutes) * 60.0
}

// FeedbackCfgReconcileBatchSize 对齐 feedback_cfg_reconcile_batch_size
func FeedbackCfgReconcileBatchSize(cfg *FeedbackIntegrationConfig) int {
	return feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FeedbackCorrectionReconcileBatchSize }), 20, 1)
}

// FeedbackCfgWindowLabel 对齐 feedback_cfg_window_label
func FeedbackCfgWindowLabel(cfg *FeedbackIntegrationConfig) string {
	hours := FeedbackCfgWindowHours(cfg)
	// Python round() 为银行家舍入
	rounded := math.RoundToEven(hours)
	if math.Abs(hours-rounded) < 1e-9 {
		return strconv.Itoa(int(rounded)) + "h"
	}
	return strconv.FormatFloat(hours, 'f', 2, 64) + "h"
}

// FuzzyModifyCfgEnabled 对齐 fuzzy_modify_cfg_enabled
func FuzzyModifyCfgEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FuzzyModifyEnabled }, true)
}

// FuzzyModifyCfgAutoExecuteEnabled 对齐 fuzzy_modify_cfg_auto_execute_enabled
func FuzzyModifyCfgAutoExecuteEnabled(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FuzzyModifyAutoExecuteEnabled }, false)
}

// FuzzyModifyCfgConfirmThreshold 对齐 fuzzy_modify_cfg_confirm_threshold
func FuzzyModifyCfgConfirmThreshold(cfg *FeedbackIntegrationConfig) float64 {
	if raw := cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FuzzyModifyConfirmThreshold }); raw != nil {
		return *raw
	}
	return 0.85
}

// FuzzyModifyCfgCandidateLimit 对齐 fuzzy_modify_cfg_candidate_limit
func FuzzyModifyCfgCandidateLimit(cfg *FeedbackIntegrationConfig) int {
	return feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FuzzyModifyCandidateLimit }), 20, 1)
}

// FuzzyModifyCfgMaxTargets 对齐 fuzzy_modify_cfg_max_targets
func FuzzyModifyCfgMaxTargets(cfg *FeedbackIntegrationConfig) int {
	return feedbackIntOr(cfgValue(cfg, func(c *FeedbackIntegrationConfig) *float64 { return c.FuzzyModifyMaxTargets }), 5, 1)
}

// FuzzyModifyCfgAllowGlobalScope 对齐 fuzzy_modify_cfg_allow_global_scope
func FuzzyModifyCfgAllowGlobalScope(cfg *FeedbackIntegrationConfig) bool {
	return feedbackCfgBool(cfg, func(c *FeedbackIntegrationConfig) *bool { return c.FuzzyModifyAllowGlobalScope }, false)
}

// cfgValue 读取配置字段取值函数，cfg 为 nil 时返回 nil
func cfgValue(cfg *FeedbackIntegrationConfig, get func(*FeedbackIntegrationConfig) *float64) *float64 {
	if cfg == nil {
		return nil
	}
	return get(cfg)
}

// feedbackCfgBool 读取布尔配置并应用默认值
func feedbackCfgBool(cfg *FeedbackIntegrationConfig, get func(*FeedbackIntegrationConfig) *bool, defaultValue bool) bool {
	if cfg == nil {
		return defaultValue
	}
	return feedbackBool(get(cfg), defaultValue)
}

// feedbackBool 对齐 getattr 的布尔默认值
func feedbackBool(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

// feedbackFloatOr 对齐 float(getattr(...) or default)
func feedbackFloatOr(value *float64, defaultValue float64) float64 {
	if value == nil || *value == 0 {
		return defaultValue
	}
	return *value
}

// feedbackIntOr 对齐 max(minimum, int(getattr(...) or default))
func feedbackIntOr(value *float64, defaultValue int, minimum int) int {
	number := float64(defaultValue)
	if value != nil && *value != 0 {
		number = *value
	}
	result := int(math.Trunc(number))
	if result < minimum {
		return minimum
	}
	return result
}
