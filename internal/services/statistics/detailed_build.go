package stats

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

const detailedReplyToolName = "reply"

var detailedPeriodDefinitions = []struct {
	key      string
	duration time.Duration
}{
	{"all_time", 0},
	{"last_30_days", 30 * 24 * time.Hour},
	{"last_7_days", 7 * 24 * time.Hour},
	{"last_3_days", 3 * 24 * time.Hour},
	{"last_24_hours", 24 * time.Hour},
	{"last_3_hours", 3 * time.Hour},
	{"last_hour", time.Hour},
	{"last_15_minutes", 15 * time.Minute},
}

type detailedClassKey struct {
	dimension string
	kind      string
	name      string
}

type detailedClassTotals struct {
	requestCount    int64
	inputTokens     int64
	outputTokens    int64
	cacheHitTokens  int64
	cacheMissTokens int64
	totalCost       float64
	durationCount   int64
	durationSum     float64
	durationSumSq   float64
}

type detailedPeriodData struct {
	totalRequests   int64
	totalCost       float64
	cacheHitTokens  int64
	cacheMissTokens int64
	inputTokens     int64
	outputTokens    int64
	tokensByChat    map[string]int64
	costByChat      map[string]float64
	totalMessages   int64
	totalReplies    int64
	onlineSeconds   float64
	messagesByChat  map[string]int64
	classified      map[detailedClassKey]*detailedClassTotals
}

type detailedChatName struct {
	name      string
	timestamp time.Time
}

func newDetailedPeriodData() *detailedPeriodData {
	return &detailedPeriodData{
		tokensByChat:   map[string]int64{},
		costByChat:     map[string]float64{},
		messagesByChat: map[string]int64{},
		classified:     map[detailedClassKey]*detailedClassTotals{},
	}
}

// EmptyDetailedStatistics 返回可直接序列化的空详细统计快照
func EmptyDetailedStatistics(now time.Time) DetailedStatisticsData {
	return emptyDetailedStatistics(now)
}

func newDetailedStatisticsData(now time.Time) DetailedStatisticsData {
	return DetailedStatisticsData{
		GeneratedAt: formatDetailedTime(now),
		Periods:     make([]DetailedStatisticsPeriod, 0, len(detailedPeriodDefinitions)),
		Trends:      map[string]DetailedStatisticsTrendData{},
		Metrics:     map[string]DetailedStatisticsMetricsData{},
	}
}

func emptyDetailedStatistics(now time.Time) DetailedStatisticsData {
	now = now.In(time.Local)
	snapshot := newDetailedStatisticsData(now)
	allStart := time.Date(2000, 1, 1, 0, 0, 0, 0, time.Local)
	for _, definition := range detailedPeriodDefinitions {
		start := now
		if definition.key == "all_time" {
			start = allStart
		} else if definition.duration > 0 {
			start = now.Add(-definition.duration)
		}
		snapshot.Periods = append(snapshot.Periods, buildDetailedPeriod(definition.key, start, now, newDetailedPeriodData(), map[string]detailedChatName{}))
	}
	return snapshot
}

