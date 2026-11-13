package schemas

// 详细统计的响应模型，对齐 MaiBot src/webui/schemas/statistics.py 的
// DetailedStatistics* 系列。数据由 services/statistics 构造，
// webui/routers 直接序列化返回，因此类型定义放在契约层。

// DetailedStatisticsSummary 对齐 DetailedStatisticsSummary
type DetailedStatisticsSummary struct {
	OnlineTime                         float64  `json:"online_time"`
	TotalMessages                      int64    `json:"total_messages"`
	TotalReplies                       int64    `json:"total_replies"`
	TotalRequests                      int64    `json:"total_requests"`
	TotalTokens                        int64    `json:"total_tokens"`
	InputTokens                        int64    `json:"input_tokens"`
	OutputTokens                       int64    `json:"output_tokens"`
	CacheHitTokens                     int64    `json:"cache_hit_tokens"`
	CacheMissTokens                    int64    `json:"cache_miss_tokens"`
	CacheHitRate                       *float64 `json:"cache_hit_rate"`
	TotalCost                          float64  `json:"total_cost"`
	CostPer100Messages                 float64  `json:"cost_per_100_messages"`
	CostPer100MessagesExcludingReplies float64  `json:"cost_per_100_messages_excluding_replies"`
	CostPer100Replies                  float64  `json:"cost_per_100_replies"`
	CostPerHour                        float64  `json:"cost_per_hour"`
	TokensPerHour                      float64  `json:"tokens_per_hour"`
}

// DetailedStatisticsBreakdown 对齐 DetailedStatisticsBreakdown
type DetailedStatisticsBreakdown struct {
	Name              string   `json:"name"`
	IsEmbedding       bool     `json:"is_embedding"`
	RequestCount      int64    `json:"request_count"`
	InputTokens       int64    `json:"input_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	TotalTokens       int64    `json:"total_tokens"`
	CacheHitTokens    int64    `json:"cache_hit_tokens"`
	CacheMissTokens   int64    `json:"cache_miss_tokens"`
	CacheHitRate      *float64 `json:"cache_hit_rate"`
	TotalCost         float64  `json:"total_cost"`
	AvgTimeCost       float64  `json:"avg_time_cost"`
	StdTimeCost       float64  `json:"std_time_cost"`
	AvgCallsPerReply  *float64 `json:"avg_calls_per_reply"`
	AvgTokensPerReply *float64 `json:"avg_tokens_per_reply"`
	AvgTokensPerCall  *float64 `json:"avg_tokens_per_call"`
}

// DetailedChatStatistics 对齐 DetailedChatStatistics
type DetailedChatStatistics struct {
	Name         string `json:"name"`
	MessageCount int64  `json:"message_count"`
}

// DetailedDistributionItem 对齐 DetailedDistributionItem
type DetailedDistributionItem struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// DetailedStatisticsDistributions 对齐 DetailedStatisticsDistributions
type DetailedStatisticsDistributions struct {
	OwnerTokens       []DetailedDistributionItem `json:"owner_tokens"`
	ModelTokens       []DetailedDistributionItem `json:"model_tokens"`
	ModuleTokens      []DetailedDistributionItem `json:"module_tokens"`
	RequestTypeTokens []DetailedDistributionItem `json:"request_type_tokens"`
	ChatTokens        []DetailedDistributionItem `json:"chat_tokens"`
	OwnerCosts        []DetailedDistributionItem `json:"owner_costs"`
	ModelCosts        []DetailedDistributionItem `json:"model_costs"`
	ModuleCosts       []DetailedDistributionItem `json:"module_costs"`
	RequestTypeCosts  []DetailedDistributionItem `json:"request_type_costs"`
	ChatMessages      []DetailedDistributionItem `json:"chat_messages"`
	ChatCosts         []DetailedDistributionItem `json:"chat_costs"`
}

// DetailedStatisticsPeriod 对齐 DetailedStatisticsPeriod
type DetailedStatisticsPeriod struct {
	Key           string                          `json:"key"`
	StartTime     string                          `json:"start_time"`
	EndTime       string                          `json:"end_time"`
	Summary       DetailedStatisticsSummary       `json:"summary"`
	Models        []DetailedStatisticsBreakdown   `json:"models"`
	Modules       []DetailedStatisticsBreakdown   `json:"modules"`
	TaskGroups    []DetailedStatisticsBreakdown   `json:"task_groups"`
	RequestTypes  []DetailedStatisticsBreakdown   `json:"request_types"`
	Chats         []DetailedChatStatistics        `json:"chats"`
	Distributions DetailedStatisticsDistributions `json:"distributions"`
}

// DetailedStatisticsTrendData 对齐 DetailedStatisticsTrendData
type DetailedStatisticsTrendData struct {
	TimeLabels    []string             `json:"time_labels"`
	TotalCostData []float64            `json:"total_cost_data"`
	CostByModel   map[string][]float64 `json:"cost_by_model"`
	CostByModule  map[string][]float64 `json:"cost_by_module"`
	MessageByChat map[string][]int64   `json:"message_by_chat"`
}

// DetailedStatisticsMetricsData 对齐 DetailedStatisticsMetricsData
type DetailedStatisticsMetricsData struct {
	TimeLabels         []string  `json:"time_labels"`
	CostPer100Messages []float64 `json:"cost_per_100_messages"`
	CostPerHour        []float64 `json:"cost_per_hour"`
	TokensPerHour      []float64 `json:"tokens_per_hour"`
	CostPer100Replies  []float64 `json:"cost_per_100_replies"`
}

// DetailedStatisticsData 对齐 DetailedStatisticsData
type DetailedStatisticsData struct {
	GeneratedAt string                                   `json:"generated_at"`
	Periods     []DetailedStatisticsPeriod               `json:"periods"`
	Trends      map[string]DetailedStatisticsTrendData   `json:"trends"`
	Metrics     map[string]DetailedStatisticsMetricsData `json:"metrics"`
}
