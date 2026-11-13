package bootstrap

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

type pluginStatisticsPort struct {
	db func() *gorm.DB
}

func newPluginStatisticsPort(db func() *gorm.DB) *pluginStatisticsPort {
	if db == nil {
		db = func() *gorm.DB { return nil }
	}
	return &pluginStatisticsPort{db: db}
}

func (p *pluginStatisticsPort) Invoke(ctx context.Context, capability string, args map[string]any) (any, error) {
	if p == nil || p.db == nil || p.db() == nil {
		return map[string]any{"success": false, "error": "统计数据库未初始化"}, nil
	}
	switch capability {
	case "statistics.local.models":
		return p.models(ctx, args)
	case "statistics.local.model_trend":
		return p.modelTrend(ctx, args)
	case "statistics.local.token_trend":
		return p.tokenTrend(ctx, args)
	case "statistics.local.token_distribution":
		return p.tokenDistribution(ctx, args)
	case "statistics.local.message_trend":
		return p.messageTrend(ctx, args)
	case "statistics.local.tool_trend":
		return p.toolTrend(ctx, args)
	case "statistics.local.online_time_trend":
		return p.onlineTimeTrend(ctx, args)
	default:
		return nil, fmt.Errorf("未注册的统计能力: %s", capability)
	}
}

func (p *pluginStatisticsPort) models(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	limit, err := pluginStatisticsLimit(args, "limit", 10)
	if err != nil {
		return nil, err
	}
	rows, err := p.raw(ctx, "SELECT COALESCE(NULLIF(model_name, ''), 'Unknown') AS model_name, SUM(request_count) AS request_count, SUM(cost) AS total_cost, SUM(total_tokens) AS total_tokens, SUM(time_cost_sum) / NULLIF(SUM(request_count), 0) AS avg_response_time FROM statistics_model_hourly WHERE bucket_time >= ? GROUP BY COALESCE(NULLIF(model_name, ''), 'Unknown') ORDER BY request_count DESC, model_name ASC LIMIT ?", p.start(days), limit)
	if err != nil {
		return nil, err
	}
	models := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		models = append(models, map[string]any{
			"model_name":        pluginStatisticsString(row["model_name"]),
			"request_count":     pluginStatisticsInt(row["request_count"]),
			"total_cost":        pluginStatisticsFloat(row["total_cost"]),
			"total_tokens":      pluginStatisticsInt(row["total_tokens"]),
			"avg_response_time": pluginStatisticsFloat(row["avg_response_time"]),
		})
	}
	return map[string]any{"success": true, "models": models}, nil
}

func (p *pluginStatisticsPort) modelTrend(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	bucket, err := pluginStatisticsBucket(args)
	if err != nil {
		return nil, err
	}
	topModels, err := pluginStatisticsLimit(args, "top_models", 10)
	if err != nil {
		return nil, err
	}
	metricSQL, valueColumn, topMetricSQL := pluginStatisticsModelMetric(pluginStringArg(args, "metric"))
	moduleName := strings.TrimSpace(pluginStringArg(args, "module_name"))
	where := "WHERE bucket_time >= ?"
	params := []any{p.start(days)}
	if moduleName != "" {
		where += " AND module_name = ?"
		params = append(params, moduleName)
	}
	topRows, err := p.raw(ctx, fmt.Sprintf("SELECT COALESCE(NULLIF(model_name, ''), 'Unknown') AS model_label, %s AS total_value FROM statistics_model_hourly %s GROUP BY COALESCE(NULLIF(model_name, ''), 'Unknown') ORDER BY total_value DESC, model_label ASC LIMIT ?", topMetricSQL, where), append(params, topModels)...)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(topRows))
	for _, row := range topRows {
		labels = append(labels, pluginStatisticsString(row["model_label"]))
	}
	if len(labels) == 0 {
		return map[string]any{"success": true, "series": pluginStatisticsSeries(nil, "model_label", valueColumn, nil)}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(labels)), ",")
	seriesRows, err := p.raw(ctx, fmt.Sprintf("SELECT %s AS bucket_label, COALESCE(NULLIF(model_name, ''), 'Unknown') AS model_label, %s AS %s FROM statistics_model_hourly %s AND COALESCE(NULLIF(model_name, ''), 'Unknown') IN (%s) GROUP BY bucket_label, model_label ORDER BY bucket_label ASC", p.bucketSQL("bucket_time", bucket), metricSQL, valueColumn, where, placeholders), append(params, pluginStatisticsAnyStrings(labels)...)...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "series": pluginStatisticsSeries(seriesRows, "model_label", valueColumn, labels)}, nil
}

