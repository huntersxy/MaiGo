package maibot

// 本包由 scripts/gen_maibot_config_types.py 从 MaiBot Pydantic 模型生成骨架。
// 运行时：loader.go + runtime_bridge.go；升级钩子 upgrade_register.go；
// 遗留库迁移 legacy_migration.go；热重载 watcher.go。

type AMemorixAdvancedConfig struct {
	EnableAutoSave          bool `toml:"enable_auto_save,omitempty"`
	AutoSaveIntervalMinutes int  `toml:"auto_save_interval_minutes,omitempty"`
	Debug                   bool `toml:"debug,omitempty"`
}

type AMemorixConfig struct {
	Plugin                     AMemorixPluginConfig          `toml:"plugin,omitempty"`
	Integration                AMemorixIntegrationConfig     `toml:"integration,omitempty"`
	Storage                    AMemorixStorageConfig         `toml:"storage,omitempty"`
	ImageMemory                AMemorixImageMemoryConfig     `toml:"image_memory,omitempty"`
	Embedding                  AMemorixEmbeddingConfig       `toml:"embedding,omitempty"`
	Retrieval                  AMemorixRetrievalConfig       `toml:"retrieval,omitempty"`
	Threshold                  AMemorixThresholdConfig       `toml:"threshold,omitempty"`
	Filter                     AMemorixFilterConfig          `toml:"filter,omitempty"`
	GlobalMemorySharingEnabled bool                          `toml:"global_memory_sharing_enabled,omitempty"`
	SharedMemoryGroups         []ChatStreamGroup             `toml:"shared_memory_groups,omitempty"`
	Episode                    AMemorixEpisodeConfig         `toml:"episode,omitempty"`
	PersonProfile              AMemorixPersonProfileConfig   `toml:"person_profile,omitempty"`
	Memory                     AMemorixMemoryEvolutionConfig `toml:"memory,omitempty"`
	Advanced                   AMemorixAdvancedConfig        `toml:"advanced,omitempty"`
	Web                        AMemorixWebConfig             `toml:"web,omitempty"`
}

type AMemorixEmbeddingConfig struct {
	ModelName               string                                `toml:"model_name,omitempty"`
	Dimension               int                                   `toml:"dimension,omitempty"`
	DimensionRequestMode    string                                `toml:"dimension_request_mode,omitempty"`
	BatchSize               int                                   `toml:"batch_size,omitempty"`
	MaxConcurrent           int                                   `toml:"max_concurrent,omitempty"`
	EnableCache             bool                                  `toml:"enable_cache,omitempty"`
	RuntimeTrainThreshold   int                                   `toml:"runtime_train_threshold,omitempty"`
	QuantizationType        string                                `toml:"quantization_type,omitempty"`
	Fallback                AMemorixEmbeddingFallbackConfig       `toml:"fallback,omitempty"`
	ParagraphVectorBackfill AMemorixParagraphVectorBackfillConfig `toml:"paragraph_vector_backfill,omitempty"`
}

type AMemorixEmbeddingFallbackConfig struct {
	Enabled                bool `toml:"enabled,omitempty"`
	ProbeIntervalSeconds   int  `toml:"probe_interval_seconds,omitempty"`
	AllowMetadataOnlyWrite bool `toml:"allow_metadata_only_write,omitempty"`
}

type AMemorixEpisodeConfig struct {
	Enabled                   bool     `toml:"enabled,omitempty"`
	GenerationEnabled         bool     `toml:"generation_enabled,omitempty"`
	SourcePollIntervalSeconds float64  `toml:"source_poll_interval_seconds,omitempty"`
	SourceBatchSize           int      `toml:"source_batch_size,omitempty"`
	SourceMaxRetry            int      `toml:"source_max_retry,omitempty"`
	SourceLeaseSeconds        float64  `toml:"source_lease_seconds,omitempty"`
	SourceMaxWaitSeconds      float64  `toml:"source_max_wait_seconds,omitempty"`
	MaxParagraphsPerCall      int      `toml:"max_paragraphs_per_call,omitempty"`
	MaxCharsPerCall           int      `toml:"max_chars_per_call,omitempty"`
	SourceTimeWindowHours     float64  `toml:"source_time_window_hours,omitempty"`
	SegmentationModel         string   `toml:"segmentation_model,omitempty"`
	DisabledSourceTypes       []string `toml:"disabled_source_types,omitempty"`
}

type AMemorixFilterConfig struct {
	Enabled   bool                          `toml:"enabled,omitempty"`
	Mode      string                        `toml:"mode,omitempty"`
	Chats     []string                      `toml:"chats,omitempty"`
	Retrieval AMemorixRetrievalFilterConfig `toml:"retrieval,omitempty"`
}

type AMemorixFusionRetrievalConfig struct {
	Method       string  `toml:"method,omitempty"`
	RrfK         int     `toml:"rrf_k,omitempty"`
	VectorWeight float64 `toml:"vector_weight,omitempty"`
	Bm25Weight   float64 `toml:"bm25_weight,omitempty"`
}

type AMemorixImageMemoryConfig struct {
	Enabled                bool    `toml:"enabled,omitempty"`
	TaskName               string  `toml:"task_name,omitempty"`
	PreprocessVersion      string  `toml:"preprocess_version,omitempty"`
	ProbeRetrySeconds      float64 `toml:"probe_retry_seconds,omitempty"`
	MaxBytes               int     `toml:"max_bytes,omitempty"`
	MaxPixels              int     `toml:"max_pixels,omitempty"`
	CandidateLimit         int     `toml:"candidate_limit,omitempty"`
	SimilarityThreshold    float64 `toml:"similarity_threshold,omitempty"`
	JobPollIntervalSeconds float64 `toml:"job_poll_interval_seconds,omitempty"`
	JobBatchSize           int     `toml:"job_batch_size,omitempty"`
	JobEnqueueBatchSize    int     `toml:"job_enqueue_batch_size,omitempty"`
	JobLeaseSeconds        float64 `toml:"job_lease_seconds,omitempty"`
	JobMaxRetries          int     `toml:"job_max_retries,omitempty"`
	MinTrainThreshold      int     `toml:"min_train_threshold,omitempty"`
}

