package services

// LLM 缓存统计接线
// internal/llm_models 在每次 chat/gemini 生成后上报 CacheUsageEvent；这里接到进程内聚合。
// 启用开关来自 debug.enable_llm_cache_stats，由 bootstrap 调 ConfigureLLMCacheStatsFromInstance。

import (
	maibotcfg "maibot-go/internal/config/maibot"
	llm "maibot-go/internal/llm_models"
)

func init() {
	llm.ConfigureCacheUsageRecorder(func(event llm.CacheUsageEvent) {
		RecordLLMCacheUsage(event.TaskName, event.RequestType, event.ModelName, event.SessionID, event.PromptTokens, event.HitTokens, event.MissTokens, event.PromptText)
	})
}

// ConfigureLLMCacheStatsFromInstance 按实例配置启用/关闭缓存统计
func ConfigureLLMCacheStatsFromInstance(cfg *maibotcfg.InstanceConfig) {
	enabled := cfg != nil && cfg.Bot.Debug.EnableLlmCacheStats
	setLLMCacheStatsEnabled(func() bool { return enabled })
}

// ConfigureLLMCacheStatsEnabled 显式设置启用开关（测试或运维入口）
func ConfigureLLMCacheStatsEnabled(enabled bool) {
	setLLMCacheStatsEnabled(func() bool { return enabled })
}

func setLLMCacheStatsEnabled(enabled func() bool) {
	if enabled == nil {
		enabled = func() bool { return false }
	}
	globalLLMCacheStore.mu.Lock()
	defer globalLLMCacheStore.mu.Unlock()
	globalLLMCacheStore.enabled = enabled
}
