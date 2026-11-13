// Package schemas 是 WebUI 对外响应的数据契约。
//
// 对齐 MaiBot src/webui/schemas/：统计与新闻的响应模型由 services 层构造，
// webui/routers 直接返回。
package schemas

// StatisticsSummary 对齐 webui/schemas/statistics.py StatisticsSummary
type StatisticsSummary struct {
	TotalRequests       int64    `json:"total_requests"`
	TotalCost           float64  `json:"total_cost"`
	TotalTokens         int64    `json:"total_tokens"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CacheHitTokens      int64    `json:"cache_hit_tokens"`
	CacheMissTokens     int64    `json:"cache_miss_tokens"`
	CacheHitRate        *float64 `json:"cache_hit_rate"`
	ChatCacheHitTokens  int64    `json:"chat_cache_hit_tokens"`
	ChatCacheMissTokens int64    `json:"chat_cache_miss_tokens"`
	ChatCacheHitRate    *float64 `json:"chat_cache_hit_rate"`
	OnlineTime          float64  `json:"online_time"`
	TotalMessages       int64    `json:"total_messages"`
	TotalReplies        int64    `json:"total_replies"`
	AvgResponseTime     float64  `json:"avg_response_time"`
	CostPerHour         float64  `json:"cost_per_hour"`
	TokensPerHour       float64  `json:"tokens_per_hour"`
}

// ModelStatistics 对齐 webui/schemas/statistics.py ModelStatistics
type ModelStatistics struct {
	ModelName       string   `json:"model_name"`
	RequestCount    int64    `json:"request_count"`
	TotalCost       float64  `json:"total_cost"`
	TotalTokens     int64    `json:"total_tokens"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CacheHitTokens  int64    `json:"cache_hit_tokens"`
	CacheMissTokens int64    `json:"cache_miss_tokens"`
	CacheHitRate    *float64 `json:"cache_hit_rate"`
	AvgResponseTime float64  `json:"avg_response_time"`
}

// RecentActivity 最近一次 LLM 调用的展示数据
type RecentActivity map[string]any

// TimeSeriesData 对齐 webui/schemas/statistics.py TimeSeriesData
type TimeSeriesData struct {
	Timestamp       string  `json:"timestamp"`
	OnlineSeconds   float64 `json:"online_seconds"`
	Requests        int64   `json:"requests"`
	Cost            float64 `json:"cost"`
	Tokens          int64   `json:"tokens"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheHitTokens  int64   `json:"cache_hit_tokens"`
	CacheMissTokens int64   `json:"cache_miss_tokens"`
}

// DashboardData 对齐 webui/schemas/statistics.py DashboardData
type DashboardData struct {
	Summary        StatisticsSummary `json:"summary"`
	ModelStats     []ModelStatistics `json:"model_stats"`
	HourlyData     []TimeSeriesData  `json:"hourly_data"`
	DailyData      []TimeSeriesData  `json:"daily_data"`
	RecentActivity []RecentActivity  `json:"recent_activity"`
}