type AMemorixIntegrationConfig struct {
	EnableMemoryQueryTool                        bool    `toml:"enable_memory_query_tool,omitempty"`
	MemoryQueryDefaultLimit                      int     `toml:"memory_query_default_limit,omitempty"`
	EnablePersonProfileQueryTool                 bool    `toml:"enable_person_profile_query_tool,omitempty"`
	EnablePersonProfileInjection                 bool    `toml:"enable_person_profile_injection,omitempty"`
	PersonProfileInjectionMaxProfiles            int     `toml:"person_profile_injection_max_profiles,omitempty"`
	HeuristicMemoryRecallEnabled                 bool    `toml:"heuristic_memory_recall_enabled,omitempty"`
	HeuristicMemoryCrossChatEnabled              bool    `toml:"heuristic_memory_cross_chat_enabled,omitempty"`
	HeuristicMemoryRecallWindowSize              int     `toml:"heuristic_memory_recall_window_size,omitempty"`
	HeuristicMemoryRecallLimit                   int     `toml:"heuristic_memory_recall_limit,omitempty"`
	HeuristicMemoryRecallMaxChars                int     `toml:"heuristic_memory_recall_max_chars,omitempty"`
	HeuristicMemoryRecallMinIntervalSeconds      int     `toml:"heuristic_memory_recall_min_interval_seconds,omitempty"`
	HeuristicMemoryRecallMinNewMessages          int     `toml:"heuristic_memory_recall_min_new_messages,omitempty"`
	HeuristicMemoryRecallCacheTtlSeconds         int     `toml:"heuristic_memory_recall_cache_ttl_seconds,omitempty"`
	HeuristicMemoryGroupToPrivateEnabled         bool    `toml:"heuristic_memory_group_to_private_enabled,omitempty"`
	HeuristicMemoryPrivateToGroupEnabled         bool    `toml:"heuristic_memory_private_to_group_enabled,omitempty"`
	PersonFactWritebackEnabled                   bool    `toml:"person_fact_writeback_enabled,omitempty"`
	ChatSummaryWritebackEnabled                  bool    `toml:"chat_summary_writeback_enabled,omitempty"`
	ChatSummaryWritebackMessageThreshold         int     `toml:"chat_summary_writeback_message_threshold,omitempty"`
	ChatSummaryWritebackContextLength            int     `toml:"chat_summary_writeback_context_length,omitempty"`
	FuzzyModifyEnabled                           bool    `toml:"fuzzy_modify_enabled,omitempty"`
	FuzzyModifyAutoExecuteEnabled                bool    `toml:"fuzzy_modify_auto_execute_enabled,omitempty"`
	FuzzyModifyConfirmThreshold                  float64 `toml:"fuzzy_modify_confirm_threshold,omitempty"`
	FuzzyModifyCandidateLimit                    int     `toml:"fuzzy_modify_candidate_limit,omitempty"`
	FuzzyModifyMaxTargets                        int     `toml:"fuzzy_modify_max_targets,omitempty"`
	FuzzyModifyAllowGlobalScope                  bool    `toml:"fuzzy_modify_allow_global_scope,omitempty"`
	FeedbackCorrectionEnabled                    bool    `toml:"feedback_correction_enabled,omitempty"`
	FeedbackCorrectionWindowHours                float64 `toml:"feedback_correction_window_hours,omitempty"`
	FeedbackCorrectionCheckIntervalMinutes       int     `toml:"feedback_correction_check_interval_minutes,omitempty"`
	FeedbackCorrectionBatchSize                  int     `toml:"feedback_correction_batch_size,omitempty"`
	FeedbackCorrectionAutoApplyThreshold         float64 `toml:"feedback_correction_auto_apply_threshold,omitempty"`
	FeedbackCorrectionMaxFeedbackMessages        int     `toml:"feedback_correction_max_feedback_messages,omitempty"`
	FeedbackCorrectionPrefilterEnabled           bool    `toml:"feedback_correction_prefilter_enabled,omitempty"`
	FeedbackCorrectionParagraphMarkEnabled       bool    `toml:"feedback_correction_paragraph_mark_enabled,omitempty"`
	FeedbackCorrectionParagraphHardFilterEnabled bool    `toml:"feedback_correction_paragraph_hard_filter_enabled,omitempty"`
	FeedbackCorrectionProfileRefreshEnabled      bool    `toml:"feedback_correction_profile_refresh_enabled,omitempty"`
	FeedbackCorrectionProfileForceRefreshOnRead  bool    `toml:"feedback_correction_profile_force_refresh_on_read,omitempty"`
	FeedbackCorrectionEpisodeRebuildEnabled      bool    `toml:"feedback_correction_episode_rebuild_enabled,omitempty"`
	FeedbackCorrectionEpisodeQueryBlockEnabled   bool    `toml:"feedback_correction_episode_query_block_enabled,omitempty"`
	FeedbackCorrectionReconcileIntervalMinutes   int     `toml:"feedback_correction_reconcile_interval_minutes,omitempty"`
	FeedbackCorrectionReconcileBatchSize         int     `toml:"feedback_correction_reconcile_batch_size,omitempty"`
}

type AMemorixMemoryEvolutionConfig struct {
	Enabled                            bool    `toml:"enabled,omitempty"`
	HalfLifeHours                      float64 `toml:"half_life_hours,omitempty"`
	PruneThreshold                     float64 `toml:"prune_threshold,omitempty"`
	FreezeDurationHours                float64 `toml:"freeze_duration_hours,omitempty"`
	ReviveThreshold                    float64 `toml:"revive_threshold,omitempty"`
	AccessReinforcementAlpha           float64 `toml:"access_reinforcement_alpha,omitempty"`
	AccessReinforcementCooldownMinutes float64 `toml:"access_reinforcement_cooldown_minutes,omitempty"`
	ExplicitReinforcementAlpha         float64 `toml:"explicit_reinforcement_alpha,omitempty"`
	WeakenAlpha                        float64 `toml:"weaken_alpha,omitempty"`
	LifecycleBatchSize                 int     `toml:"lifecycle_batch_size,omitempty"`
}

type AMemorixParagraphVectorBackfillConfig struct {
	Enabled         bool `toml:"enabled,omitempty"`
	IntervalSeconds int  `toml:"interval_seconds,omitempty"`
	BatchSize       int  `toml:"batch_size,omitempty"`
	MaxRetry        int  `toml:"max_retry,omitempty"`
}

type AMemorixPersonProfileConfig struct {
	Enabled                         bool    `toml:"enabled,omitempty"`
	RefreshIntervalMinutes          int     `toml:"refresh_interval_minutes,omitempty"`
	ActiveWindowHours               float64 `toml:"active_window_hours,omitempty"`
	MaxRefreshPerCycle              int     `toml:"max_refresh_per_cycle,omitempty"`
	RefreshDebounceSeconds          int     `toml:"refresh_debounce_seconds,omitempty"`
	RefreshQueueIntervalSeconds     int     `toml:"refresh_queue_interval_seconds,omitempty"`
	RefreshQueueBatchSize           int     `toml:"refresh_queue_batch_size,omitempty"`
	RefreshRetryBackoffSeconds      int     `toml:"refresh_retry_backoff_seconds,omitempty"`
	MaxRetry                        int     `toml:"max_retry,omitempty"`
	TopKEvidence                    int     `toml:"top_k_evidence,omitempty"`
	EvidenceClassificationMaxTokens int     `toml:"evidence_classification_max_tokens,omitempty"`
}

type AMemorixPluginConfig struct {
	Enabled bool `toml:"enabled,omitempty"`
}

type AMemorixRelationIntentVectorPoolConfig struct {
	GraphTopK           int     `toml:"graph_top_k,omitempty"`
	SemanticWeight      float64 `toml:"semantic_weight,omitempty"`
	SparseWeight        float64 `toml:"sparse_weight,omitempty"`
	GraphWeight         float64 `toml:"graph_weight,omitempty"`
	ReturnRelationItems bool    `toml:"return_relation_items,omitempty"`
}

type AMemorixRelationVectorizationConfig struct {
	Enabled         bool `toml:"enabled,omitempty"`
	BackfillEnabled bool `toml:"backfill_enabled,omitempty"`
	WriteOnImport   bool `toml:"write_on_import,omitempty"`
}

