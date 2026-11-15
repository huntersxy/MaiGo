package llm

import (
	"math"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
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
	if usage == nil {
		return 0
	}
	prompt := int64(usage.PromptTokens)
	hit := int64(usage.PromptTokenDetails.CachedTokens)
	miss := int64(0)
	if miss == 0 && hit > 0 {
		miss = max(prompt-hit, 0)
	}
	if hit+miss == 0 {
		miss = prompt
	}
	priceIn, cachePriceIn, priceOut := resolveModelPrices(cfg, at)
	inputCost := float64(hit)/1_000_000*cachePriceIn + float64(miss)/1_000_000*priceIn
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * priceOut
	return math.Round((inputCost+outputCost)*1_000_000) / 1_000_000
}

// cachedTokenFields 从 eino 统一 usage 构造缓存字段
// eino.TokenUsage 只保留命中数；命中存在但 miss 未上报时，按 Python _extract_usage_record 的 max(prompt-hit,0) 补算
func cachedTokenFields(usage *schema.TokenUsage) (hit, miss int64, enabled bool) {
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
