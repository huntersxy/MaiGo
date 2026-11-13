package memruntime

// 内核工具桥接，对齐 sdk_memory_kernel.py 中的静态工具与策略转发
// 说明：Python 直接调用 core/utils 的自由函数，Go 侧统一经由 utils 包转发

import (
	"time"

	"maibot-go/internal/A_memorix/core/utils"
)

// CoerceDatetime 对齐 _coerce_datetime
func (k *Kernel) CoerceDatetime(value any) *time.Time { return utils.CoerceDatetime(value) }

// SafeJSONLoads 对齐 _safe_json_loads
func (k *Kernel) SafeJSONLoads(raw any) map[string]any { return utils.SafeJSONLoads(raw) }

// FeedbackSignalTokens 对齐 _feedback_signal_tokens
func (k *Kernel) FeedbackSignalTokens() []string { return utils.FeedbackSignalTokens() }

// FeedbackContainsSignal 对齐 _feedback_contains_signal
func (k *Kernel) FeedbackContainsSignal(text string) bool { return utils.FeedbackContainsSignal(text) }

// FeedbackNoise 对齐 _feedback_noise
func (k *Kernel) FeedbackNoise(text string) bool { return utils.FeedbackNoise(text) }

// FeedbackCfgEnabled 对齐 _feedback_cfg_enabled
func (k *Kernel) FeedbackCfgEnabled() bool {
	return utils.FeedbackCfgEnabled(k.feedbackIntegration)
}

// FeedbackCfgWindowHours 对齐 _feedback_cfg_window_hours
func (k *Kernel) FeedbackCfgWindowHours() float64 {
	return utils.FeedbackCfgWindowHours(k.feedbackIntegration)
}

// FeedbackCfgCheckIntervalSeconds 对齐 _feedback_cfg_check_interval_seconds
func (k *Kernel) FeedbackCfgCheckIntervalSeconds() float64 {
	return utils.FeedbackCfgCheckIntervalSeconds(k.feedbackIntegration)
}

// FeedbackCfgBatchSize 对齐 _feedback_cfg_batch_size
func (k *Kernel) FeedbackCfgBatchSize() int {
	return utils.FeedbackCfgBatchSize(k.feedbackIntegration)
}

// FeedbackCfgAutoApplyThreshold 对齐 _feedback_cfg_auto_apply_threshold
func (k *Kernel) FeedbackCfgAutoApplyThreshold() float64 {
	return utils.FeedbackCfgAutoApplyThreshold(k.feedbackIntegration)
}

// FeedbackCfgMaxMessages 对齐 _feedback_cfg_max_messages
func (k *Kernel) FeedbackCfgMaxMessages() int {
	return utils.FeedbackCfgMaxMessages(k.feedbackIntegration)
}

// FeedbackCfgPrefilterEnabled 对齐 _feedback_cfg_prefilter_enabled
func (k *Kernel) FeedbackCfgPrefilterEnabled() bool {
	return utils.FeedbackCfgPrefilterEnabled(k.feedbackIntegration)
}

// FeedbackCfgParagraphMarkEnabled 对齐 _feedback_cfg_paragraph_mark_enabled
func (k *Kernel) FeedbackCfgParagraphMarkEnabled() bool {
	return utils.FeedbackCfgParagraphMarkEnabled(k.feedbackIntegration)
}

// FeedbackCfgParagraphHardFilterEnabled 对齐 _feedback_cfg_paragraph_hard_filter_enabled
func (k *Kernel) FeedbackCfgParagraphHardFilterEnabled() bool {
	return utils.FeedbackCfgParagraphHardFilterEnabled(k.feedbackIntegration)
}

// FeedbackCfgProfileRefreshEnabled 对齐 _feedback_cfg_profile_refresh_enabled
func (k *Kernel) FeedbackCfgProfileRefreshEnabled() bool {
	return utils.FeedbackCfgProfileRefreshEnabled(k.feedbackIntegration)
}

// FeedbackCfgProfileForceRefreshOnRead 对齐 _feedback_cfg_profile_force_refresh_on_read
func (k *Kernel) FeedbackCfgProfileForceRefreshOnRead() bool {
	return utils.FeedbackCfgProfileForceRefreshOnRead(k.feedbackIntegration)
}

// FeedbackCfgEpisodeRebuildEnabled 对齐 _feedback_cfg_episode_rebuild_enabled
func (k *Kernel) FeedbackCfgEpisodeRebuildEnabled() bool {
	return utils.FeedbackCfgEpisodeRebuildEnabled(k.feedbackIntegration)
}

// FeedbackCfgEpisodeQueryBlockEnabled 对齐 _feedback_cfg_episode_query_block_enabled
func (k *Kernel) FeedbackCfgEpisodeQueryBlockEnabled() bool {
	return utils.FeedbackCfgEpisodeQueryBlockEnabled(k.feedbackIntegration)
}