type AMemorixRetrievalConfig struct {
	TopKParagraphs        int                                 `toml:"top_k_paragraphs,omitempty"`
	TopKRelations         int                                 `toml:"top_k_relations,omitempty"`
	TopKFinal             int                                 `toml:"top_k_final,omitempty"`
	Alpha                 float64                             `toml:"alpha,omitempty"`
	EnablePpr             bool                                `toml:"enable_ppr,omitempty"`
	PprAlpha              float64                             `toml:"ppr_alpha,omitempty"`
	PprTimeoutSeconds     float64                             `toml:"ppr_timeout_seconds,omitempty"`
	PprConcurrencyLimit   int                                 `toml:"ppr_concurrency_limit,omitempty"`
	EnableParallel        bool                                `toml:"enable_parallel,omitempty"`
	Search                AMemorixRetrievalSearchConfig       `toml:"search,omitempty"`
	Fusion                AMemorixFusionRetrievalConfig       `toml:"fusion,omitempty"`
	RelationVectorization AMemorixRelationVectorizationConfig `toml:"relation_vectorization,omitempty"`
	VectorPools           AMemorixVectorPoolsConfig           `toml:"vector_pools,omitempty"`
	Sparse                AMemorixSparseRetrievalConfig       `toml:"sparse,omitempty"`
}

type AMemorixRetrievalFilterConfig struct {
	ChatStream  AMemorixRetrievalSubtypeFilterConfig `toml:"chat_stream,omitempty"`
	ChatSummary AMemorixRetrievalSubtypeFilterConfig `toml:"chat_summary,omitempty"`
	Episode     AMemorixRetrievalSubtypeFilterConfig `toml:"episode,omitempty"`
}

type AMemorixRetrievalSearchConfig struct {
	SmartFallback AMemorixSmartFallbackConfig `toml:"smart_fallback,omitempty"`
}

type AMemorixRetrievalSubtypeFilterConfig struct {
	Enabled bool     `toml:"enabled,omitempty"`
	Mode    string   `toml:"mode,omitempty"`
	Chats   []string `toml:"chats,omitempty"`
}

type AMemorixSmartFallbackConfig struct {
	Enabled bool `toml:"enabled,omitempty"`
}

type AMemorixSparseRetrievalConfig struct {
	Enabled            bool   `toml:"enabled,omitempty"`
	Backend            string `toml:"backend,omitempty"`
	Mode               string `toml:"mode,omitempty"`
	TokenizerMode      string `toml:"tokenizer_mode,omitempty"`
	CandidateK         int    `toml:"candidate_k,omitempty"`
	RelationCandidateK int    `toml:"relation_candidate_k,omitempty"`
}

type AMemorixStorageConfig struct {
	DataDir string `toml:"data_dir,omitempty"`
}

type AMemorixThresholdConfig struct {
	MinThreshold float64 `toml:"min_threshold,omitempty"`
	MaxThreshold float64 `toml:"max_threshold,omitempty"`
	Percentile   int     `toml:"percentile,omitempty"`
	MinResults   int     `toml:"min_results,omitempty"`
}

type AMemorixVectorPoolsConfig struct {
	Mode                   string                                 `toml:"mode,omitempty"`
	ParagraphTopK          int                                    `toml:"paragraph_top_k,omitempty"`
	GraphTopK              int                                    `toml:"graph_top_k,omitempty"`
	GraphExpandParagraphK  int                                    `toml:"graph_expand_paragraph_k,omitempty"`
	RelationExpandPerHit   int                                    `toml:"relation_expand_per_hit,omitempty"`
	EntityExpandPerHit     int                                    `toml:"entity_expand_per_hit,omitempty"`
	RelationEvidenceWeight float64                                `toml:"relation_evidence_weight,omitempty"`
	EntityEvidenceWeight   float64                                `toml:"entity_evidence_weight,omitempty"`
	SemanticWeight         float64                                `toml:"semantic_weight,omitempty"`
	SparseWeight           float64                                `toml:"sparse_weight,omitempty"`
	GraphWeight            float64                                `toml:"graph_weight,omitempty"`
	RelationIntent         AMemorixRelationIntentVectorPoolConfig `toml:"relation_intent,omitempty"`
}

type AMemorixWebConfig struct {
	ImportConfig AMemorixWebImportConfig `toml:"import_config,omitempty"`
	Tuning       AMemorixWebTuningConfig `toml:"tuning,omitempty"`
}

type AMemorixWebImportConfig struct {
	Enabled                    bool                           `toml:"enabled,omitempty"`
	MaxQueueSize               int                            `toml:"max_queue_size,omitempty"`
	MaxFilesPerTask            int                            `toml:"max_files_per_task,omitempty"`
	MaxFileSizeMb              int                            `toml:"max_file_size_mb,omitempty"`
	MaxPasteChars              int                            `toml:"max_paste_chars,omitempty"`
	DefaultFileConcurrency     int                            `toml:"default_file_concurrency,omitempty"`
	DefaultChunkConcurrency    int                            `toml:"default_chunk_concurrency,omitempty"`
	DefaultNarrativeWindowSize int                            `toml:"default_narrative_window_size,omitempty"`
	DefaultNarrativeOverlap    int                            `toml:"default_narrative_overlap,omitempty"`
	DefaultFactualTargetSize   int                            `toml:"default_factual_target_size,omitempty"`
	MaxChunkChars              int                            `toml:"max_chunk_chars,omitempty"`
	Timeout                    AMemorixWebImportTimeoutConfig `toml:"timeout,omitempty"`
}

type AMemorixWebImportTimeoutConfig struct {
	LlmCallSeconds          float64 `toml:"llm_call_seconds,omitempty"`
	ProcessPollSeconds      float64 `toml:"process_poll_seconds,omitempty"`
	ProcessTerminateSeconds float64 `toml:"process_terminate_seconds,omitempty"`
	ProcessKillSeconds      float64 `toml:"process_kill_seconds,omitempty"`
	ConvertPreflightSeconds float64 `toml:"convert_preflight_seconds,omitempty"`
}

type AMemorixWebTuningConfig struct {
	Enabled           bool   `toml:"enabled,omitempty"`
	MaxQueueSize      int    `toml:"max_queue_size,omitempty"`
	PollIntervalMs    int    `toml:"poll_interval_ms,omitempty"`
	DefaultIntensity  string `toml:"default_intensity,omitempty"`
	DefaultObjective  string `toml:"default_objective,omitempty"`
	DefaultTopKEval   int    `toml:"default_top_k_eval,omitempty"`
	DefaultSampleSize int    `toml:"default_sample_size,omitempty"`
}

type APIProvider struct {
	Name                  string            `toml:"name,omitempty"`
	BaseUrl               string            `toml:"base_url,omitempty"`
	ApiKey                string            `toml:"api_key,omitempty"`
	ClientType            string            `toml:"client_type,omitempty"`
	AuthType              string            `toml:"auth_type,omitempty"`
	AuthHeaderName        string            `toml:"auth_header_name,omitempty"`
	AuthHeaderPrefix      string            `toml:"auth_header_prefix,omitempty"`
	AuthQueryName         string            `toml:"auth_query_name,omitempty"`
	DefaultHeaders        map[string]string `toml:"default_headers,omitempty"`
	DefaultQuery          map[string]string `toml:"default_query,omitempty"`
	Organization          any               `toml:"organization,omitempty"`
	Project               any               `toml:"project,omitempty"`
	ModelListEndpoint     string            `toml:"model_list_endpoint,omitempty"`
	ReasoningParseMode    string            `toml:"reasoning_parse_mode,omitempty"`
	ToolArgumentParseMode string            `toml:"tool_argument_parse_mode,omitempty"`
	MaxRetry              int               `toml:"max_retry,omitempty"`
	// MaxRetrySet 表示 TOML 中显式写了 max_retry（含 0）；加载时填充，不参与序列化
	MaxRetrySet bool `toml:"-" json:"-"`
	// AuthHeaderPrefixSet 表示 TOML 中显式写了 auth_header_prefix（含空串）
	AuthHeaderPrefixSet bool `toml:"-" json:"-"`
	Timeout             int  `toml:"timeout,omitempty"`
	RetryInterval       int  `toml:"retry_interval,omitempty"`
}