// BuildDetailedStatistics 按 Python StatisticOutputTask 的快照语义构建详细统计
func BuildDetailedStatistics(ctx context.Context, provider DetailedProvider, now time.Time) (DetailedStatisticsData, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if provider == nil {
		return emptyDetailedStatistics(now), fmt.Errorf("详细统计查询器未初始化")
	}
	allStart := time.Date(2000, 1, 1, 0, 0, 0, 0, time.Local)
	if earliest, ok, err := provider.EarliestStatisticsTime(ctx); err != nil {
		return emptyDetailedStatistics(now), fmt.Errorf("查询统计起始时间失败: %w", err)
	} else if ok && earliest.Before(now) {
		allStart = earliest.In(time.Local)
	}

	starts := make([]time.Time, len(detailedPeriodDefinitions))
	for i, definition := range detailedPeriodDefinitions {
		if definition.key == "all_time" {
			starts[i] = allStart
		} else {
			starts[i] = now.Add(-definition.duration)
		}
	}
	periods := make([]*detailedPeriodData, len(detailedPeriodDefinitions))
	for i := range periods {
		periods[i] = newDetailedPeriodData()
	}

	usage, err := provider.UsageSince(ctx, allStart)
	if err != nil {
		return emptyDetailedStatistics(now), fmt.Errorf("查询模型用量失败: %w", err)
	}
	accumulateDetailedUsage(usage, starts, periods)

	nameMapping := map[string]detailedChatName{}
	order := make([]int, len(detailedPeriodDefinitions))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return starts[order[i]].After(starts[order[j]]) })
	for _, index := range order {
		chats, err := provider.MessageChatsSince(ctx, starts[index])
		if err != nil {
			return emptyDetailedStatistics(now), fmt.Errorf("查询聊天消息统计失败: %w", err)
		}
		for _, chat := range chats {
			updateDetailedChatName(nameMapping, chat)
			periods[index].totalMessages += chat.MessageCount
			periods[index].messagesByChat[chat.ChatID] += chat.MessageCount
		}
		replies, err := provider.ReplyCountSince(ctx, starts[index], detailedReplyToolName)
		if err != nil {
			zap.L().Warn("统计 reply 工具次数失败，将回复数视为 0", zap.Error(err))
			replies = 0
		}
		periods[index].totalReplies = replies
	}

	intervals, err := provider.OnlineIntervalsSince(ctx, allStart)
	if err != nil {
		return emptyDetailedStatistics(now), fmt.Errorf("查询在线区间失败: %w", err)
	}
	for i := range periods {
		periods[i].onlineSeconds = detailedOnlineSeconds(intervals, starts[i], now)
	}

	trendStart := now.Add(-30 * 24 * time.Hour)
	messages, err := provider.MessagesSince(ctx, trendStart)
	if err != nil {
		return emptyDetailedStatistics(now), fmt.Errorf("查询消息趋势失败: %w", err)
	}
	accounts, err := provider.BotAccounts(ctx)
	if err != nil {
		zap.L().Warn("查询机器人账号失败，趋势回复数按 0 计算", zap.Error(err))
		accounts = nil
	}

	snapshot := newDetailedStatisticsData(now)
	for i, definition := range detailedPeriodDefinitions {
		snapshot.Periods = append(snapshot.Periods, buildDetailedPeriod(definition.key, starts[i], now, periods[i], nameMapping))
	}
	snapshot.Trends, snapshot.Metrics = buildDetailedTimeSeries(now, usage, messages, intervals, accounts)
	return snapshot, nil
}

// BuildDetailedStatisticsForDB 按数据库方言构建详细统计
func BuildDetailedStatisticsForDB(ctx context.Context, db *gorm.DB, now time.Time) (DetailedStatisticsData, error) {
	if db == nil {
		return emptyDetailedStatistics(now), fmt.Errorf("统计数据库未初始化")
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return emptyDetailedStatistics(now), fmt.Errorf("初始化统计表失败: %w", err)
	}
	return BuildDetailedStatistics(ctx, NewDetailedProvider(db), now)
}

