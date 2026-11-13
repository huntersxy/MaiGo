package stats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/chat/session"
)

// 遥测统计载荷对齐 MaiBot src/services/telemetry_stats_service.py

const (
	// statsChatTypePrivate / statsChatTypeGroup 与 MaiBot chat_type 取值一致
	statsChatTypePrivate = "private"
	statsChatTypeGroup   = "group"

	// statsDirectionReceived / statsDirectionBotSent 与 MaiBot messages.by_direction 键一致
	statsDirectionReceived = "received"
	statsDirectionBotSent  = "bot_sent"

	// statsPrecisionDaily 旧库只有按日累计在线时长，按重叠比例折算
	statsPrecisionDaily = "daily_prorated"
	// statsPrecisionHeartbeat 在线区间来自 OnlineTimeRecordTask 的分钟心跳
	statsPrecisionHeartbeat = "minute_heartbeat"
	// statsPrecisionNone 无数据库时无法统计在线时长
	statsPrecisionNone = "unavailable"

	// statsUnknownLabel 未知平台/任务/模型的占位标签
	statsUnknownLabel = "unknown"

	statsSecondsPerDay = 24 * 60 * 60
)

// telemetryFrequencyBuckets 对齐 MaiBot FREQUENCY_BUCKETS 顺序
var telemetryFrequencyBuckets = []string{"0", "0.05", "0.1", "0.25", "0.5", "0.75", "1", "unknown"}

const (
	directionTotalsQuery = `SELECT direction, COALESCE(SUM(message_count), 0) AS message_count
FROM message_traffic_hourly
WHERE bucket_start >= ? AND bucket_start < ?
GROUP BY direction`

	chatTypeTotalsQuery = `SELECT direction, conversation_key, COALESCE(SUM(message_count), 0) AS message_count
FROM message_traffic_chat_hourly
WHERE bucket_start >= ? AND bucket_start < ?
GROUP BY direction, conversation_key`

	onlineDaysQuery = `SELECT CAST(day_date AS TEXT) AS day_date, online_seconds
FROM online_time_daily
WHERE day_date >= ? AND day_date <= ?
ORDER BY day_date`

	llmUsageQuery = `SELECT COALESCE(NULLIF(task_name, ''), request_type, 'unknown') AS task,
COALESCE(NULLIF(model_assign_name, ''), NULLIF(model_name, ''), 'unknown') AS model,
COUNT(*) AS request_count,
COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
COALESCE(SUM(total_tokens), 0) AS total_tokens
FROM llm_usage
WHERE timestamp >= ? AND timestamp < ?
GROUP BY 1, 2`
)

type telemetryStatsPayload struct {
	SchemaVersion       int                                 `json:"schema_version"`
	UploadID            string                              `json:"upload_id"`
	Client              telemetryStatsClient                `json:"client"`
	Period              telemetryStatsPeriod                `json:"period"`
	Runtime             telemetryStatsRuntime               `json:"runtime"`
	Messages            telemetryStatsMessages              `json:"messages"`
	Adapters            []AdapterSnapshot                   `json:"adapters"`
	ModelTaskAssignment map[string]telemetryStatsAssignment `json:"model_task_assignment"`
	LLMUsage            map[string]telemetryStatsTaskUsage  `json:"llm_usage"`
}

type telemetryStatsClient struct {
	OSType     string `json:"os_type"`
	MMCVersion string `json:"mmc_version"`
}

type telemetryStatsPeriod struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	Seconds   int64  `json:"seconds"`
	Truncated bool   `json:"truncated"`
}

type telemetryStatsRuntime struct {
	OnlineSeconds int64   `json:"online_seconds"`
	CoverageRatio float64 `json:"coverage_ratio"`
	Precision     string  `json:"precision"`
}

type telemetryStatsMessages struct {
	ByDirection map[string]*telemetryStatsDirection `json:"by_direction"`
}

type telemetryStatsDirection struct {
	Total      int64                              `json:"total"`
	ByPlatform map[string]*telemetryStatsPlatform `json:"by_platform"`
}