type AgentConfig struct {
	Enabled         bool              `toml:"enabled,omitempty"`
	AllowUsers      []string          `toml:"allow_users,omitempty"`
	AllowGroups     []string          `toml:"allow_groups,omitempty"`
	PlannerEnabled  bool              `toml:"planner_enabled,omitempty"`
	PlannerSessions []string          `toml:"planner_sessions,omitempty"`
	Connection      string            `toml:"connection,omitempty"`
	DesktopManifest string            `toml:"desktop_manifest,omitempty"`
	AcpCommand      string            `toml:"acp_command,omitempty"`
	AcpArgs         []string          `toml:"acp_args,omitempty"`
	AcpCwd          string            `toml:"acp_cwd,omitempty"`
	AcpRemoteCwd    string            `toml:"acp_remote_cwd,omitempty"`
	McpPort         int               `toml:"mcp_port,omitempty"`
	AcpEnv          map[string]string `toml:"acp_env,omitempty"`
	AcpModel        string            `toml:"acp_model,omitempty"`
	AcpTimeout      float64           `toml:"acp_timeout,omitempty"`
}

type AttentionDriftConfig struct {
	Enabled       bool   `toml:"enabled,omitempty"`
	DriftLevel    string `toml:"drift_level,omitempty"`
	AnchorPolicy  string `toml:"anchor_policy,omitempty"`
	ReactionStyle string `toml:"reaction_style,omitempty"`
}

type BotConfig struct {
	Platform   string   `toml:"platform,omitempty"`
	QqAccount  string   `toml:"qq_account,omitempty"`
	Platforms  []string `toml:"platforms,omitempty"`
	Nickname   string   `toml:"nickname,omitempty"`
	AliasNames []string `toml:"alias_names,omitempty"`
}

type CapabilitiesConfig struct {
	Files                       bool `toml:"files,omitempty"`
	ToolResultCondense          bool `toml:"tool_result_condense,omitempty"`
	ToolResultCondenseThreshold int  `toml:"tool_result_condense_threshold,omitempty"`
	PlannerSubagent             bool `toml:"planner_subagent,omitempty"`
}

type ChatConfig struct {
	EnableReplyAt           bool                  `toml:"enable_reply_at,omitempty"`
	MaxContextSize          int                   `toml:"max_context_size,omitempty"`
	MaxPrivateContextSize   int                   `toml:"max_private_context_size,omitempty"`
	UseTokenLimit           bool                  `toml:"use_token_limit,omitempty"`
	MaxContextTokens        int                   `toml:"max_context_tokens,omitempty"`
	PrivateUseTokenLimit    bool                  `toml:"private_use_token_limit,omitempty"`
	PrivateMaxContextTokens int                   `toml:"private_max_context_tokens,omitempty"`
	ReplyTiming             ChatReplyTimingConfig `toml:"reply_timing,omitempty"`
	ReplyStyle              ChatReplyStyleConfig  `toml:"reply_style,omitempty"`
	// 8.14.19 前 [chat] 根级字段；升级链迁至 reply_timing / reply_style
	ChatLegacyFlatFields `toml:",inline"`
}

// ChatLegacyFlatFields 旧版平铺在 [chat] 下的键（仅升级链消费）
type ChatLegacyFlatFields struct {
	TalkValue               float64         `toml:"talk_value,omitempty"`
	MentionedBotReply       *bool           `toml:"mentioned_bot_reply,omitempty"`
	InevitableAtReply       *bool           `toml:"inevitable_at_reply,omitempty"`
	MaxConsecutiveWaitCount *int            `toml:"max_consecutive_wait_count,omitempty"`
	EnableTalkValueRules    *bool           `toml:"enable_talk_value_rules,omitempty"`
	TalkValueRules          []TalkRulesItem `toml:"talk_value_rules,omitempty"`
	EnableReplyQuote        *bool           `toml:"enable_reply_quote,omitempty"`
	GroupChatPrompt         string          `toml:"group_chat_prompt,omitempty"`
	PrivateChatPrompts      string          `toml:"private_chat_prompts,omitempty"`
	ChatPrompts             []any           `toml:"chat_prompts,omitempty"`
}

type ChatReplyStyleConfig struct {
	GroupQuoteFrequency   string `toml:"group_quote_frequency,omitempty"`
	PrivateQuoteFrequency string `toml:"private_quote_frequency,omitempty"`
	GroupChatPrompt       string `toml:"group_chat_prompt,omitempty"`
	PrivateChatPrompts    string `toml:"private_chat_prompts,omitempty"`
	ChatPrompts           []any  `toml:"chat_prompts,omitempty"`
}

type ChatReplyTimingConfig struct {
	MessageDebounceMode        string                  `toml:"message_debounce_mode,omitempty"`
	PrivateMessageDebounceMode string                  `toml:"private_message_debounce_mode,omitempty"`
	PlannerMessageSteering     bool                    `toml:"planner_message_steering,omitempty"`
	TalkValue                  float64                 `toml:"talk_value"`
	PrivateReplyPaused         bool                    `toml:"private_reply_paused,omitempty"`
	PrivateReplyPauseRules     []PrivateReplyPauseRule `toml:"private_reply_pause_rules,omitempty"`
	MentionedBotReply          bool                    `toml:"mentioned_bot_reply,omitempty"`
	InevitableAtReply          bool                    `toml:"inevitable_at_reply,omitempty"`
	// EnableTalkValueRules 8.16.11 前；规则开关移除后由升级链消费
	EnableTalkValueRules *bool `toml:"enable_talk_value_rules,omitempty"`
	// TalkValueRules 8.16.18 前；升级链迁入 TalkRuleGroups
	TalkValueRules []TalkRulesItem `toml:"talk_value_rules,omitempty"`
	TalkRuleGroups TalkRuleGroups  `toml:"talk_rule_groups,omitempty"`
}

type ChatStreamGroup struct {
	Targets []TargetItem `toml:"targets,omitempty"`
	// 8.10.20 前组内键名
	LegacyInnerExpressionGroups []TargetItem `toml:"expression_groups,omitempty"`
	LegacyInnerJargonGroups     []TargetItem `toml:"jargon_groups,omitempty"`
}

type ChineseTypoConfig struct {
	Enable                     bool    `toml:"enable,omitempty"`
	EnableCorrectionQuote      bool    `toml:"enable_correction_quote,omitempty"`
	CorrectionQuoteProbability float64 `toml:"correction_quote_probability,omitempty"`
	ErrorRate                  float64 `toml:"error_rate,omitempty"`
	MinFreq                    int     `toml:"min_freq,omitempty"`
	ToneErrorRate              float64 `toml:"tone_error_rate,omitempty"`
	WordReplaceRate            float64 `toml:"word_replace_rate,omitempty"`
}

