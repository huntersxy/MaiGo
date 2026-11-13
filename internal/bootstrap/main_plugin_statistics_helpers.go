package bootstrap

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

func pluginStatisticsStart(dialect string, days int) any {
	return pluginStatisticsStartAt(dialect, days, time.Now())
}

func pluginStatisticsStartAt(dialect string, days int, now time.Time) any {
	start := now.AddDate(0, 0, -days)
	if dialect == "sqlite" {
		return start.Format("2006-01-02 15:04:05")
	}
	return start
}

func pluginStatisticsDays(args map[string]any) (int, error) {
	days, err := pluginStatisticsIntArg(args, "days", 7)
	if err != nil {
		return 0, err
	}
	if days <= 0 {
		return 0, fmt.Errorf("days 必须为正整数")
	}
	if days > 365 {
		days = 365
	}
	return days, nil
}

func pluginStatisticsLimit(args map[string]any, key string, fallback int) (int, error) {
	limit, err := pluginStatisticsIntArg(args, key, fallback)
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, fmt.Errorf("%s 必须为正整数", key)
	}
	if limit > 50 {
		limit = 50
	}
	return limit, nil
}

func pluginStatisticsBucket(args map[string]any) (string, error) {
	bucket := strings.ToLower(strings.TrimSpace(pluginStringArg(args, "bucket")))
	switch bucket {
	case "", "d", "day", "days", "1d", "天", "按天":
		return "day", nil
	case "h", "hour", "hours", "1h", "小时", "按小时":
		return "hour", nil
	default:
		return "", fmt.Errorf("bucket 只支持 hour 或 day")
	}
}

func pluginStatisticsBucketSQL(dialect, column, bucket string) string {
	if dialect == "sqlite" {
		if bucket == "hour" {
			return "strftime('%Y-%m-%d %H:00:00', " + column + ")"
		}
		return "strftime('%Y-%m-%d 00:00:00', " + column + ")"
	}
	if bucket == "hour" {
		return "to_char(date_trunc('hour', " + column + "), 'YYYY-MM-DD HH24:MI:SS')"
	}
	return "to_char(date_trunc('day', " + column + "), 'YYYY-MM-DD 00:00:00')"
}

func pluginStatisticsTokenGroup(groupBy string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "", "model":
		return "model_name", "模型"
	case "module":
		return "module_name", "模块"
	case "provider":
		return "provider_name", "服务商"
	case "type":
		return "request_type", "请求类型"
	default:
		return "", ""
	}
}

func pluginStatisticsModelMetric(metric string) (string, string, string) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "request", "requests", "count", "次数":
		return "SUM(request_count)", "metric_value", "SUM(request_count)"
	case "cost", "费用", "花费":
		return "SUM(cost)", "metric_value", "SUM(cost)"
	case "latency", "time", "耗时", "延迟":
		return "SUM(time_cost_sum) / NULLIF(SUM(request_count), 0)", "metric_value", "SUM(request_count)"
	default:
		return "SUM(total_tokens)", "metric_value", "SUM(total_tokens)"
	}
}

func pluginStatisticsSeries(rows []map[string]any, labelColumn, valueColumn string, ordered []string) map[string]any {
	buckets := map[string]map[string]float64{}
	for _, row := range rows {
		bucket := pluginStatisticsString(row["bucket_label"])
		if bucket == "" {
			continue
		}
		label := pluginStatisticsString(row[labelColumn])
		if label == "" {
			label = "Unknown"
		}
		if buckets[bucket] == nil {
			buckets[bucket] = map[string]float64{}
		}
		buckets[bucket][label] = pluginStatisticsFloat(row[valueColumn])
	}
	timestamps := make([]string, 0, len(buckets))
	for timestamp := range buckets {
		timestamps = append(timestamps, timestamp)
	}
	sort.Strings(timestamps)
	used := map[string]bool{}
	valuesByKey := map[string][]float64{}
	labelsByKey := map[string]string{}
	for _, label := range ordered {
		key := pluginStatisticsLabelKey(label, used)
		values := make([]float64, 0, len(timestamps))
		for _, timestamp := range timestamps {
			values = append(values, buckets[timestamp][label])
		}
		valuesByKey[key] = values
		labelsByKey[key] = label
	}
	total := 0.0
	for _, values := range valuesByKey {
		for _, value := range values {
			total += value
		}
	}
	return map[string]any{
		"timestamps": timestamps, "values_by_key": valuesByKey, "labels_by_key": labelsByKey,
		"total": total, "source_count": len(rows),
	}
}

func pluginStatisticsLabelKey(label string, used map[string]bool) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(label) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	key := strings.Trim(builder.String(), "_")
	if key == "" {
		key = "unknown"
	}
	candidate := key
	index := 2
	for used[candidate] {
		candidate = fmt.Sprintf("%s_%d", key, index)
		index++
	}
	used[candidate] = true
	return candidate
}

func pluginStatisticsPie(counter map[string]int64, limit int) []map[string]any {
	type item struct {
		label string
		value int64
	}
	items := make([]item, 0, len(counter))
	for label, value := range counter {
		items = append(items, item{label: label, value: value})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].value != items[j].value {
			return items[i].value > items[j].value
		}
		return items[i].label < items[j].label
	})
	result := make([]map[string]any, 0, limit+1)
	for index, item := range items {
		if index >= limit {
			break
		}
		if item.value > 0 {
			result = append(result, map[string]any{"name": item.label, "value": item.value})
		}
	}
	others := int64(0)
	for index, item := range items {
		if index >= limit && item.value > 0 {
			others += item.value
		}
	}
	if others > 0 {
		result = append(result, map[string]any{"name": "其他", "value": others})
	}
	return result
}

func pluginStatisticsAnyStrings(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func pluginStatisticsString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func pluginStatisticsFloat(value any) float64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int64:
		return float64(typed)
	case int32:
		return float64(typed)
	case int:
		return float64(typed)
	case []byte:
		var parsed float64
		fmt.Sscanf(string(typed), "%f", &parsed)
		return parsed
	case string:
		var parsed float64
		fmt.Sscanf(typed, "%f", &parsed)
		return parsed
	default:
		return 0
	}
}

func pluginStatisticsInt(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case []byte:
		var parsed int64
		fmt.Sscanf(string(typed), "%d", &parsed)
		return parsed
	case string:
		var parsed int64
		fmt.Sscanf(typed, "%d", &parsed)
		return parsed
	default:
		return 0
	}
}
