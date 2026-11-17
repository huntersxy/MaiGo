package pluginmarket

// 官方统计数据读取，与插件清单和下载源的选择无关，对齐 plugin_market/statistics.py

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// NormalizeStatistics 按官方记录原样验证，不补造 ID 或统计读数
func NormalizeStatistics(data map[string]any) (map[string]any, error) {
	if data == nil {
		return nil, fmt.Errorf("统计数据必须是对象")
	}
	pluginID, ok := data["plugin_id"].(string)
	if !ok || strings.TrimSpace(pluginID) == "" {
		return nil, fmt.Errorf("统计数据缺少有效的 plugin_id")
	}
	counts := []string{"downloads", "likes", "dislikes", "rating_count", "comment_count"}
	for _, field := range counts {
		value, ok := statisticInt(data[field])
		if !ok || value < 0 {
			return nil, fmt.Errorf("统计数据字段 %s 必须是非负整数", field)
		}
	}
	rating, ok := statisticFloat(data["rating"])
	if !ok || rating < 0 || rating > 5 {
		return nil, fmt.Errorf("统计数据字段 rating 必须在 0 到 5 之间")
	}
	out := copyAnyMap(data)
	out["plugin_id"] = strings.TrimSpace(pluginID)
	return out, nil
}

func statisticInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case float32:
		if typed != float32(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}

func statisticFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

// attachStatistics 按插件 ID 挂上官方统计；没有记录时保留空值
func attachStatistics(plugins []PluginCatalogItem, summary map[string]map[string]any) []PluginCatalogItem {
	out := make([]PluginCatalogItem, len(plugins))
	copy(out, plugins)
	for index := range out {
		out[index].Stats = summary[out[index].ID]
	}
	return out
}

// parseStatisticsSummary 对齐 _parse_summary：统计汇总按插件 ID 返回对象映射
func parseStatisticsSummary(data map[string]any) (map[string]map[string]any, error) {
	success, _ := data["success"].(bool)
	records, ok := anyMap(data["stats"])
	if !success || !ok {
		return nil, fmt.Errorf("官方统计汇总接口返回格式无效：%v", data)
	}
	stats := make(map[string]map[string]any, len(records))
	for pluginID, raw := range records {
		record, ok := anyMap(raw)
		if !ok {
			return nil, fmt.Errorf("官方统计汇总包含无效的插件记录：%s", pluginID)
		}
		normalized, err := NormalizeStatistics(record)
		if err != nil {
			return nil, err
		}
		if safeString(normalized["plugin_id"]) != pluginID {
			return nil, fmt.Errorf("官方统计汇总返回了不一致的插件记录：%s / %s",
				pluginID, safeString(normalized["plugin_id"]))
		}
		stats[pluginID] = normalized
	}
	return stats, nil
}

// Summary 对齐 get_summary：返回官方统计汇总与缓存元数据
func (s *Service) Summary(ctx context.Context, forceRefresh bool) (map[string]any, error) {
	stats, info, sourceURL, err := s.statisticsSnapshot(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success":    true,
		"stats":      stats,
		"source":     "official",
		"source_url": sourceURL,
		"cache":      info,
	}, nil
}

// statisticsSnapshot 读取独立统计汇总；只有验证成功的数据才能进入缓存
func (s *Service) statisticsSnapshot(ctx context.Context, forceRefresh bool) (map[string]map[string]any, CacheInfo, string, error) {
	targetURL := marketBaseURL() + "/stats/summary"
	loader := func() (any, error) {
		data, err := (GitSource{}).ReadJSON(ctx, targetURL, requestTimeout)
		if err != nil {
			return nil, err
		}
		return parseStatisticsSummary(data)
	}
	data, info, err := s.cache.Get(targetURL, loader, marketTTL, forceRefresh)
	if err != nil {
		return nil, CacheInfo{}, "", err
	}
	stats, err := statisticsSummaryMap(data)
	if err != nil {
		return nil, CacheInfo{}, "", err
	}
	return stats, info, targetURL, nil
}

// statisticsSummaryMap 缓存命中时 JSON 副本会变成 map[string]any，这里恢复按插件 ID 的映射
func statisticsSummaryMap(data any) (map[string]map[string]any, error) {
	switch typed := data.(type) {
	case map[string]map[string]any:
		return typed, nil
	case map[string]any:
		stats := make(map[string]map[string]any, len(typed))
		for pluginID, raw := range typed {
			record, ok := anyMap(raw)
			if !ok {
				return nil, fmt.Errorf("官方统计汇总响应格式无效")
			}
			stats[pluginID] = record
		}
		return stats, nil
	default:
		return nil, fmt.Errorf("官方统计汇总响应格式无效")
	}
}

// statisticsPlugin 读取单个插件的官方统计，独立缓存评论与计数
func (s *Service) statisticsPlugin(ctx context.Context, pluginID string, forceRefresh bool) (map[string]any, CacheInfo, string, error) {
	trimmed := strings.TrimSpace(pluginID)
	if trimmed == "" {
		return nil, CacheInfo{}, "", fmt.Errorf("plugin_id 不能为空")
	}
	targetURL := fmt.Sprintf("%s/stats/%s", marketBaseURL(), quoteSegment(trimmed))
	loader := func() (any, error) {
		data, err := (GitSource{}).ReadJSON(ctx, targetURL, requestTimeout)
		if err != nil {
			return nil, err
		}
		if success, _ := data["success"].(bool); !success {
			return nil, &MarketError{StatusCode: http.StatusBadGateway, Message: fmt.Sprintf("官方统计接口返回失败：%v", data)}
		}
		record, ok := anyMap(data["stats"])
		if !ok {
			return nil, fmt.Errorf("官方统计响应格式无效")
		}
		if actual := safeString(record["plugin_id"]); actual != trimmed {
			return nil, fmt.Errorf("官方统计返回了其他插件的记录：%s / %s", trimmed, actual)
		}
		return NormalizeStatistics(record)
	}
	data, info, err := s.cache.Get(targetURL, loader, marketTTL, forceRefresh)
	if err != nil {
		return nil, CacheInfo{}, "", err
	}
	stats, ok := anyMap(data)
	if !ok {
		return nil, CacheInfo{}, "", fmt.Errorf("官方统计响应格式无效")
	}
	return stats, info, targetURL, nil
}