type CommandPermissionConfig struct {
	AllowUsers []string `toml:"allow_users,omitempty"`
	AllowChats []string `toml:"allow_chats,omitempty"`
}

type DebugConfig struct {
	EnableConsoleInput          bool `toml:"enable_console_input,omitempty"`
	GeminiReplyCompatibility    bool `toml:"gemini_reply_compatibility,omitempty"`
	ShowMaisakaThinking         bool `toml:"show_maisaka_thinking,omitempty"`
	SaveMaisakaReasoning        bool `toml:"save_maisaka_reasoning,omitempty"`
	KeepPromptPreviewJsonBase64 bool `toml:"keep_prompt_preview_json_base64,omitempty"`
	RecordToolStructuredContent bool `toml:"record_tool_structured_content,omitempty"`
	EnableLlmCacheStats         bool `toml:"enable_llm_cache_stats,omitempty"`
	ForcePluginCompatibility    bool `toml:"force_plugin_compatibility,omitempty"`
}

type DesktopPetConfig struct {
	Enabled                      bool    `toml:"enabled,omitempty"`
	Mode                         string  `toml:"mode,omitempty"`
	ChatToolsEnabled             bool    `toml:"chat_tools_enabled,omitempty"`
	ShowCurrentTask              bool    `toml:"show_current_task,omitempty"`
	SkinId                       string  `toml:"skin_id,omitempty"`
	MessageSound                 string  `toml:"message_sound,omitempty"`
	ProactiveWakeEnabled         bool    `toml:"proactive_wake_enabled,omitempty"`
	ProactiveWakeIntervalMinutes int     `toml:"proactive_wake_interval_minutes,omitempty"`
	ScreenReadEnabled            bool    `toml:"screen_read_enabled,omitempty"`
	ScreenReadIntervalSeconds    int     `toml:"screen_read_interval_seconds,omitempty"`
	ScreenSampleIntervalSeconds  float64 `toml:"screen_sample_interval_seconds,omitempty"`
	ScreenChangeThreshold        float64 `toml:"screen_change_threshold,omitempty"`
}

type EmojiCacheCleanupConfig struct {
	Enabled                   bool    `toml:"enabled,omitempty"`
	CheckIntervalHours        float64 `toml:"check_interval_hours,omitempty"`
	EmojiFileRetentionDays    int     `toml:"emoji_file_retention_days,omitempty"`
	NoFileRecordRetentionDays int     `toml:"no_file_record_retention_days,omitempty"`
}

type EmojiConfig struct {
	MaxRegNum         int                     `toml:"max_reg_num,omitempty"`
	DoReplace         bool                    `toml:"do_replace,omitempty"`
	CheckInterval     int                     `toml:"check_interval,omitempty"`
	StealEmoji        bool                    `toml:"steal_emoji,omitempty"`
	MaxEmojiSizeMb    float64                 `toml:"max_emoji_size_mb,omitempty"`
	ContentFiltration bool                    `toml:"content_filtration,omitempty"`
	CacheCleanup      EmojiCacheCleanupConfig `toml:"cache_cleanup,omitempty"`
}

type ExperimentalConfig struct {
	DesktopPetExplorationEnabled bool   `toml:"desktop_pet_exploration_enabled,omitempty"`
	PassiveReplyer               bool   `toml:"passive_replyer,omitempty"`
	SuperCreativeMode            bool   `toml:"super_creative_mode,omitempty"`
	SuperCreativeLanguage        string `toml:"super_creative_language,omitempty"`
	GroupAttentionDrift          any    `toml:"group_attention_drift,omitempty"`
	PrivateAttentionDrift        any    `toml:"private_attention_drift,omitempty"`
	FocusMode                    bool   `toml:"focus_mode,omitempty"`
	FocusOnPrivate               bool   `toml:"focus_on_private,omitempty"`
	FocusChatWhitelist           []any  `toml:"focus_chat_whitelist,omitempty"`
	FocusGroups                  []any  `toml:"focus_groups,omitempty"`
	// PlannerMessageSteering 旧位置（8.15.37 前）；升级后迁至 chat.reply_timing
	PlannerMessageSteering *bool `toml:"planner_message_steering,omitempty"`
}

type ExpressionConfig struct {
	ExpressionCheckedOnly bool `toml:"expression_checked_only,omitempty"`
	ExpressionSelfReflect bool `toml:"expression_self_reflect,omitempty"`
	// 8.10.18 升级链读取
	AllGlobalJargon *bool `toml:"all_global_jargon,omitempty"`
	// ExpressionSelectionMode 已废弃（8.14.58 移除）；升级链仍读取旧 TOML
	ExpressionSelectionMode           string                  `toml:"expression_selection_mode,omitempty"`
	UseVectorExpression               bool                    `toml:"use_vector_expression,omitempty"`
	ExpressionVectorIndexPath         string                  `toml:"expression_vector_index_path,omitempty"`
	ExpressionVectorCandidatePoolSize int                     `toml:"expression_vector_candidate_pool_size,omitempty"`
	MaxExpressionLearner              int                     `toml:"max_expression_learner,omitempty"`
	LearningList                      []LearningItem          `toml:"learning_list,omitempty"`
	ExpressionGroups                  []ChatStreamGroup       `toml:"expression_groups,omitempty"`
	LearningBlacklist                 []LearningBlacklistUser `toml:"learning_blacklist,omitempty"`
}

type ExtraPromptItem struct {
	Platform string `toml:"platform,omitempty"`
	ItemId   string `toml:"item_id,omitempty"`
	RuleType string `toml:"rule_type,omitempty"`
	Prompt   string `toml:"prompt,omitempty"`
}

type FilesConfig struct {
	MaxFileMib             int      `toml:"max_file_mib,omitempty"`
	MaxStorageGib          int      `toml:"max_storage_gib,omitempty"`
	MaxTextMib             int      `toml:"max_text_mib,omitempty"`
	CacheDays              int      `toml:"cache_days,omitempty"`
	MaxConcurrentTransfers int      `toml:"max_concurrent_transfers,omitempty"`
	TransferTimeout        int      `toml:"transfer_timeout,omitempty"`
	TransferBaseUrl        string   `toml:"transfer_base_url,omitempty"`
	TrustedDownloadOrigins []string `toml:"trusted_download_origins,omitempty"`
}

type HTTPConfig struct {
	Host []string `toml:"host,omitempty"`
	Port int      `toml:"port,omitempty"`
}

type ImageCacheCleanupConfig struct {
	Enabled                   bool    `toml:"enabled,omitempty"`
	CheckIntervalHours        float64 `toml:"check_interval_hours,omitempty"`
	ImageFileRetentionDays    int     `toml:"image_file_retention_days,omitempty"`
	NoFileResultRetentionDays int     `toml:"no_file_result_retention_days,omitempty"`
}

type JargonConfig struct {
	LearningList      []LearningItem          `toml:"learning_list,omitempty"`
	JargonGroups      []ChatStreamGroup       `toml:"jargon_groups,omitempty"`
	LearningBlacklist []LearningBlacklistUser `toml:"learning_blacklist,omitempty"`
	// 8.10.18 前非表结构 jargon_groups；加载时填充，升级后清空
	JargonGroupsLegacyRaw any `toml:"-"`
}

