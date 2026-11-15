package stats

import (
	"context"
	"time"

	"MaiGo/internal/webui/schemas"
)

// 以下响应模型定义在 internal/webui/schemas（对齐 Python src/webui/schemas/statistics.py），
// 这里保留别名；本文件后半部分是服务层内部的查询投影类型，不属于对外契约。
type (
	DetailedStatisticsSummary       = schemas.DetailedStatisticsSummary
	DetailedStatisticsBreakdown     = schemas.DetailedStatisticsBreakdown
	DetailedChatStatistics          = schemas.DetailedChatStatistics
	DetailedDistributionItem        = schemas.DetailedDistributionItem
	DetailedStatisticsDistributions = schemas.DetailedStatisticsDistributions
	DetailedStatisticsPeriod        = schemas.DetailedStatisticsPeriod
	DetailedStatisticsTrendData     = schemas.DetailedStatisticsTrendData
	DetailedStatisticsMetricsData   = schemas.DetailedStatisticsMetricsData
	DetailedStatisticsData          = schemas.DetailedStatisticsData
)

// DetailedUsage 是 detailed 统计需要的 llm_usage 投影
type DetailedUsage struct {
	Timestamp             time.Time
	RequestType           string
	ProviderName          string
	ModelAssignName       string
	ModelName             string
	SessionID             string
	TaskName              string
	PromptTokens          int64
	CompletionTokens      int64
	PromptCacheEnabled    bool
	PromptCacheHitTokens  int64
	PromptCacheMissTokens int64
	Cost                  float64
	TimeCost              float64
}

// DetailedMessageChat 是按时段聚合后的聊天消息数
type DetailedMessageChat struct {
	ChatID          string
	ChatName        string
	MessageCount    int64
	LatestTimestamp time.Time
}

// DetailedMessage 是趋势图需要的消息投影
type DetailedMessage struct {
	StatisticsTime time.Time
	Platform       string
	UserID         string
	ChatID         string
	ChatName       string
	IsGroup        bool
}

// DetailedBotAccount 是 bot_platform_accounts 的账号投影
type DetailedBotAccount struct {
	Platform  string
	AccountID string
}

// DetailedProvider 抽象 detailed 统计所需的只读查询，便于无真实库测试
type DetailedProvider interface {
	EarliestStatisticsTime(ctx context.Context) (time.Time, bool, error)
	UsageSince(ctx context.Context, start time.Time) ([]DetailedUsage, error)
	MessageChatsSince(ctx context.Context, start time.Time) ([]DetailedMessageChat, error)
	ReplyCountSince(ctx context.Context, start time.Time, toolName string) (int64, error)
	OnlineIntervalsSince(ctx context.Context, start time.Time) ([]OnlineInterval, error)
	MessagesSince(ctx context.Context, start time.Time) ([]DetailedMessage, error)
	BotAccounts(ctx context.Context) ([]DetailedBotAccount, error)
}