func (p *pluginStatisticsPort) tokenTrend(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	bucket, err := pluginStatisticsBucket(args)
	if err != nil {
		return nil, err
	}
	bucketSQL := p.bucketSQL("bucket_time", bucket)
	start := p.start(days)
	groupBy := strings.TrimSpace(pluginStringArg(args, "group_by"))
	if groupBy == "" {
		rows, err := p.raw(ctx, fmt.Sprintf("SELECT %s AS bucket_label, SUM(prompt_tokens) AS prompt_tokens, SUM(completion_tokens) AS completion_tokens, SUM(total_tokens) AS total_tokens, SUM(request_count) AS request_count FROM statistics_model_hourly WHERE bucket_time >= ? GROUP BY bucket_label ORDER BY bucket_label ASC", bucketSQL), start)
		if err != nil {
			return nil, err
		}
		timestamps := make([]string, 0, len(rows))
		keys := []string{"total_tokens", "prompt_tokens", "completion_tokens", "request_count"}
		values := make(map[string][]float64, len(keys))
		for _, key := range keys {
			values[key] = make([]float64, 0, len(rows))
		}
		for _, row := range rows {
			timestamps = append(timestamps, pluginStatisticsString(row["bucket_label"]))
			for _, key := range keys {
				values[key] = append(values[key], pluginStatisticsFloat(row[key]))
			}
		}
		total := 0.0
		for _, value := range values["total_tokens"] {
			total += value
		}
		return map[string]any{"success": true, "series": map[string]any{
			"timestamps":    timestamps,
			"values_by_key": values,
			"labels_by_key": map[string]string{
				"total_tokens": "总 token", "prompt_tokens": "输入 token",
				"completion_tokens": "输出 token", "request_count": "请求次数",
			},
			"total":        total,
			"source_count": len(rows),
		}}, nil
	}
	topItems, err := pluginStatisticsLimit(args, "top_items", 10)
	if err != nil {
		return nil, err
	}
	groupColumn, _ := pluginStatisticsTokenGroup(groupBy)
	if groupColumn == "" {
		return nil, fmt.Errorf("group_by 只支持 model、module、provider 或 type")
	}
	topRows, err := p.raw(ctx, fmt.Sprintf("SELECT COALESCE(NULLIF(%s, ''), 'Unknown') AS group_label, SUM(total_tokens) AS total_tokens FROM statistics_model_hourly WHERE bucket_time >= ? GROUP BY COALESCE(NULLIF(%s, ''), 'Unknown') ORDER BY total_tokens DESC, group_label ASC LIMIT ?", groupColumn, groupColumn), start, topItems)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(topRows))
	for _, row := range topRows {
		labels = append(labels, pluginStatisticsString(row["group_label"]))
	}
	if len(labels) == 0 {
		return map[string]any{"success": true, "series": pluginStatisticsSeries(nil, "group_label", "total_tokens", nil)}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(labels)), ",")
	seriesRows, err := p.raw(ctx, fmt.Sprintf("SELECT %s AS bucket_label, COALESCE(NULLIF(%s, ''), 'Unknown') AS group_label, SUM(total_tokens) AS total_tokens FROM statistics_model_hourly WHERE bucket_time >= ? AND COALESCE(NULLIF(%s, ''), 'Unknown') IN (%s) GROUP BY bucket_label, group_label ORDER BY bucket_label ASC", bucketSQL, groupColumn, groupColumn, placeholders), append([]any{start}, pluginStatisticsAnyStrings(labels)...)...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "series": pluginStatisticsSeries(seriesRows, "group_label", "total_tokens", labels)}, nil
}