type KeywordReactionConfig struct {
	KeywordRules []KeywordRuleConfig `toml:"keyword_rules,omitempty"`
	RegexRules   []KeywordRuleConfig `toml:"regex_rules,omitempty"`
}

type KeywordRuleConfig struct {
	Keywords []string `toml:"keywords,omitempty"`
	Regex    []string `toml:"regex,omitempty"`
	Reaction string   `toml:"reaction,omitempty"`
}

type LearningBlacklistUser struct {
	Platform string `toml:"platform,omitempty"`
	UserId   string `toml:"user_id,omitempty"`
}

type LearningItem struct {
	// 不能用 omitempty：空串是合法取值（platform="" 表示通配），
	// 省略会让 WebUI 读到 undefined 并在 trim() 处崩溃
	Platform string `toml:"platform"`
	ItemId   string `toml:"item_id"`
	Type     string `toml:"type"`
	Use      bool   `toml:"use"`
	Learn    bool   `toml:"learn"`
	// 8.10.18–8.10.19 升级链读取
	RuleType             string `toml:"rule_type,omitempty"`
	UseExpression        *bool  `toml:"use_expression,omitempty"`
	EnableLearning       *bool  `toml:"enable_learning,omitempty"`
	EnableJargonLearning *bool  `toml:"enable_jargon_learning,omitempty"`
}

type LogConfig struct {
	DateStyle                        string            `toml:"date_style,omitempty"`
	LogLevelStyle                    string            `toml:"log_level_style,omitempty"`
	ColorText                        string            `toml:"color_text,omitempty"`
	LogLevel                         string            `toml:"log_level,omitempty"`
	ConsoleLogLevel                  string            `toml:"console_log_level,omitempty"`
	FileLogLevel                     string            `toml:"file_log_level,omitempty"`
	LogFileMaxBytes                  int               `toml:"log_file_max_bytes,omitempty"`
	MaxLogFiles                      int               `toml:"max_log_files,omitempty"`
	LogCleanupDays                   int               `toml:"log_cleanup_days,omitempty"`
	LlmRequestSnapshotLimit          int               `toml:"llm_request_snapshot_limit,omitempty"`
	MaisakaPromptPreviewLimit        int               `toml:"maisaka_prompt_preview_limit,omitempty"`
	MemoryDiagnosticsEnabled         bool              `toml:"memory_diagnostics_enabled,omitempty"`
	MemoryDiagnosticsDeepEnabled     bool              `toml:"memory_diagnostics_deep_enabled,omitempty"`
	MemoryDiagnosticsIntervalSeconds float64           `toml:"memory_diagnostics_interval_seconds,omitempty"`
	EventLoopWatchdogEnabled         bool              `toml:"event_loop_watchdog_enabled,omitempty"`
	EventLoopWatchdogWarnSeconds     float64           `toml:"event_loop_watchdog_warn_seconds,omitempty"`
	SuppressLibraries                []string          `toml:"suppress_libraries,omitempty"`
	LibraryLogLevels                 map[string]string `toml:"library_log_levels,omitempty"`
}

type MCPAuthorizationConfig struct {
	Mode        string `toml:"mode,omitempty"`
	BearerToken string `toml:"bearer_token,omitempty"`
}

type MCPClientConfig struct {
	ClientName    string               `toml:"client_name,omitempty"`
	ClientVersion string               `toml:"client_version,omitempty"`
	Roots         MCPRootsConfig       `toml:"roots,omitempty"`
	Sampling      MCPSamplingConfig    `toml:"sampling,omitempty"`
	Elicitation   MCPElicitationConfig `toml:"elicitation,omitempty"`
}

type MCPConfig struct {
	Enable  bool                  `toml:"enable,omitempty"`
	Client  MCPClientConfig       `toml:"client,omitempty"`
	Servers []MCPServerItemConfig `toml:"servers,omitempty"`
}

type MCPElicitationConfig struct {
	Enable    bool `toml:"enable,omitempty"`
	AllowForm bool `toml:"allow_form,omitempty"`
	AllowUrl  bool `toml:"allow_url,omitempty"`
}

type MCPRootItemConfig struct {
	Enabled bool   `toml:"enabled,omitempty"`
	Uri     string `toml:"uri,omitempty"`
	Name    string `toml:"name,omitempty"`
}

type MCPRootsConfig struct {
	Enable bool                `toml:"enable,omitempty"`
	Items  []MCPRootItemConfig `toml:"items,omitempty"`
}

type MCPSamplingConfig struct {
	Enable      bool   `toml:"enable,omitempty"`
	TaskName    string `toml:"task_name,omitempty"`
	ToolSupport bool   `toml:"tool_support,omitempty"`
}

type MCPServerItemConfig struct {
	Name               string                 `toml:"name,omitempty"`
	Enabled            bool                   `toml:"enabled,omitempty"`
	Transport          string                 `toml:"transport,omitempty"`
	Command            string                 `toml:"command,omitempty"`
	Args               []string               `toml:"args,omitempty"`
	Env                map[string]string      `toml:"env,omitempty"`
	Url                string                 `toml:"url,omitempty"`
	Headers            map[string]string      `toml:"headers,omitempty"`
	HttpTimeoutSeconds float64                `toml:"http_timeout_seconds,omitempty"`
	ReadTimeoutSeconds float64                `toml:"read_timeout_seconds,omitempty"`
	Authorization      MCPAuthorizationConfig `toml:"authorization,omitempty"`
}

type MaimMessageConfig struct {
	Enabled                 bool     `toml:"enabled,omitempty"`
	WsServerHost            string   `toml:"ws_server_host,omitempty"`
	WsServerPort            int      `toml:"ws_server_port,omitempty"`
	AuthToken               []string `toml:"auth_token,omitempty"`
	EnableApiServer         bool     `toml:"enable_api_server,omitempty"`
	ApiServerHost           string   `toml:"api_server_host,omitempty"`
	ApiServerPort           int      `toml:"api_server_port,omitempty"`
	ApiServerUseWss         bool     `toml:"api_server_use_wss,omitempty"`
	ApiServerCertFile       string   `toml:"api_server_cert_file,omitempty"`
	ApiServerKeyFile        string   `toml:"api_server_key_file,omitempty"`
	ApiServerAllowedApiKeys []string `toml:"api_server_allowed_api_keys,omitempty"`
}

type MessageReceiveConfig struct {
	BanWords     any `toml:"ban_words,omitempty"`
	BanMsgsRegex any `toml:"ban_msgs_regex,omitempty"`
}

