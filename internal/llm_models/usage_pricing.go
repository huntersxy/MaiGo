package llm

import (
	"math"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/llm_models/contract"
)

// resolveModelPrices 对齐 MaiBot LLMUsageRecorder._resolve_prices：
// 按请求开始时刻的本地 HH:MM 选择 price_periods，未匹配时使用模型基础价格
func resolveModelPrices(cfg ModelConfig, at time.Time) (priceIn, cachePriceIn, priceOut float64) {
	if at.IsZero() {
		at = time.Now()
	}
	local := at.Local()
	minutes := local.Hour()*60 + local.Minute()
	for _, period := range cfg.PricePeriods {
		start, okStart := parsePriceClock(period.StartTime)
		end, okEnd := parsePriceClock(period.EndTime)
		if !okStart || !okEnd {
			continue
		}
		if pricePeriodContains(start, end, minutes) {
			return period.PriceIn, period.CachePriceIn, period.PriceOut
		}
	}
	return cfg.PriceIn, cfg.CachePriceIn, cfg.PriceOut
}

func parsePriceClock(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		parsed, err = time.Parse("15:04:05", value)
	}
	if err != nil {
		return 0, false
	}
	return parsed.Hour()*60 + parsed.Minute(), true
}

func pricePeriodContains(start, end, current int) bool {
	if start < end {
		return current >= start && current < end
	}
	// 跨午夜时段：Python 判断 current >= start or current < end
	return current >= start || current < end
}

// calculateModelUsageCost 对齐 MaiBot _calculate_input_cost + output cost，结果 round 6
func calculateModelUsageCost(cfg ModelConfig, usage *schema.TokenUsage, at time.Time) float64 {
	return calculateModelUsageCostWithDetails(cfg, usage, at, nil)
}

func calculateModelUsageCostWithDetails(
	cfg ModelConfig,
	usage *schema.TokenUsage,
	at time.Time,
	details *contract.TokenUsageDetails,
) float64 {
	if usage == nil {
		return 0
	}
	prompt := int64(usage.PromptTokens)
	hit, miss := int64(usage.PromptTokenDetails.CachedTokens), int64(0)
	if details != nil {
		hit = int64(details.PromptCacheHitTokens)
		miss = int64(details.PromptCacheMissTokens)
	}
	priceIn, cachePriceIn, priceOut := resolveModelPrices(cfg, at)
	return CalculateUsageCost(priceIn, cachePriceIn, priceOut, prompt, int64(usage.CompletionTokens), hit, miss)
}

// CalculateUsageCost 按百万 token 单价计算费用，对齐 Python round(..., 6) 的银行家舍入
func CalculateUsageCost(priceIn, cachePriceIn, priceOut float64, prompt, completion, hit, miss int64) float64 {
	if miss == 0 && hit > 0 {
		miss = max(prompt-hit, 0)
	}
	if hit+miss == 0 {
		miss = prompt
	}
	inputCost := float64(hit)/1_000_000*cachePriceIn + float64(miss)/1_000_000*priceIn
	outputCost := float64(completion) / 1_000_000 * priceOut
	return math.RoundToEven((inputCost+outputCost)*1_000_000) / 1_000_000
}

// cachedTokenFields 从 eino 统一 usage 构造缓存字段
func cachedTokenFields(usage *schema.TokenUsage, details *contract.TokenUsageDetails) (hit, miss int64, enabled bool) {
	if details != nil {
		return int64(max(details.PromptCacheHitTokens, 0)),
			int64(max(details.PromptCacheMissTokens, 0)),
			details.PromptCacheReported
	}
	if usage == nil {
		return 0, 0, false
	}
	hit = int64(usage.PromptTokenDetails.CachedTokens)
	if hit > 0 {
		miss = max(int64(usage.PromptTokens)-hit, 0)
		return hit, miss, true
	}
	return 0, 0, false
}
