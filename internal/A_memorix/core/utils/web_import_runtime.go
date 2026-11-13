package utils

// Web 导入运行时配置与就绪检查，对齐 web_import_manager.py

import (
	"fmt"
	"math"
	"strings"
)

// maxPasteChars 对齐 _max_paste_chars
func (m *WebImportManager) maxPasteChars() int {
	return webImportMaxInt(1000, m.cfgInt("web.import.max_paste_chars", 200000))
}

// defaultFileConcurrency 对齐 _default_file_concurrency
func (m *WebImportManager) defaultFileConcurrency() int {
	return webImportMaxInt(1, m.cfgInt("web.import.default_file_concurrency", 2))
}

// defaultChunkConcurrency 对齐 _default_chunk_concurrency
func (m *WebImportManager) defaultChunkConcurrency() int {
	return webImportMaxInt(1, m.cfgInt("web.import.default_chunk_concurrency", 4))
}

// defaultNarrativeWindowSize 对齐 _default_narrative_window_size
func (m *WebImportManager) defaultNarrativeWindowSize() int {
	return webImportMaxInt(200, m.cfgInt("web.import.default_narrative_window_size", 1600))
}

// defaultNarrativeOverlap 对齐 _default_narrative_overlap
func (m *WebImportManager) defaultNarrativeOverlap() int {
	windowSize := m.defaultNarrativeWindowSize()
	return webImportClamp(m.cfgInt("web.import.default_narrative_overlap", 400), 0, webImportMaxInt(0, windowSize-1))
}

// defaultFactualTargetSize 对齐 _default_factual_target_size
func (m *WebImportManager) defaultFactualTargetSize() int {
	return webImportMaxInt(200, m.cfgInt("web.import.default_factual_target_size", 1200))
}

// maxImportChunkChars 对齐 _max_import_chunk_chars
func (m *WebImportManager) maxImportChunkChars() int {
	return webImportMaxInt(200, m.cfgInt("web.import.max_chunk_chars", 3200))
}

// maxFileConcurrency 对齐 _max_file_concurrency
func (m *WebImportManager) maxFileConcurrency() int {
	return webImportMaxInt(1, m.cfgInt("web.import.max_file_concurrency", 6))
}

// maxChunkConcurrency 对齐 _max_chunk_concurrency
func (m *WebImportManager) maxChunkConcurrency() int {
	return webImportMaxInt(1, m.cfgInt("web.import.max_chunk_concurrency", 12))
}

// timeoutConfig 对齐 _timeout_config
func (m *WebImportManager) timeoutConfig() map[string]float64 {
	return map[string]float64{
		"llm_call_seconds":          math.Max(0, m.cfgFloat("web.import.timeout.llm_call_seconds", 240)),
		"process_poll_seconds":      math.Max(0.1, m.cfgFloat("web.import.timeout.process_poll_seconds", 1)),
		"process_terminate_seconds": math.Max(0.1, m.cfgFloat("web.import.timeout.process_terminate_seconds", 5)),
		"process_kill_seconds":      math.Max(0.1, m.cfgFloat("web.import.timeout.process_kill_seconds", 3)),
		"convert_preflight_seconds": math.Max(0.1, m.cfgFloat("web.import.timeout.convert_preflight_seconds", 20)),
	}
}

// llmRetryConfig 对齐 _llm_retry_config
func (m *WebImportManager) llmRetryConfig() map[string]float64 {
	timeout := m.timeoutConfig()
	retries := webImportMaxInt(0, m.cfgInt("web.import.llm_retry.max_attempts", 4))
	minWait := math.Max(0.1, webImportCoerceFloat(m.cfg("web.import.llm_retry.min_wait_seconds", 3), 3))
	maxWait := math.Max(minWait, webImportCoerceFloat(m.cfg("web.import.llm_retry.max_wait_seconds", 40), 40))
	multiplier := math.Max(1, webImportCoerceFloat(m.cfg("web.import.llm_retry.backoff_multiplier", 3), 3))
	return map[string]float64{"retries": float64(retries), "min_wait": minWait, "max_wait": maxWait, "multiplier": multiplier, "llm_call_seconds": timeout["llm_call_seconds"]}
}

// isEnabled 对齐 _is_enabled
func (m *WebImportManager) isEnabled() bool {
	return webImportCoerceBool(m.cfg("web.import.enabled", true), true)
}

// queueLimit 对齐 _queue_limit
func (m *WebImportManager) queueLimit() int {
	return webImportMaxInt(1, m.cfgInt("web.import.max_queue_size", 20))
}

// maxFilesPerTask 对齐 _max_files_per_task
func (m *WebImportManager) maxFilesPerTask() int {
	return webImportMaxInt(1, m.cfgInt("web.import.max_files_per_task", 200))
}

// maxFileSizeBytes 对齐 _max_file_size_bytes
func (m *WebImportManager) maxFileSizeBytes() int64 {
	mb := webImportMaxInt(1, m.cfgInt("web.import.max_file_size_mb", 20))
	return int64(mb) * 1024 * 1024
}

// getRuntimeSettings 对齐 get_runtime_settings
func (m *WebImportManager) GetRuntimeSettings() map[string]any {
	return map[string]any{
		"max_queue_size":                m.queueLimit(),
		"max_files_per_task":            m.maxFilesPerTask(),
		"max_file_size_mb":              m.cfgInt("web.import.max_file_size_mb", 20),
		"max_paste_chars":               m.maxPasteChars(),
		"default_file_concurrency":      m.defaultFileConcurrency(),
		"default_chunk_concurrency":     m.defaultChunkConcurrency(),
		"default_narrative_window_size": m.defaultNarrativeWindowSize(),
		"default_narrative_overlap":     m.defaultNarrativeOverlap(),
		"default_factual_target_size":   m.defaultFactualTargetSize(),
		"max_chunk_chars":               m.maxImportChunkChars(),
		"max_file_concurrency":          m.maxFileConcurrency(),
		"max_chunk_concurrency":         m.maxChunkConcurrency(),
		"poll_interval_ms":              webImportMaxInt(200, m.cfgInt("web.import.poll_interval_ms", 1000)),
		"maibot_source_db_default":      m.defaultMaibotSourceDB(),
		"maibot_target_data_dir":        m.resolveDataDir(),
		"path_aliases":                  m.GetPathAliases(),
		"llm_retry":                     m.llmRetryConfig(),
		"timeout":                       m.timeoutConfig(),
	}
}

// ensureReady 对齐 _ensure_ready
func (m *WebImportManager) ensureReady() error {
	missing := []string{}
	if m.deps.Store == nil {
		missing = append(missing, "metadata_store")
	}
	if m.deps.Graph == nil {
		missing = append(missing, "graph_store")
	}
	if m.deps.Embedding == nil {
		missing = append(missing, "embedding_manager")
	}
	if !m.allowMetadataOnlyWrite() {
		if m.paragraphVectorStore() == nil {
			missing = append(missing, "paragraph_vector_store")
		}
		if m.graphVectorStore() == nil {
			missing = append(missing, "graph_vector_store")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("导入依赖未初始化: %s", strings.Join(missing, ", "))
	}
	if m.deps.RuntimeReady != nil && !m.deps.RuntimeReady() {
		return fmt.Errorf("插件运行时未就绪，请先完成 on_enable 初始化")
	}
	return nil
}