type ModelInfo struct {
	SuppressAnyWarning bool               `toml:"suppress_any_warning,omitempty"`
	ModelType          string             `toml:"model_type,omitempty"`
	ModelIdentifier    string             `toml:"model_identifier,omitempty"`
	Name               string             `toml:"name,omitempty"`
	ApiProvider        string             `toml:"api_provider,omitempty"`
	PriceIn            float64            `toml:"price_in,omitempty"`
	CachePriceIn       float64            `toml:"cache_price_in,omitempty"`
	PriceOut           float64            `toml:"price_out,omitempty"`
	PricePeriods       []ModelPricePeriod `toml:"price_periods,omitempty"`
	Temperature        any                `toml:"temperature,omitempty"`
	SendTemperature    bool               `toml:"send_temperature,omitempty"`
	MaxTokens          any                `toml:"max_tokens,omitempty"`
	ForceStreamMode    bool               `toml:"force_stream_mode,omitempty"`
	Visual             bool               `toml:"visual,omitempty"`
	MaxImages          int                `toml:"max_images,omitempty"`
	ImageMaxSide       int                `toml:"image_max_side,omitempty"`
	ImageMinSide       int                `toml:"image_min_side,omitempty"`
	ExtraParams        map[string]any     `toml:"extra_params,omitempty"`
	SendTemperatureSet bool               `toml:"-"`
}

type ModelPricePeriod struct {
	StartTime    string  `toml:"start_time,omitempty"`
	EndTime      string  `toml:"end_time,omitempty"`
	PriceIn      float64 `toml:"price_in,omitempty"`
	PriceOut     float64 `toml:"price_out,omitempty"`
	CachePriceIn float64 `toml:"cache_price_in,omitempty"`
}

type ModelTaskConfig struct {
	WebSearch      WebSearchTaskConfig `toml:"web_search"`
	Replyer        TaskConfig          `toml:"replyer"`
	Planner        TaskConfig          `toml:"planner"`
	Memory         TaskConfig          `toml:"memory"`
	Utils          TaskConfig          `toml:"utils"`
	Learner        TaskConfig          `toml:"learner"`
	FastModel      TaskConfig          `toml:"fast_model"`
	Vlm            TaskConfig          `toml:"vlm"`
	Voice          TaskConfig          `toml:"voice"`
	Embedding      TaskConfig          `toml:"embedding"`
	ImageEmbedding TaskConfig          `toml:"image_embedding"`
}

type PersonalityConfig struct {
	Profiles            []PersonalityProfile `toml:"profiles,omitempty"`
	SelectedProfile     string               `toml:"selected_profile,omitempty"`
	Personality         string               `toml:"personality,omitempty"`
	BehaviorStyle       string               `toml:"behavior_style,omitempty"`
	ReplyStyle          string               `toml:"reply_style,omitempty"`
	MultipleReplyStyle  []string             `toml:"multiple_reply_style,omitempty"`
	MultipleProbability float64              `toml:"multiple_probability,omitempty"`
}

type PersonalityProfile struct {
	Name                string   `toml:"name,omitempty"`
	Personality         string   `toml:"personality,omitempty"`
	BehaviorStyle       string   `toml:"behavior_style,omitempty"`
	ReplyStyle          string   `toml:"reply_style,omitempty"`
	GroupChatPrompt     string   `toml:"group_chat_prompt,omitempty"`
	PrivateChatPrompts  string   `toml:"private_chat_prompts,omitempty"`
	MultipleReplyStyle  []string `toml:"multiple_reply_style,omitempty"`
	MultipleProbability float64  `toml:"multiple_probability,omitempty"`
}

type PluginConfig struct {
	Permission                []string `toml:"permission,omitempty"`
	SilentPermissionDenied    bool     `toml:"silent_permission_denied,omitempty"`
	CommandRequireSelfMention bool     `toml:"command_require_self_mention,omitempty"`
	CommandPermissions        any      `toml:"command_permissions,omitempty"`
	DisabledCommands          []string `toml:"disabled_commands,omitempty"`
}

type PluginRuntimeConfig struct {
	Enabled                bool                      `toml:"enabled,omitempty"`
	HealthCheckIntervalSec float64                   `toml:"health_check_interval_sec,omitempty"`
	MaxRestartAttempts     int                       `toml:"max_restart_attempts,omitempty"`
	RunnerSpawnTimeoutSec  float64                   `toml:"runner_spawn_timeout_sec,omitempty"`
	HookBlockingTimeoutSec float64                   `toml:"hook_blocking_timeout_sec,omitempty"`
	IpcSocketPath          string                    `toml:"ipc_socket_path,omitempty"`
	Render                 PluginRuntimeRenderConfig `toml:"render,omitempty"`
}

type PluginRuntimeRenderConfig struct {
	Enabled                      bool     `toml:"enabled,omitempty"`
	BrowserWsEndpoint            string   `toml:"browser_ws_endpoint,omitempty"`
	ExecutablePath               string   `toml:"executable_path,omitempty"`
	BrowserInstallRoot           string   `toml:"browser_install_root,omitempty"`
	Headless                     bool     `toml:"headless,omitempty"`
	LaunchArgs                   []string `toml:"launch_args,omitempty"`
	ConcurrencyLimit             int      `toml:"concurrency_limit,omitempty"`
	StartupTimeoutSec            float64  `toml:"startup_timeout_sec,omitempty"`
	RenderTimeoutSec             float64  `toml:"render_timeout_sec,omitempty"`
	AutoDownloadChromium         bool     `toml:"auto_download_chromium,omitempty"`
	DownloadConnectionTimeoutSec float64  `toml:"download_connection_timeout_sec,omitempty"`
	RestartAfterRenderCount      int      `toml:"restart_after_render_count,omitempty"`
}

type PrivateReplyPauseRule struct {
	Platform string `toml:"platform,omitempty"`
	ItemId   string `toml:"item_id,omitempty"`
	Paused   *bool  `toml:"paused,omitempty"`
}

type ResponsePostProcessConfig struct {
	EnableResponsePostProcess bool    `toml:"enable_response_post_process,omitempty"`
	RemoveBracketContent      bool    `toml:"remove_bracket_content,omitempty"`
	TypingSpeed               float64 `toml:"typing_speed,omitempty"`
}

type ResponseSplitterConfig struct {
	Mode                    string `toml:"mode,omitempty"`
	Enable                  bool   `toml:"enable,omitempty"`
	MaxLength               int    `toml:"max_length,omitempty"`
	MaxSentenceNum          int    `toml:"max_sentence_num,omitempty"`
	MaxSplitNum             int    `toml:"max_split_num,omitempty"`
	EnableKaomojiProtection bool   `toml:"enable_kaomoji_protection,omitempty"`
	EnableOverflowReturnAll bool   `toml:"enable_overflow_return_all,omitempty"`
}

type TalkRulesItem struct {
	Enabled  *bool    `toml:"enabled,omitempty"`
	Platform string   `toml:"platform,omitempty"`
	ItemId   string   `toml:"item_id,omitempty"`
	RuleType string   `toml:"rule_type,omitempty"`
	Time     string   `toml:"time,omitempty"`
	Value    *float64 `toml:"value,omitempty"`
}

// RuleEnabled 未显式关闭时规则生效
func (r TalkRulesItem) RuleEnabled() bool { return r.Enabled == nil || *r.Enabled }

const DefaultTalkRuleGroupName = "默认"

type TalkRuleGroup struct {
	Name  string          `toml:"name,omitempty"`
	Rules []TalkRulesItem `toml:"rules,omitempty"`
}

type TalkRuleGroups struct {
	ActiveGroup string          `toml:"active_group,omitempty"`
	Groups      []TalkRuleGroup `toml:"groups,omitempty"`
}

type TargetItem struct {
	Platform string `toml:"platform,omitempty"`
	ItemId   string `toml:"item_id,omitempty"`
	RuleType string `toml:"rule_type,omitempty"`
}

