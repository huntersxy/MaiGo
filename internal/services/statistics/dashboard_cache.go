package stats

import (
	"strconv"
	"time"

	"github.com/bytedance/sonic"
)

const (
	dashboardCacheKey     = "webui_dashboard_statistics_cache"
	dashboardCacheVersion = 6
	dashboardCacheMaxAge  = 20 * time.Minute
)

type dashboardCacheSnapshot struct {
	Version     int                       `json:"version"`
	GeneratedAt float64                   `json:"generated_at"`
	Entries     map[string]map[string]any `json:"entries"`
}

func loadDashboardStatisticsCache(instanceDir string, hours int, now time.Time) (DashboardData, bool) {
	store, err := openTelemetryStore(instanceDir)
	if err != nil {
		return DashboardData{}, false
	}
	raw := store.Get(dashboardCacheKey)
	if raw == nil {
		return DashboardData{}, false
	}
	encoded, err := sonic.Marshal(raw)
	if err != nil {
		return DashboardData{}, false
	}
	var snapshot dashboardCacheSnapshot
	if err := sonic.Unmarshal(encoded, &snapshot); err != nil {
		return DashboardData{}, false
	}
	if snapshot.Version != dashboardCacheVersion || snapshot.GeneratedAt <= 0 {
		return DashboardData{}, false
	}
	generatedAt := time.Unix(int64(snapshot.GeneratedAt), 0)
	if now.Sub(generatedAt) > dashboardCacheMaxAge {
		return DashboardData{}, false
	}
	entry, ok := snapshot.Entries[formatDashboardHours(hours)]
	if !ok || entry == nil {
		return DashboardData{}, false
	}
	expanded := expandDashboardCacheEntry(entry, hours, snapshot.GeneratedAt)
	body, err := sonic.Marshal(expanded)
	if err != nil {
		return DashboardData{}, false
	}
	var data DashboardData
	if err := sonic.Unmarshal(body, &data); err != nil {
		return DashboardData{}, false
	}
	return data, true
}

func updateDashboardStatisticsCacheEntry(instanceDir string, hours int, data DashboardData, now time.Time) error {
	store, err := openTelemetryStore(instanceDir)
	if err != nil {
		return err
	}
	entries := map[string]map[string]any{}
	if raw := store.Get(dashboardCacheKey); raw != nil {
		encoded, err := sonic.Marshal(raw)
		if err == nil {
			var previous dashboardCacheSnapshot
			if sonic.Unmarshal(encoded, &previous) == nil && previous.Entries != nil {
				entries = previous.Entries
			}
		}
	}
	entries[formatDashboardHours(hours)] = compactDashboardCacheEntry(data)
	return store.Set(dashboardCacheKey, dashboardCacheSnapshot{
		Version:     dashboardCacheVersion,
		GeneratedAt: float64(now.UnixNano()) / 1e9,
		Entries:     entries,
	})
}

func formatDashboardHours(hours int) string {
	if hours <= 0 {
		hours = 24
	}
	return strconv.Itoa(hours)
}

func compactDashboardCacheEntry(data DashboardData) map[string]any {
	raw, err := sonic.Marshal(data)
	if err != nil {
		return map[string]any{}
	}
	entry := map[string]any{}
	if err := sonic.Unmarshal(raw, &entry); err != nil {
		return map[string]any{}
	}
	for _, field := range []string{"hourly_data", "daily_data"} {
		series, _ := entry[field].([]any)
		filtered := make([]any, 0, len(series))
		for _, item := range series {
			if isEmptyTimeSeriesItem(item) {
				continue
			}
			filtered = append(filtered, item)
		}
		entry[field] = filtered
	}
	entry["sparse"] = true
	return entry
}

func isEmptyTimeSeriesItem(value any) bool {
	item, ok := value.(map[string]any)
	if !ok {
		return false
	}
	return numberValue(item["requests"]) == 0 &&
		numberValue(item["cost"]) == 0 &&
		numberValue(item["tokens"]) == 0 &&
		numberValue(item["online_seconds"]) == 0
}

func expandDashboardCacheEntry(entry map[string]any, hours int, generatedAt float64) map[string]any {
	expanded := make(map[string]any, len(entry))
	for key, value := range entry {
		expanded[key] = value
	}
	if sparse, _ := entry["sparse"].(bool); !sparse {
		return expanded
	}
	generated := time.Unix(int64(generatedAt), 0).In(time.Local)
	start := generated.Add(-time.Duration(hours) * time.Hour)
	expanded["hourly_data"] = expandTimeSeries(entry["hourly_data"], start, generated, time.Hour, "2006-01-02T15:00:00")
	expanded["daily_data"] = expandTimeSeries(entry["daily_data"], start, generated, 24*time.Hour, "2006-01-02T00:00:00")
	delete(expanded, "sparse")
	return expanded
}

func expandTimeSeries(raw any, start, end time.Time, step time.Duration, layout string) []any {
	series, _ := raw.([]any)
	byTimestamp := make(map[string]any, len(series))
	for _, item := range series {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := obj["timestamp"].(string)
		if key == "" {
			continue
		}
		byTimestamp[key] = obj
	}
	cursor := start
	if step >= 24*time.Hour {
		cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, time.Local)
	} else {
		cursor = floorLocalHour(cursor)
	}
	out := make([]any, 0, int(end.Sub(cursor)/step)+1)
	for !cursor.After(end) {
		key := cursor.Format(layout)
		if item, ok := byTimestamp[key]; ok {
			out = append(out, item)
		} else {
			out = append(out, map[string]any{
				"timestamp":         key,
				"online_seconds":    0.0,
				"requests":          0,
				"cost":              0.0,
				"tokens":            0,
				"input_tokens":      0,
				"output_tokens":     0,
				"cache_hit_tokens":  0,
				"cache_miss_tokens": 0,
			})
		}
		cursor = cursor.Add(step)
	}
	return out
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}