func accumulateDetailedUsage(usage []DetailedUsage, starts []time.Time, periods []*detailedPeriodData) {
	for _, record := range usage {
		requestType := firstNonEmpty(record.RequestType, "unknown")
		providerName := firstNonEmpty(record.ProviderName, "unknown")
		modelName := firstNonEmpty(record.ModelAssignName, record.ModelName, "unknown")
		moduleName := requestType
		if index := strings.Index(requestType, "."); index >= 0 {
			moduleName = requestType[:index]
		}
		taskName := firstNonEmpty(record.TaskName, "未记录")
		sessionID := strings.TrimSpace(record.SessionID)
		chatKey := sessionID
		if chatKey == "" {
			chatKey = "__global__"
		}
		promptTokens := record.PromptTokens
		completionTokens := record.CompletionTokens
		totalTokens := promptTokens + completionTokens
		hitTokens, missTokens := int64(0), int64(0)
		if record.PromptCacheEnabled {
			hitTokens, missTokens = normalizeCacheTokens(promptTokens, true, record.PromptCacheHitTokens, record.PromptCacheMissTokens)
		}
		kind := "generation"
		if taskName == "embedding" || taskName == "image_embedding" {
			kind = "embedding"
		}
		names := []struct {
			dimension string
			name      string
		}{
			{"type", requestType},
			{"user", providerName},
			{"model", modelName},
			{"module", moduleName},
			{"task", taskName},
			{"request_type", requestType},
		}
		timestamp := record.Timestamp.In(time.Local)
		for i, start := range starts {
			if timestamp.Before(start) {
				continue
			}
			period := periods[i]
			period.totalRequests++
			period.totalCost += record.Cost
			period.cacheHitTokens += hitTokens
			period.cacheMissTokens += missTokens
			period.inputTokens += promptTokens
			period.outputTokens += completionTokens
			period.tokensByChat[chatKey] += totalTokens
			period.costByChat[chatKey] += record.Cost
			for _, item := range names {
				totals := period.classified[detailedClassKey{item.dimension, kind, item.name}]
				if totals == nil {
					totals = &detailedClassTotals{}
					period.classified[detailedClassKey{item.dimension, kind, item.name}] = totals
				}
				totals.requestCount++
				totals.inputTokens += promptTokens
				totals.outputTokens += completionTokens
				totals.cacheHitTokens += hitTokens
				totals.cacheMissTokens += missTokens
				totals.totalCost += record.Cost
				if record.TimeCost > 0 {
					totals.durationCount++
					totals.durationSum += record.TimeCost
					totals.durationSumSq += record.TimeCost * record.TimeCost
				}
			}
		}
	}
}

func buildDetailedPeriod(key string, start, now time.Time, data *detailedPeriodData, names map[string]detailedChatName) DetailedStatisticsPeriod {
	totalTokens := data.inputTokens + data.outputTokens
	onlineHours := data.onlineSeconds / 3600
	receivedMessages := data.totalMessages - data.totalReplies
	summary := DetailedStatisticsSummary{
		OnlineTime:         data.onlineSeconds,
		TotalMessages:      data.totalMessages,
		TotalReplies:       data.totalReplies,
		TotalRequests:      data.totalRequests,
		TotalTokens:        totalTokens,
		InputTokens:        data.inputTokens,
		OutputTokens:       data.outputTokens,
		CacheHitTokens:     data.cacheHitTokens,
		CacheMissTokens:    data.cacheMissTokens,
		CacheHitRate:       calculateCacheHitRate(data.cacheHitTokens, data.cacheMissTokens),
		TotalCost:          data.totalCost,
		CostPer100Messages: ratioOrZero(data.totalCost, float64(data.totalMessages)) * 100,
		CostPer100Replies:  ratioOrZero(data.totalCost, float64(data.totalReplies)) * 100,
		CostPerHour:        ratioOrZero(data.totalCost, onlineHours),
		TokensPerHour:      ratioOrZero(float64(totalTokens), onlineHours),
	}
	if receivedMessages > 0 {
		summary.CostPer100MessagesExcludingReplies = data.totalCost / float64(receivedMessages) * 100
	}

	chats := make([]DetailedChatStatistics, 0, len(data.messagesByChat))
	chatIDs := make([]string, 0, len(data.messagesByChat))
	for chatID := range data.messagesByChat {
		chatIDs = append(chatIDs, chatID)
	}
	sort.Strings(chatIDs)
	for _, chatID := range chatIDs {
		chats = append(chats, DetailedChatStatistics{Name: detailedChatNameFor(names, chatID), MessageCount: data.messagesByChat[chatID]})
	}

	return DetailedStatisticsPeriod{
		Key:           key,
		StartTime:     formatDetailedTime(start),
		EndTime:       formatDetailedTime(now),
		Summary:       summary,
		Models:        buildDetailedBreakdowns(data, "model"),
		Modules:       buildDetailedBreakdowns(data, "module"),
		TaskGroups:    buildDetailedBreakdowns(data, "task"),
		RequestTypes:  buildDetailedBreakdowns(data, "request_type"),
		Chats:         chats,
		Distributions: buildDetailedDistributions(data, names),
	}
}

