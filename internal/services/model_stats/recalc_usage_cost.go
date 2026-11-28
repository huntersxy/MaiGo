package modelstats

// 按当前模型目录价格重算 llm_usage.cost，并重建小时汇总。
// 用于修复共享目录价格未加载期间写入的 cost=0 脏数据；不改变计费公式。

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	maibotcfg "MaiGo/internal/config/maibot"

	"gorm.io/gorm"
)

// UsageCostRecalcReport 重算结果摘要
type UsageCostRecalcReport struct {
	Scanned           int64
	Updated           int64
	EmbeddingUpdated  int64
	SkippedNoPrice    int64
	SkippedUnchanged  int64
	CostDelta         float64
	AggregatesRebuilt bool
}

type usageCostPrice struct {
	PriceIn      float64
	CachePriceIn float64
	PriceOut     float64
}

// RecalcUsageCosts 用实例当前模型目录价格重算 cost=0 的用量行，并重建小时汇总
func RecalcUsageCosts(ctx context.Context, instanceDir string, db *gorm.DB) (UsageCostRecalcReport, error) {
	if db == nil {
		return UsageCostRecalcReport{}, fmt.Errorf("数据库未初始化")
	}
	prices, err := loadUsageCostPrices(instanceDir)
	if err != nil {
		return UsageCostRecalcReport{}, err
	}
	report, err := updateZeroUsageCosts(ctx, db, prices)
	if err != nil {
		return report, err
	}
	if err := rebuildUsageAggregates(ctx, db); err != nil {
		return report, err
	}
	report.AggregatesRebuilt = true
	clearStatisticsCaches(instanceDir)
	return report, nil
}

func loadUsageCostPrices(instanceDir string) (map[string]usageCostPrice, error) {
	instanceDir = filepath.Clean(strings.TrimSpace(instanceDir))
	if instanceDir == "" {
		return nil, fmt.Errorf("实例目录不能为空")
	}
	catalogPath := filepath.Join(instanceDir, "host", maibotcfg.CatalogFilename)
	if _, err := os.Stat(catalogPath); err == nil {
		if err := os.Setenv(maibotcfg.SharedModelsEnv, catalogPath); err != nil {
			return nil, fmt.Errorf("设置共享模型目录失败: %w", err)
		}
	}
	cfg, err := maibotcfg.LoadFromInstanceDir(instanceDir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]usageCostPrice, len(cfg.Model.Models)*2)
	for _, model := range cfg.Model.Models {
		price := usageCostPrice{
			PriceIn:      model.PriceIn,
			CachePriceIn: model.CachePriceIn,
			PriceOut:     model.PriceOut,
		}
		if name := strings.TrimSpace(model.Name); name != "" {
			out[name] = price
		}
		if id := strings.TrimSpace(model.ModelIdentifier); id != "" {
			if _, exists := out[id]; !exists {
				out[id] = price
			}
		}
	}
	return out, nil
}

func updateZeroUsageCosts(ctx context.Context, db *gorm.DB, prices map[string]usageCostPrice) (UsageCostRecalcReport, error) {
	var report UsageCostRecalcReport
	type row struct {
		ID                    int64
		ModelName             string
		ModelAssignName       string
		TaskName              string
		PromptTokens          int64
		CompletionTokens      int64
		PromptCacheHitTokens  int64
		PromptCacheMissTokens int64
		Cost                  float64
	}
	var rows []row
	if err := db.WithContext(ctx).Raw(`
		SELECT id, model_name, model_assign_name, task_name,
			COALESCE(prompt_tokens, 0) AS prompt_tokens,
			COALESCE(completion_tokens, 0) AS completion_tokens,
			COALESCE(prompt_cache_hit_tokens, 0) AS prompt_cache_hit_tokens,
			COALESCE(prompt_cache_miss_tokens, 0) AS prompt_cache_miss_tokens,
			COALESCE(cost, 0) AS cost
		FROM llm_usage
		WHERE cost IS NULL OR cost = 0`).Scan(&rows).Error; err != nil {
		return report, fmt.Errorf("读取零费用用量失败: %w", err)
	}
	report.Scanned = int64(len(rows))
	for _, item := range rows {
		price, ok := resolveUsageCostPrice(prices, item.ModelAssignName, item.ModelName)
		if !ok {
			report.SkippedNoPrice++
			continue
		}
		newCost := calculateUsageCost(
			price.PriceIn, price.CachePriceIn, price.PriceOut,
			item.PromptTokens, item.CompletionTokens, item.PromptCacheHitTokens, item.PromptCacheMissTokens,
		)
		if absFloat(newCost-item.Cost) < 1e-15 {
			report.SkippedUnchanged++
			continue
		}
		if err := db.WithContext(ctx).Exec(`UPDATE llm_usage SET cost = ? WHERE id = ?`, newCost, item.ID).Error; err != nil {
			return report, fmt.Errorf("更新用量费用失败 (id=%d): %w", item.ID, err)
		}
		report.Updated++
		report.CostDelta += newCost - item.Cost
		if item.TaskName == "embedding" || item.TaskName == "image_embedding" {
			report.EmbeddingUpdated++
		}
	}
	return report, nil
}

func resolveUsageCostPrice(prices map[string]usageCostPrice, assignName, modelName string) (usageCostPrice, bool) {
	for _, key := range []string{strings.TrimSpace(assignName), strings.TrimSpace(modelName)} {
		if key == "" {
			continue
		}
		if price, ok := prices[key]; ok {
			return price, true
		}
	}
	return usageCostPrice{}, false
}