func (p *pluginStatisticsPort) tokenDistribution(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	topItems, err := pluginStatisticsLimit(args, "top_items", 10)
	if err != nil {
		return nil, err
	}
	groupBy := pluginStringArg(args, "group_by")
	if strings.TrimSpace(groupBy) == "" {
		groupBy = "model"
	}
	groupColumn, groupLabel := pluginStatisticsTokenGroup(groupBy)
	if groupColumn == "" {
		return nil, fmt.Errorf("group_by 只支持 model、module、provider 或 type")
	}
	rows, err := p.raw(ctx, fmt.Sprintf("SELECT COALESCE(NULLIF(%s, ''), 'Unknown') AS group_label, SUM(total_tokens) AS total_tokens, SUM(request_count) AS request_count FROM statistics_model_hourly WHERE bucket_time >= ? GROUP BY COALESCE(NULLIF(%s, ''), 'Unknown') ORDER BY total_tokens DESC, group_label ASC", groupColumn, groupColumn), p.start(days))
	if err != nil {
		return nil, err
	}
	tokenCounts := map[string]int64{}
	requestCounts := map[string]int64{}
	var total int64
	for _, row := range rows {
		label := pluginStatisticsString(row["group_label"])
		tokenCounts[label] = pluginStatisticsInt(row["total_tokens"])
		requestCounts[label] = pluginStatisticsInt(row["request_count"])
		total += tokenCounts[label]
	}
	return map[string]any{"success": true, "distribution": map[string]any{
		"pies": []map[string]any{
			{"title": "Token 按" + groupLabel + "分布", "data": pluginStatisticsPie(tokenCounts, topItems)},
			{"title": "请求次数按" + groupLabel + "分布", "data": pluginStatisticsPie(requestCounts, topItems)},
		},
		"total": total, "source_count": len(rows),
	}}, nil
}

func (p *pluginStatisticsPort) messageTrend(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	bucket, err := pluginStatisticsBucket(args)
	if err != nil {
		return nil, err
	}
	topChats, err := pluginStatisticsLimit(args, "top_chats", 10)
	if err != nil {
		return nil, err
	}
	start := p.start(days)
	topRows, err := p.raw(ctx, "SELECT chat_id, COALESCE(NULLIF(chat_name, ''), chat_id, 'Unknown') AS chat_label, SUM(message_count) AS total_count FROM statistics_message_hourly WHERE bucket_time >= ? GROUP BY chat_id, chat_label ORDER BY total_count DESC, chat_label ASC LIMIT ?", start, topChats)
	if err != nil {
		return nil, err
	}
	chatIDs := make([]string, 0, len(topRows))
	labels := make([]string, 0, len(topRows))
	for _, row := range topRows {
		chatIDs = append(chatIDs, pluginStatisticsString(row["chat_id"]))
		labels = append(labels, pluginStatisticsString(row["chat_label"]))
	}
	if len(chatIDs) == 0 {
		return map[string]any{"success": true, "series": pluginStatisticsSeries(nil, "chat_label", "total_count", nil)}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chatIDs)), ",")
	seriesRows, err := p.raw(ctx, fmt.Sprintf("SELECT %s AS bucket_label, chat_id, COALESCE(NULLIF(chat_name, ''), chat_id, 'Unknown') AS chat_label, SUM(message_count) AS total_count FROM statistics_message_hourly WHERE bucket_time >= ? AND chat_id IN (%s) GROUP BY bucket_label, chat_id, chat_label ORDER BY bucket_label ASC", p.bucketSQL("bucket_time", bucket), placeholders), append([]any{start}, pluginStatisticsAnyStrings(chatIDs)...)...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "series": pluginStatisticsSeries(seriesRows, "chat_label", "total_count", labels)}, nil
}