func buildDetailedBreakdowns(data *detailedPeriodData, dimension string) []DetailedStatisticsBreakdown {
	rows := make([]DetailedStatisticsBreakdown, 0)
	for key, totals := range data.classified {
		if key.dimension != dimension {
			continue
		}
		totalTokens := totals.inputTokens + totals.outputTokens
		avgTime := 0.0
		variance := 0.0
		if totals.durationCount > 0 {
			avgTime = totals.durationSum / float64(totals.durationCount)
			variance = math.Max(totals.durationSumSq/float64(totals.durationCount)-avgTime*avgTime, 0)
		}
		row := DetailedStatisticsBreakdown{
			Name:            key.name,
			IsEmbedding:     key.kind == "embedding",
			RequestCount:    totals.requestCount,
			InputTokens:     totals.inputTokens,
			OutputTokens:    totals.outputTokens,
			TotalTokens:     totalTokens,
			CacheHitTokens:  totals.cacheHitTokens,
			CacheMissTokens: totals.cacheMissTokens,
			CacheHitRate:    calculateCacheHitRate(totals.cacheHitTokens, totals.cacheMissTokens),
			TotalCost:       totals.totalCost,
			AvgTimeCost:     roundDetailed(avgTime),
			StdTimeCost:     roundDetailed(math.Sqrt(variance)),
		}
		if data.totalReplies > 0 {
			value := float64(totals.requestCount) / float64(data.totalReplies)
			row.AvgCallsPerReply = &value
			tokensPerReply := float64(totalTokens) / float64(data.totalReplies)
			row.AvgTokensPerReply = &tokensPerReply
		}
		if totals.requestCount > 0 {
			value := float64(totalTokens) / float64(totals.requestCount)
			row.AvgTokensPerCall = &value
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].IsEmbedding != rows[j].IsEmbedding {
			return !rows[i].IsEmbedding
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func buildDetailedDistributions(data *detailedPeriodData, names map[string]detailedChatName) DetailedStatisticsDistributions {
	modelTokens, modelCosts := detailedClassDistributions(data, "model")
	moduleTokens, moduleCosts := detailedClassDistributions(data, "module")
	typeTokens, typeCosts := detailedClassDistributions(data, "type")

	chatMessages := map[string]float64{}
	for chatID, count := range data.messagesByChat {
		name := detailedChatNameFor(names, chatID)
		chatMessages[name] += float64(count)
	}
	chatTokens := map[string]float64{}
	for chatID, tokens := range data.tokensByChat {
		name := detailedChatDistributionName(names, chatID)
		chatTokens[name] += float64(tokens)
	}
	chatCosts := map[string]float64{}
	for chatID, cost := range data.costByChat {
		name := detailedChatDistributionName(names, chatID)
		chatCosts[name] += cost
	}

	return DetailedStatisticsDistributions{
		OwnerTokens:       detailedDistributionItems(detailedOwnerDistribution(typeTokens)),
		ModelTokens:       detailedDistributionItems(modelTokens),
		ModuleTokens:      detailedDistributionItems(moduleTokens),
		RequestTypeTokens: detailedDistributionItems(typeTokens),
		ChatTokens:        detailedDistributionItems(chatTokens),
		OwnerCosts:        detailedDistributionItems(detailedOwnerDistribution(typeCosts)),
		ModelCosts:        detailedDistributionItems(modelCosts),
		ModuleCosts:       detailedDistributionItems(moduleCosts),
		RequestTypeCosts:  detailedDistributionItems(typeCosts),
		ChatMessages:      detailedDistributionItems(chatMessages),
		ChatCosts:         detailedDistributionItems(chatCosts),
	}
}

func detailedClassDistributions(data *detailedPeriodData, dimension string) (map[string]float64, map[string]float64) {
	tokens := map[string]float64{}
	costs := map[string]float64{}
	for key, totals := range data.classified {
		if key.dimension != dimension {
			continue
		}
		tokens[key.name] += float64(totals.inputTokens + totals.outputTokens)
		costs[key.name] += totals.totalCost
	}
	return tokens, costs
}

func detailedOwnerDistribution(values map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for requestType, value := range values {
		normalized := strings.TrimSpace(requestType)
		label := "本体"
		if strings.HasPrefix(normalized, "plugin.") {
			pluginID := strings.TrimSpace(strings.TrimPrefix(normalized, "plugin."))
			pluginID = strings.TrimSpace(strings.TrimSuffix(pluginID, ".asr"))
			if pluginID == "" {
				label = "插件（未知）"
			} else {
				label = "插件 " + pluginID
			}
		}
		out[label] += value
	}
	for label, value := range out {
		if value <= 0 {
			delete(out, label)
		}
	}
	return out
}

func detailedDistributionItems(values map[string]float64) []DetailedDistributionItem {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]DetailedDistributionItem, 0, len(names))
	for _, name := range names {
		items = append(items, DetailedDistributionItem{Name: name, Value: values[name]})
	}
	return items
}

func updateDetailedChatName(mapping map[string]detailedChatName, chat DetailedMessageChat) {
	if chat.LatestTimestamp.IsZero() {
		chat.LatestTimestamp = time.Now()
	}
	current, ok := mapping[chat.ChatID]
	if !ok || (chat.ChatName != current.name && chat.LatestTimestamp.After(current.timestamp)) {
		mapping[chat.ChatID] = detailedChatName{name: chat.ChatName, timestamp: chat.LatestTimestamp}
	}
}

func detailedChatNameFor(names map[string]detailedChatName, chatID string) string {
	if item, ok := names[chatID]; ok && strings.TrimSpace(item.name) != "" {
		return item.name
	}
	return "未知聊天"
}

func detailedChatDistributionName(names map[string]detailedChatName, chatID string) string {
	if chatID == "__global__" {
		return "全局"
	}
	if item, ok := names[chatID]; ok && strings.TrimSpace(item.name) != "" {
		return item.name
	}
	if strings.HasPrefix(chatID, "g") {
		return "群聊" + strings.TrimPrefix(chatID, "g")
	}
	if strings.HasPrefix(chatID, "u") {
		return "用户" + strings.TrimPrefix(chatID, "u")
	}
	return chatID
}

func detailedOnlineSeconds(intervals []OnlineInterval, start, now time.Time) float64 {
	var total float64
	for _, interval := range intervals {
		overlapStart := interval.Start
		if overlapStart.Before(start) {
			overlapStart = start
		}
		overlapEnd := interval.End
		if overlapEnd.After(now) {
			overlapEnd = now
		}
		if overlapEnd.After(overlapStart) {
			total += overlapEnd.Sub(overlapStart).Seconds()
		}
	}
	return total
}

func ratioOrZero(numerator, denominator float64) float64 {
	if denominator > 0 {
		return numerator / denominator
	}
	return 0
}

func roundDetailed(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func formatDetailedTime(value time.Time) string {
	value = value.In(time.Local)
	if value.Nanosecond()/1000 == 0 {
		return value.Format("2006-01-02T15:04:05")
	}
	return value.Format("2006-01-02T15:04:05.000000")
}
