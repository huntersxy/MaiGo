package contract

// TokenUsageDetails 对齐 MaiBot UsageRecord 的 Prompt 缓存用量。
// eino.TokenUsage 只有命中 token，供应商显式上报的未命中 token 与上报标志单独传递。
type TokenUsageDetails struct {
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
	PromptCacheReported   bool
}