// FeedbackCfgReconcileIntervalSeconds 对齐 _feedback_cfg_reconcile_interval_seconds
func (k *Kernel) FeedbackCfgReconcileIntervalSeconds() float64 {
	return utils.FeedbackCfgReconcileIntervalSeconds(k.feedbackIntegration)
}

// FeedbackCfgReconcileBatchSize 对齐 _feedback_cfg_reconcile_batch_size
func (k *Kernel) FeedbackCfgReconcileBatchSize() int {
	return utils.FeedbackCfgReconcileBatchSize(k.feedbackIntegration)
}

// FeedbackCfgWindowLabel 对齐 _feedback_cfg_window_label
func (k *Kernel) FeedbackCfgWindowLabel() string {
	return utils.FeedbackCfgWindowLabel(k.feedbackIntegration)
}

// FuzzyModifyCfgEnabled 对齐 fuzzy_modify_cfg_enabled
func (k *Kernel) FuzzyModifyCfgEnabled() bool {
	return utils.FuzzyModifyCfgEnabled(k.feedbackIntegration)
}

// FuzzyModifyCfgAutoExecuteEnabled 对齐 fuzzy_modify_cfg_auto_execute_enabled
func (k *Kernel) FuzzyModifyCfgAutoExecuteEnabled() bool {
	return utils.FuzzyModifyCfgAutoExecuteEnabled(k.feedbackIntegration)
}

// FuzzyModifyCfgConfirmThreshold 对齐 fuzzy_modify_cfg_confirm_threshold
func (k *Kernel) FuzzyModifyCfgConfirmThreshold() float64 {
	return utils.FuzzyModifyCfgConfirmThreshold(k.feedbackIntegration)
}

// FuzzyModifyCfgCandidateLimit 对齐 fuzzy_modify_cfg_candidate_limit
func (k *Kernel) FuzzyModifyCfgCandidateLimit() int {
	return utils.FuzzyModifyCfgCandidateLimit(k.feedbackIntegration)
}

// FuzzyModifyCfgMaxTargets 对齐 fuzzy_modify_cfg_max_targets
func (k *Kernel) FuzzyModifyCfgMaxTargets() int {
	return utils.FuzzyModifyCfgMaxTargets(k.feedbackIntegration)
}

// FuzzyModifyCfgAllowGlobalScope 对齐 fuzzy_modify_cfg_allow_global_scope
func (k *Kernel) FuzzyModifyCfgAllowGlobalScope() bool {
	return utils.FuzzyModifyCfgAllowGlobalScope(k.feedbackIntegration)
}

// ShouldAutoEnqueueEpisode 对齐 _should_auto_enqueue_episode
func (k *Kernel) ShouldAutoEnqueueEpisode(sourceType string) bool {
	return utils.ShouldAutoEnqueueEpisode(k.configGetter(), sourceType)
}

// PersonProfileRefreshQueueIntervalSeconds 对齐 _person_profile_refresh_queue_interval_seconds
func (k *Kernel) PersonProfileRefreshQueueIntervalSeconds() float64 {
	return utils.PersonProfileRefreshQueueIntervalSeconds(k.configGetter())
}

// PersonProfileRefreshQueueBatchSize 对齐 _person_profile_refresh_queue_batch_size
func (k *Kernel) PersonProfileRefreshQueueBatchSize() int {
	return utils.PersonProfileRefreshQueueBatchSize(k.configGetter())
}

// PersonProfileRefreshDebounceSeconds 对齐 _person_profile_refresh_debounce_seconds
func (k *Kernel) PersonProfileRefreshDebounceSeconds() float64 {
	return utils.PersonProfileRefreshDebounceSeconds(k.configGetter())
}

// PersonProfileRefreshRetryBackoffSeconds 对齐 _person_profile_refresh_retry_backoff_seconds
func (k *Kernel) PersonProfileRefreshRetryBackoffSeconds() float64 {
	return utils.PersonProfileRefreshRetryBackoffSeconds(k.configGetter())
}

// PersonProfileRefreshMaxRetry 对齐 _person_profile_refresh_max_retry
func (k *Kernel) PersonProfileRefreshMaxRetry() int {
	return utils.PersonProfileRefreshMaxRetry(k.configGetter())
}

// Tokens 对齐 _tokens
func (k *Kernel) Tokens(values any) []string { return utils.MergeTokens(values) }

// MergeTokens 对齐 _merge_tokens
func (k *Kernel) MergeTokens(groups ...any) []string { return utils.MergeTokens(groups...) }

// ArgumentTokens 对齐 _argument_tokens
func (k *Kernel) ArgumentTokens(value any) []string { return utils.ArgumentTokens(value) }

// MergeArgumentTokens 对齐 _merge_argument_tokens
func (k *Kernel) MergeArgumentTokens(groups ...any) []string {
	return utils.MergeArgumentTokens(groups...)
}

// BuildSource 对齐 _build_source
func (k *Kernel) BuildSource(sourceType string, chatID string, personIDs []string) string {
	return utils.BuildSource(sourceType, chatID, personIDs)
}