type telemetryStatsPlatform struct {
	Total            int64                       `json:"total"`
	Private          int64                       `json:"private"`
	Group            int64                       `json:"group"`
	ByReplyFrequency map[string]map[string]int64 `json:"by_reply_frequency"`
}

type telemetryStatsAssignment struct {
	Models []string `json:"models"`
}

type telemetryStatsTaskUsage struct {
	Total  telemetryStatsModelUsage            `json:"total"`
	Models map[string]telemetryStatsModelUsage `json:"models"`
}

type telemetryStatsModelUsage struct {
	RequestCount     int64 `json:"request_count"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type directionCountRow struct {
	Direction    string
	MessageCount int64
}

type chatTypeCountRow struct {
	Direction       string
	ConversationKey string
	MessageCount    int64
}

type onlineDayRow struct {
	DayDate       string
	OnlineSeconds int64
}

type llmUsageRow struct {
	Task             string
	Model            string
	RequestCount     int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// buildStatsPayload 组装一次上传载荷
func (u *statsUploader) buildStatsPayload(ctx context.Context, periodStart, periodEnd time.Time, truncated bool) (telemetryStatsPayload, error) {
	uuid := u.tele.currentClientUUID()
	periodSeconds := int64(periodEnd.Sub(periodStart) / time.Second)
	if periodSeconds < 0 {
		periodSeconds = 0
	}
	runtimeStats, err := u.collectRuntime(ctx, periodStart, periodEnd, periodSeconds)
	if err != nil {
		return telemetryStatsPayload{}, err
	}
	messages, err := u.collectMessages(ctx, periodStart, periodEnd)
	if err != nil {
		return telemetryStatsPayload{}, err
	}
	usage, err := u.collectLLMUsage(ctx, periodStart, periodEnd)
	if err != nil {
		return telemetryStatsPayload{}, err
	}
	adapters := u.tele.adapters
	if adapters == nil {
		adapters = []AdapterSnapshot{}
	}
	return telemetryStatsPayload{
		SchemaVersion: statsSchemaVersion,
		UploadID:      statsUploadID(uuid, periodStart, periodEnd),
		Client: telemetryStatsClient{
			OSType:     osTypeLabel(),
			MMCVersion: clientVersionLabel(u.tele.version),
		},
		Period: telemetryStatsPeriod{
			Start:     formatStatsTime(periodStart),
			End:       formatStatsTime(periodEnd),
			Seconds:   periodSeconds,
			Truncated: truncated,
		},
		Runtime:             runtimeStats,
		Messages:            messages,
		Adapters:            adapters,
		ModelTaskAssignment: u.collectModelAssignments(),
		LLMUsage:            usage,
	}, nil
}

// collectRuntime 统计时段内在线时长；优先使用 online_time 分钟区间，旧库回退按日折算
func (u *statsUploader) collectRuntime(ctx context.Context, periodStart, periodEnd time.Time, periodSeconds int64) (telemetryStatsRuntime, error) {
	out := telemetryStatsRuntime{Precision: statsPrecisionHeartbeat}
	if u.db == nil {
		out.Precision = statsPrecisionNone
		return out, nil
	}
	total := int64(0)
	if onlineIntervalsEmpty(ctx, u.db) {
		out.Precision = statsPrecisionDaily
		from := localDayStartOf(periodStart).Format("2006-01-02")
		to := localDayStartOf(periodEnd).Format("2006-01-02")
		var rows []onlineDayRow
		if err := u.db.WithContext(ctx).Raw(onlineDaysQuery, from, to).Scan(&rows).Error; err != nil {
			return telemetryStatsRuntime{}, fmt.Errorf("查询在线时长失败: %w", err)
		}
		for _, row := range rows {
			dayStart, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(row.DayDate), time.Local)
			if err != nil || row.OnlineSeconds <= 0 {
				continue
			}
			overlap := overlapSeconds(dayStart, dayStart.Add(24*time.Hour), periodStart, periodEnd)
			if overlap <= 0 {
				continue
			}
			if overlap >= statsSecondsPerDay {
				total += row.OnlineSeconds
				continue
			}
			total += int64(math.Round(float64(row.OnlineSeconds) * float64(overlap) / statsSecondsPerDay))
		}
	} else {
		intervals, err := queryOnlineIntervals(ctx, u.db, periodStart, periodEnd)
		if err != nil {
			return telemetryStatsRuntime{}, fmt.Errorf("查询在线区间失败: %w", err)
		}
		total = int64(math.Round(onlineSecondsMergedInRange(intervals, periodStart, periodEnd)))
	}
	out.OnlineSeconds = total
	if periodSeconds > 0 {
		ratio := math.Round(float64(total)/float64(periodSeconds)*10000) / 10000
		if ratio > 1 {
			ratio = 1
		}
		out.CoverageRatio = ratio
	}
	return out, nil
}

// collectMessages 统计收发消息方向、平台与私聊/群聊拆分
func (u *statsUploader) collectMessages(ctx context.Context, periodStart, periodEnd time.Time) (telemetryStatsMessages, error) {
	directions := map[string]*telemetryStatsDirection{
		statsDirectionReceived: newDirectionStats(),
		statsDirectionBotSent:  newDirectionStats(),
	}
	if u.db != nil {
		if accounts := u.botAccounts(); len(accounts) > 0 {
			return u.collectMessagesFromLogs(ctx, periodStart, periodEnd, accounts)
		}
	}
	totals, err := u.directionTotals(ctx, periodStart, periodEnd)
	if err != nil {
		return telemetryStatsMessages{}, err
	}
	split, err := u.chatTypeTotals(ctx, periodStart, periodEnd)
	if err != nil {
		return telemetryStatsMessages{}, err
	}
	platform := u.platformLabel()
	for direction, stats := range directions {
		total := totals[direction]
		private := split[direction][statsChatTypePrivate]
		group := split[direction][statsChatTypeGroup]
		if sum := private + group; sum > total {
			// 两张表分别累计，偶发不一致时以拆分合计为准，避免私聊+群聊大于总数
			total = sum
		}
		stats.Total = total
		if total == 0 && private == 0 && group == 0 {
			continue
		}
		stats.ByPlatform[platform] = &telemetryStatsPlatform{
			Total:   total,
			Private: private,
			Group:   group,
			ByReplyFrequency: map[string]map[string]int64{
				statsChatTypePrivate: newFrequencyBuckets(private),
				statsChatTypeGroup:   newFrequencyBuckets(group),
			},
		}
	}
	return telemetryStatsMessages{ByDirection: directions}, nil
}

func (u *statsUploader) directionTotals(ctx context.Context, periodStart, periodEnd time.Time) (map[string]int64, error) {
	counts := map[string]int64{}
	if u.db == nil {
		if counter := DefaultMessageCounters(); counter != nil {
			inbound, outbound := counter.Totals()
			counts[statsDirectionReceived] = inbound
			counts[statsDirectionBotSent] = outbound
		}
		return counts, nil
	}
	var rows []directionCountRow
	if err := u.db.WithContext(ctx).Raw(directionTotalsQuery, periodStart.UTC(), periodEnd.UTC()).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询消息方向统计失败: %w", err)
	}
	for _, row := range rows {
		if direction := mapDirectionLabel(row.Direction); direction != "" {
			counts[direction] += row.MessageCount
		}
	}
	return counts, nil
}

func (u *statsUploader) chatTypeTotals(ctx context.Context, periodStart, periodEnd time.Time) (map[string]map[string]int64, error) {
	counts := map[string]map[string]int64{}
	if u.db == nil {
		return counts, nil
	}
	var rows []chatTypeCountRow
	if err := u.db.WithContext(ctx).Raw(chatTypeTotalsQuery, periodStart.UTC(), periodEnd.UTC()).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询消息会话拆分失败: %w", err)
	}
	for _, row := range rows {
		direction := mapDirectionLabel(row.Direction)
		if direction == "" {
			continue
		}
		kind, _, err := chatsession.ParseConversationKey(row.ConversationKey)
		if err != nil {
			continue
		}
		chatType := chatsession.ChatTypeFromKind(kind)
		if chatType == "" {
			continue
		}
		if counts[direction] == nil {
			counts[direction] = map[string]int64{}
		}
		counts[direction][chatType] += row.MessageCount
	}
	return counts, nil
}

// collectLLMUsage 从 llm_usage 明细汇总 task/model 的 token 拆分
func (u *statsUploader) collectLLMUsage(ctx context.Context, periodStart, periodEnd time.Time) (map[string]telemetryStatsTaskUsage, error) {
	usage := map[string]telemetryStatsTaskUsage{}
	if u.db == nil {
		return usage, nil
	}
	var rows []llmUsageRow
	if err := u.db.WithContext(ctx).Raw(llmUsageQuery, periodStart.UTC(), periodEnd.UTC()).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询模型调用统计失败: %w", err)
	}
	for _, row := range rows {
		task := strings.TrimSpace(row.Task)
		if task == "" {
			task = statsUnknownLabel
		}
		name := strings.TrimSpace(row.Model)
		if name == "" {
			name = statsUnknownLabel
		}
		entry, ok := usage[task]
		if !ok {
			entry = telemetryStatsTaskUsage{Models: map[string]telemetryStatsModelUsage{}}
		}
		item := entry.Models[name]
		item.RequestCount += row.RequestCount
		item.PromptTokens += row.PromptTokens
		item.CompletionTokens += row.CompletionTokens
		item.TotalTokens += row.TotalTokens
		entry.Models[name] = item
		entry.Total.RequestCount += row.RequestCount
		entry.Total.PromptTokens += row.PromptTokens
		entry.Total.CompletionTokens += row.CompletionTokens
		entry.Total.TotalTokens += row.TotalTokens
		usage[task] = entry
	}
	return usage, nil
}

// collectModelAssignments 从 model_config.toml 的 model_task_config 取任务到模型映射
func (u *statsUploader) collectModelAssignments() map[string]telemetryStatsAssignment {
	out := map[string]telemetryStatsAssignment{}
	if u.models == nil {
		return out
	}
	source := u.models()
	names := make([]string, 0, len(source))
	for name := range source {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		models := append([]string(nil), source[name]...)
		out[name] = telemetryStatsAssignment{Models: models}
	}
	return out
}

// platformLabel Go 侧只有一个适配器时才能确定平台，否则记为 unknown
func (u *statsUploader) platformLabel() string {
	if len(u.tele.adapters) != 1 {
		return statsUnknownLabel
	}
	platform := strings.ToLower(strings.TrimSpace(u.tele.adapters[0].Platform))
	if platform == "" {
		return statsUnknownLabel
	}
	return platform
}

func newDirectionStats() *telemetryStatsDirection {
	return &telemetryStatsDirection{ByPlatform: map[string]*telemetryStatsPlatform{}}
}

// newFrequencyBuckets 回复频率分桶没有 Go 侧数据来源，全部计入 unknown
func newFrequencyBuckets(count int64) map[string]int64 {
	buckets := make(map[string]int64, len(telemetryFrequencyBuckets))
	for _, name := range telemetryFrequencyBuckets {
		buckets[name] = 0
	}
	buckets["unknown"] = count
	return buckets
}

func mapDirectionLabel(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case dirInbound:
		return statsDirectionReceived
	case dirOutbound:
		return statsDirectionBotSent
	default:
		return ""
	}
}

// formatStatsTime 输出 Python datetime.isoformat() 风格的本地时间
func formatStatsTime(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02T15:04:05.000000-07:00")
}

func statsUploadID(clientUUID string, periodStart, periodEnd time.Time) string {
	sum := sha256.Sum256([]byte(clientUUID + ":" + formatStatsTime(periodStart) + ":" + formatStatsTime(periodEnd)))
	return hex.EncodeToString(sum[:])
}

func clientVersionLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return statsUnknownLabel
	}
	return version
}

func localDayStartOf(t time.Time) time.Time {
	t = t.In(time.Local)
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.Local)
}

func overlapSeconds(aStart, aEnd, bStart, bEnd time.Time) int64 {
	start := aStart
	if bStart.After(start) {
		start = bStart
	}
	end := aEnd
	if bEnd.Before(end) {
		end = bEnd
	}
	if !end.After(start) {
		return 0
	}
	return int64(end.Sub(start) / time.Second)
}