func (p *pluginStatisticsPort) toolTrend(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	bucket, err := pluginStatisticsBucket(args)
	if err != nil {
		return nil, err
	}
	topTools, err := pluginStatisticsLimit(args, "top_tools", 10)
	if err != nil {
		return nil, err
	}
	start := p.start(days)
	topRows, err := p.raw(ctx, "SELECT COALESCE(NULLIF(tool_name, ''), 'Unknown') AS tool_label, SUM(call_count) AS total_count FROM statistics_tool_hourly WHERE bucket_time >= ? GROUP BY COALESCE(NULLIF(tool_name, ''), 'Unknown') ORDER BY total_count DESC, tool_label ASC LIMIT ?", start, topTools)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(topRows))
	for _, row := range topRows {
		labels = append(labels, pluginStatisticsString(row["tool_label"]))
	}
	if len(labels) == 0 {
		return map[string]any{"success": true, "series": pluginStatisticsSeries(nil, "tool_label", "total_count", nil)}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(labels)), ",")
	seriesRows, err := p.raw(ctx, fmt.Sprintf("SELECT %s AS bucket_label, COALESCE(NULLIF(tool_name, ''), 'Unknown') AS tool_label, SUM(call_count) AS total_count FROM statistics_tool_hourly WHERE bucket_time >= ? AND COALESCE(NULLIF(tool_name, ''), 'Unknown') IN (%s) GROUP BY bucket_label, tool_label ORDER BY bucket_label ASC", p.bucketSQL("bucket_time", bucket), placeholders), append([]any{start}, pluginStatisticsAnyStrings(labels)...)...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "series": pluginStatisticsSeries(seriesRows, "tool_label", "total_count", labels)}, nil
}

func (p *pluginStatisticsPort) onlineTimeTrend(ctx context.Context, args map[string]any) (any, error) {
	days, err := pluginStatisticsDays(args)
	if err != nil {
		return nil, err
	}
	bucket, err := pluginStatisticsBucket(args)
	if err != nil {
		return nil, err
	}
	rows, err := p.raw(ctx, fmt.Sprintf("SELECT %s AS bucket_label, SUM(duration_minutes) / 60.0 AS online_hours FROM online_time WHERE COALESCE(start_timestamp, timestamp) >= ? GROUP BY bucket_label ORDER BY bucket_label ASC", p.bucketSQL("COALESCE(start_timestamp, timestamp)", bucket)), p.start(days))
	if err != nil {
		return nil, err
	}
	timestamps := make([]string, 0, len(rows))
	values := make([]float64, 0, len(rows))
	total := 0.0
	for _, row := range rows {
		timestamps = append(timestamps, pluginStatisticsString(row["bucket_label"]))
		hours := pluginStatisticsFloat(row["online_hours"])
		values = append(values, hours)
		total += hours
	}
	return map[string]any{"success": true, "series": map[string]any{
		"timestamps":    timestamps,
		"values_by_key": map[string][]float64{"online_hours": values},
		"labels_by_key": map[string]string{"online_hours": "在线时长(小时)"},
		"total":         total,
		"source_count":  len(rows),
	}}, nil
}

// dialect 返回插件统计端口使用的时间函数方言。
	// 产品库已统一 SQLite；非 sqlite 驱动名也按 sqlite 处理，避免误走 PG 方言 SQL。
	func (p *pluginStatisticsPort) dialect() string {
		return "sqlite"
	}

func (p *pluginStatisticsPort) start(days int) any {
	return pluginStatisticsStart(p.dialect(), days)
}

func (p *pluginStatisticsPort) bucketSQL(column, bucket string) string {
	return pluginStatisticsBucketSQL(p.dialect(), column, bucket)
}

func (p *pluginStatisticsPort) raw(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	db := p.db()
	if db == nil {
		return nil, fmt.Errorf("统计数据库未初始化")
	}
	rows, err := db.WithContext(ctx).Raw(sql, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for index := range values {
			ptrs[index] = &values[index]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			value := values[index]
			if bytes, ok := value.([]byte); ok {
				value = string(bytes)
			}
			row[column] = value
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func pluginStatisticsIntArg(args map[string]any, key string, fallback int) (int, error) {
	if args == nil {
		return fallback, nil
	}
	raw, exists := args[key]
	if !exists || raw == nil {
		return fallback, nil
	}
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case float64:
		return int(value), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, fmt.Errorf("%s 必须为正整数", key)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s 必须为正整数", key)
	}
}
