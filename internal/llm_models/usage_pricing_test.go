package llm

import (
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// TestCalculateModelUsageCost 验证配置价格后按命中/未命中与输出 token 计费
func TestCalculateModelUsageCost(t *testing.T) {
	cfg := ModelConfig{PriceIn: 1, CachePriceIn: 0.5, PriceOut: 2}
	usage := &schema.TokenUsage{
		PromptTokens:       1000,
		CompletionTokens:   500,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 200},
	}
	got := calculateModelUsageCost(cfg, usage, time.Now())
	const want = 0.0019
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("calculateModelUsageCost() = %v, want %v", got, want)
	}
}