func rebuildUsageAggregates(ctx context.Context, db *gorm.DB) error {
	now := time.Now().UTC()
	if err := db.WithContext(ctx).Exec(`DELETE FROM statistics_model_hourly`).Error; err != nil {
		return fmt.Errorf("清空 model 小时汇总失败: %w", err)
	}
	if err := db.WithContext(ctx).Exec(`DELETE FROM statistics_usage_hourly`).Error; err != nil {
		return fmt.Errorf("清空 usage 小时汇总失败: %w", err)
	}
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO statistics_model_hourly(
			bucket_time, request_type, module_name, provider_name, model_name,
			request_count, prompt_tokens, completion_tokens, total_tokens, cost, time_cost_sum, time_cost_sq_sum
		)
		SELECT
			datetime(strftime('%Y-%m-%d %H:00:00', timestamp)) AS bucket_time,
			COALESCE(request_type, 'unknown') AS request_type,
			CASE WHEN instr(COALESCE(request_type, 'unknown'), '.') > 0
				THEN substr(COALESCE(request_type, 'unknown'), 1, instr(COALESCE(request_type, 'unknown'), '.') - 1)
				ELSE COALESCE(request_type, 'unknown') END AS module_name,
			COALESCE(model_api_provider_name, 'unknown') AS provider_name,
			COALESCE(model_assign_name, model_name, 'unknown') AS model_name,
			COUNT(*) AS request_count,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(cost), 0) AS cost,
			COALESCE(SUM(time_cost), 0) AS time_cost_sum,
			COALESCE(SUM(time_cost * time_cost), 0) AS time_cost_sq_sum
		FROM llm_usage
		WHERE timestamp IS NOT NULL
		GROUP BY 1, 2, 4, 5`).Error; err != nil {
		return fmt.Errorf("重建 model 小时汇总失败: %w", err)
	}
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO statistics_usage_hourly(
			bucket_time, request_type, provider_name, model_name, task_name, session_id,
			request_count, prompt_tokens, completion_tokens, cache_hit_tokens, cache_miss_tokens, cost,
			duration_count, duration_sum, duration_sum_sq, response_time_count, response_time_sum
		)
		SELECT
			datetime(strftime('%Y-%m-%d %H:00:00', timestamp)) AS bucket_time,
			COALESCE(NULLIF(request_type, ''), 'unknown') AS request_type,
			COALESCE(NULLIF(model_api_provider_name, ''), 'unknown') AS provider_name,
			COALESCE(NULLIF(model_assign_name, ''), NULLIF(model_name, ''), 'unknown') AS model_name,
			COALESCE(NULLIF(task_name, ''), 'unknown') AS task_name,
			COALESCE(session_id, '') AS session_id,
			COUNT(*) AS request_count,
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(CASE WHEN prompt_cache_enabled THEN prompt_cache_hit_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN prompt_cache_enabled THEN
				CASE
					WHEN prompt_cache_miss_tokens > 0 THEN prompt_cache_miss_tokens
					WHEN prompt_cache_hit_tokens > 0 THEN MAX(prompt_tokens - prompt_cache_hit_tokens, 0)
					ELSE prompt_tokens
				END
			ELSE 0 END), 0),
			COALESCE(SUM(cost), 0),
			SUM(CASE WHEN time_cost IS NOT NULL THEN 1 ELSE 0 END),
			COALESCE(SUM(COALESCE(time_cost, 0)), 0),
			COALESCE(SUM(COALESCE(time_cost, 0) * COALESCE(time_cost, 0)), 0),
			SUM(CASE WHEN time_cost IS NOT NULL THEN 1 ELSE 0 END),
			COALESCE(SUM(COALESCE(time_cost, 0)), 0)
		FROM llm_usage
		WHERE timestamp IS NOT NULL
		GROUP BY 1, 2, 3, 4, 5, 6`).Error; err != nil {
		return fmt.Errorf("重建 usage 小时汇总失败: %w", err)
	}
	var maxID int64
	if err := db.WithContext(ctx).Raw(`SELECT COALESCE(MAX(id), 0) FROM llm_usage`).Scan(&maxID).Error; err != nil {
		return fmt.Errorf("读取用量最大 id 失败: %w", err)
	}
	for _, source := range []string{"model_usage", "usage_hourly_v1"} {
		if err := db.WithContext(ctx).Exec(`
			INSERT INTO statistics_aggregation_cursors(source_name, last_processed_id, updated_at)
			VALUES(?, ?, ?)
			ON CONFLICT(source_name) DO UPDATE SET
				last_processed_id = excluded.last_processed_id,
				updated_at = excluded.updated_at`, source, maxID, now).Error; err != nil {
			return fmt.Errorf("更新汇总游标失败 (%s): %w", source, err)
		}
	}
	return nil
}

func clearStatisticsCaches(instanceDir string) {
	dir := filepath.Join(instanceDir, "data", "statistics")
	for _, name := range []string{"detailed_snapshot.json", "dashboard_cache.json"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// calculateUsageCost 对齐 llm.CalculateUsageCost / Python round(..., 6)
func calculateUsageCost(priceIn, cachePriceIn, priceOut float64, prompt, completion, hit, miss int64) float64 {
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