type TaskConfig struct {
	ModelList         []string `toml:"model_list"`
	MaxTokens         int      `toml:"max_tokens,omitempty"`
	Temperature       float64  `toml:"temperature,omitempty"`
	SelectionStrategy string   `toml:"selection_strategy,omitempty"`
	HardTimeout       float64  `toml:"hard_timeout,omitempty"`
	TemperatureSet    bool     `toml:"-"`
	MaxTokensSet      bool     `toml:"-"`
}

type TelemetryConfig struct {
	Enable bool `toml:"enable,omitempty"`
}

type VisualConfig struct {
	ImageQuality               string                  `toml:"image_quality,omitempty"`
	MaxImageNum                int                     `toml:"max_image_num,omitempty"`
	WaitImageRecognizeMaxTime  float64                 `toml:"wait_image_recognize_max_time,omitempty"`
	HandleOversizedImages      bool                    `toml:"handle_oversized_images,omitempty"`
	MaxImageSizeMb             float64                 `toml:"max_image_size_mb,omitempty"`
	OversizedImageHandleMethod string                  `toml:"oversized_image_handle_method,omitempty"`
	ImageCacheCleanup          ImageCacheCleanupConfig `toml:"image_cache_cleanup,omitempty"`
}

type VoiceConfig struct {
	EnableAsr bool `toml:"enable_asr,omitempty"`
}

type WebConfig struct {
	PreviewChars int `toml:"preview_chars,omitempty"`
	Services     any `toml:"services,omitempty"`
}

type WebRuntimeConfig struct {
	Enabled bool   `toml:"enabled,omitempty"`
	Search  string `toml:"search,omitempty"`
	Fetch   string `toml:"fetch,omitempty"`
}

type WebSearchTaskConfig struct {
	ModelList []string `toml:"model_list"`
}

type WebServiceConfig struct {
	Adapter      string  `toml:"adapter,omitempty"`
	BaseUrl      string  `toml:"base_url,omitempty"`
	ApiKey       string  `toml:"api_key,omitempty"`
	ServerRef    string  `toml:"server_ref,omitempty"`
	Timeout      float64 `toml:"timeout,omitempty"`
	SearchDepth  string  `toml:"search_depth,omitempty"`
	ExtractDepth string  `toml:"extract_depth,omitempty"`
	Category     string  `toml:"category,omitempty"`
}

type WebUIConfig struct {
	// Host/Port 旧 WebUI 监听字段（8.15.62 前）；升级后迁至 http
	Host                     *string `toml:"host,omitempty"`
	Port                     *int    `toml:"port,omitempty"`
	Enabled                  bool    `toml:"enabled,omitempty"`
	Mode                     string  `toml:"mode,omitempty"`
	WebuiStyle               int     `toml:"webui_style,omitempty"`
	AntiCrawlerMode          string  `toml:"anti_crawler_mode,omitempty"`
	AllowedIps               string  `toml:"allowed_ips,omitempty"`
	TrustedProxies           string  `toml:"trusted_proxies,omitempty"`
	TrustXff                 bool    `toml:"trust_xff,omitempty"`
	SecureCookie             bool    `toml:"secure_cookie,omitempty"`
	EnforcePublicOutboundUrl bool    `toml:"enforce_public_outbound_url,omitempty"`
}

// BotConfigFile 对应 MaiBot bot_config.toml 根表（不含 inner.version）
// 字段与 src/config/config.py Config 及 official_configs 等一致
type BotConfigFile struct {
	Agent               AgentConfig               `toml:"agent,omitempty"`
	DesktopPet          DesktopPetConfig          `toml:"desktop_pet,omitempty"`
	Http                HTTPConfig                `toml:"http,omitempty"`
	Files               FilesConfig               `toml:"files,omitempty"`
	Bot                 BotConfig                 `toml:"bot,omitempty"`
	Personality         PersonalityConfig         `toml:"personality,omitempty"`
	Chat                ChatConfig                `toml:"chat,omitempty"`
	Capabilities        CapabilitiesConfig        `toml:"capabilities,omitempty"`
	Experimental        ExperimentalConfig        `toml:"experimental,omitempty"`
	Visual              VisualConfig              `toml:"visual,omitempty"`
	Expression          ExpressionConfig          `toml:"expression,omitempty"`
	Jargon              JargonConfig              `toml:"jargon,omitempty"`
	AMemorix            AMemorixConfig            `toml:"a_memorix,omitempty"`
	MessageReceive      MessageReceiveConfig      `toml:"message_receive,omitempty"`
	Voice               VoiceConfig               `toml:"voice,omitempty"`
	Emoji               EmojiConfig               `toml:"emoji,omitempty"`
	KeywordReaction     KeywordReactionConfig     `toml:"keyword_reaction,omitempty"`
	ResponsePostProcess ResponsePostProcessConfig `toml:"response_post_process,omitempty"`
	// LegacyChineseTypo 8.16.11 前的错字配置；启动时迁入内置 chinese_typo 插件
	LegacyChineseTypo *ChineseTypoConfig     `toml:"-"`
	ResponseSplitter  ResponseSplitterConfig `toml:"response_splitter,omitempty"`
	Telemetry         TelemetryConfig        `toml:"telemetry,omitempty"`
	Log               LogConfig              `toml:"log,omitempty"`
	Debug             DebugConfig            `toml:"debug,omitempty"`
	MaimMessage       MaimMessageConfig      `toml:"maim_message,omitempty"`
	Webui             WebUIConfig            `toml:"webui,omitempty"`
	Mcp               MCPConfig              `toml:"mcp,omitempty"`
	Plugin            PluginConfig           `toml:"plugin,omitempty"`
	PluginRuntime     PluginRuntimeConfig    `toml:"plugin_runtime,omitempty"`

	// LoadMeta 加载 bot_config 时从原始 TOML 提取的键存在信息（不参与序列化）
	LoadMeta BotConfigLoadMeta `toml:"-"`
}

// BotConfigLoadMeta 供升级链判断旧版键是否显式出现
type BotConfigLoadMeta struct {
	RootKeys             map[string]struct{}
	EmojiKeys            map[string]struct{}
	FilesKeys            map[string]struct{}
	ChatRootKeys         map[string]struct{}
	ChatReplyTimingKeys  map[string]struct{}
	ChatReplyStyleKeys   map[string]struct{}
	ExpressionKeys       map[string]struct{}
	JargonKeys           map[string]struct{}
	PersonalityKeys      map[string]struct{}
	HTTPKeys             map[string]struct{}
	PostProcessKeys      map[string]struct{}
	ChineseTypoKeys      map[string]struct{}
	ResponseSplitterKeys map[string]struct{}
}

// ModelConfigFile 对应 MaiBot model_config.toml 根表
type ModelConfigFile struct {
	Web             WebConfig       `toml:"web,omitempty"`
	Models          []ModelInfo     `toml:"models,omitempty"`
	ModelTaskConfig ModelTaskConfig `toml:"model_task_config"`
	ApiProviders    []APIProvider   `toml:"api_providers,omitempty"`

	// LegacyModelTypeKeySet[i] 为 true 表示 models[i] 在 TOML 中显式含 model_type 键（加载时填充，不参与序列化）
	LegacyModelTypeKeySet []bool `toml:"-"`
}
