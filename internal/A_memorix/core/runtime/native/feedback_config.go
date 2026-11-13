package native

// 反馈纠错集成配置装配：从内核插件配置读取 a_memorix.integration.*
// 与 Python feedback_policy.py 的 getattr 默认值保持一致

import (
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/utils"
)

// feedbackIntegrationConfig 由内核配置构造反馈纠错集成配置
// 每次调用都重新读取配置，内核热重载后无需重新注册闭包
func feedbackIntegrationConfig(kernel *memruntime.Kernel) *utils.FeedbackIntegrationConfig {
	if kernel == nil {
		return nil
	}
	cfg := kernel.Cfg
	boolValue := func(key string, defaultValue bool) *bool {
		value := memruntime.PyTruthy(cfg(key, defaultValue))
		return &value
	}
	floatValue := func(key string, defaultValue float64) *float64 {
		value, err := memruntime.PyFloat(cfg(key, defaultValue))
		if err != nil {
			value = defaultValue
		}
		return &value
	}
	// Python 对窗口/间隔/批大小使用 "or 默认值"，显式 0 视为未配置
	positiveFloatValue := func(key string, defaultValue float64) *float64 {
		value, err := memruntime.PyFloat(cfg(key, defaultValue))
		if err != nil || value == 0 {
			return nil
		}
		return &value
	}
	return &utils.FeedbackIntegrationConfig{
		FeedbackCorrectionEnabled:                    boolValue("integration.feedback_correction_enabled", false),
		FeedbackCorrectionWindowHours:                positiveFloatValue("integration.feedback_correction_window_hours", 12.0),
		FeedbackCorrectionCheckIntervalMinutes:       positiveFloatValue("integration.feedback_correction_check_interval_minutes", 30),
		FeedbackCorrectionBatchSize:                  positiveFloatValue("integration.feedback_correction_batch_size", 20),
		FeedbackCorrectionAutoApplyThreshold:         floatValue("integration.feedback_correction_auto_apply_threshold", 0.85),
		FeedbackCorrectionMaxFeedbackMessages:        positiveFloatValue("integration.feedback_correction_max_feedback_messages", 30),
		FeedbackCorrectionPrefilterEnabled:           boolValue("integration.feedback_correction_prefilter_enabled", true),
		FeedbackCorrectionParagraphMarkEnabled:       boolValue("integration.feedback_correction_paragraph_mark_enabled", true),
		FeedbackCorrectionParagraphHardFilterEnabled: boolValue("integration.feedback_correction_paragraph_hard_filter_enabled", true),
		FeedbackCorrectionProfileRefreshEnabled:      boolValue("integration.feedback_correction_profile_refresh_enabled", true),
		FeedbackCorrectionProfileForceRefreshOnRead:  boolValue("integration.feedback_correction_profile_force_refresh_on_read", true),
		FeedbackCorrectionEpisodeRebuildEnabled:      boolValue("integration.feedback_correction_episode_rebuild_enabled", true),
		FeedbackCorrectionEpisodeQueryBlockEnabled:   boolValue("integration.feedback_correction_episode_query_block_enabled", true),
		FeedbackCorrectionReconcileIntervalMinutes:   positiveFloatValue("integration.feedback_correction_reconcile_interval_minutes", 5),
		FeedbackCorrectionReconcileBatchSize:         positiveFloatValue("integration.feedback_correction_reconcile_batch_size", 20),
		FuzzyModifyEnabled:                           boolValue("integration.fuzzy_modify_enabled", true),
		FuzzyModifyAutoExecuteEnabled:                boolValue("integration.fuzzy_modify_auto_execute_enabled", false),
		FuzzyModifyConfirmThreshold:                  floatValue("integration.fuzzy_modify_confirm_threshold", 0.85),
		FuzzyModifyCandidateLimit:                    positiveFloatValue("integration.fuzzy_modify_candidate_limit", 20),
		FuzzyModifyMaxTargets:                        positiveFloatValue("integration.fuzzy_modify_max_targets", 5),
		FuzzyModifyAllowGlobalScope:                  boolValue("integration.fuzzy_modify_allow_global_scope", false),
	}
}
