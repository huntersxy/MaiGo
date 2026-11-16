package pluginmarket

// 官方统计数据读取，与插件清单和下载源的选择无关，对齐 plugin_market/statistics.py

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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

// statisticsSnapshot 读取官方统计摘要；解析失败时让缓存失效，避免脏数据滞留
func (s *Service) statisticsSnapshot(ctx context.Context, forceRefresh bool) (map[string]map[string]any, marketResponse, error) {
	params := url.Values{}
	params.Set("maibot_version", DefaultMaibotVersion())
	params.Set("compatible_only", "false")
	response, err := s.requestMarket(ctx, "plugins/summary", params, forceRefresh)
	if err != nil {
		return nil, marketResponse{}, err
	}
	fail := func(err error) (map[string]map[string]any, marketResponse, error) {
		s.cache.Invalidate(response.SourceURL)
		return nil, marketResponse{}, err
	}
	rawItems, ok := anyList(response.Data["plugins"])
	if !ok {
		return fail(&MarketError{StatusCode: http.StatusBadGateway, Message: "官方统计快照缺少 plugins"})
	}
	stats := make(map[string]map[string]any, len(rawItems))
	for _, raw := range rawItems {
		item, ok := anyMap(raw)
		if !ok {
			continue
		}
		manifest, _ := anyMap(item["manifest"])
		if safeInt(manifest["manifest_version"], 0) != minPluginManifestVersion {
			continue
		}
		record, exists := item["stats"]
		if !exists || record == nil {
			continue
		}
		recordMap, ok := anyMap(record)
		if !ok {
			return fail(fmt.Errorf("官方统计快照包含无效记录"))
		}
		normalized, err := NormalizeStatistics(recordMap)
		if err != nil {
			return fail(err)
		}
		pluginID := safeString(normalized["plugin_id"])
		if _, duplicate := stats[pluginID]; duplicate {
			return fail(fmt.Errorf("官方统计快照中的插件 ID 重复：%s", pluginID))
		}
		stats[pluginID] = normalized
	}
	return stats, response, nil
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
