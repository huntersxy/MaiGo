package stats

// 控制台统计输出，对齐 Python src/chat/utils/statistic.py
// StatisticOutputTask._statistic_console_output：每轮汇总后把「近1小时」核心指标
// 打到日志，便于无 WebUI 部署（docker logs）直接观察。

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// FormatOnlineTime 对齐 _format_online_time：天/小时/分钟/秒分级
func FormatOnlineTime(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	remaining := seconds % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天%d小时%d分钟%d秒", days, hours, minutes, remaining)
	case hours > 0:
		return fmt.Sprintf("%d小时%d分钟%d秒", hours, minutes, remaining)
	default:
		return fmt.Sprintf("%d分钟%d秒", minutes, remaining)
	}
}

// FormatLargeNumber 对齐 _format_large_number：>=10000 用 K 后缀
func FormatLargeNumber(value float64) string {
	if value >= 10000 {
		kValue := value / 1000
		if kValue >= 10 {
			return strconv.FormatInt(int64(kValue), 10) + "K"
		}
		return fmt.Sprintf("%.1fK", kValue)
	}
	if value != float64(int64(value)) {
		return fmt.Sprintf("%.1f", value)
	}
	return strconv.FormatInt(int64(value), 10)
}

// FormatCacheHitRate 对齐 _format_cache_hit_rate：无缓存 token 时 N/A
func FormatCacheHitRate(hitTokens, missTokens int64) string {
	total := hitTokens + missTokens
	if total <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.2f%%", float64(hitTokens)/float64(total)*100)
}

// NormalizePromptCacheTokens 对齐 _normalize_prompt_cache_tokens
func NormalizePromptCacheTokens(promptTokens, hitTokens, missTokens int64) (int64, int64) {
	if hitTokens < 0 {
		hitTokens = 0
	}
	if missTokens < 0 {
		missTokens = 0
	}
	if missTokens == 0 && hitTokens > 0 {
		if promptTokens-hitTokens > 0 {
			missTokens = promptTokens - hitTokens
		}
	}
	if hitTokens+missTokens == 0 && promptTokens > 0 {
		missTokens = promptTokens
	}
	return hitTokens, missTokens
}

// logConsoleStatistics 输出近1小时核心统计，对齐 _statistic_console_output
func logConsoleStatistics(snapshot DetailedStatisticsData) {
	period, ok := findStatisticsPeriod(snapshot, "last_hour")
	if !ok {
		return
	}
	lines := []string{
		strings.Repeat("=", 60),
		fmt.Sprintf("最近1小时统计 · %s", strings.ReplaceAll(snapshot.GeneratedAt, "T", " ")),
		formatTotalStatLine(period.Summary),
	}
	if block := formatBreakdownBlock("模型", period.Models); block != "" {
		lines = append(lines, "", block)
	}
	if block := formatBreakdownBlock("模块", period.Modules); block != "" {
		lines = append(lines, "", block)
	}
	if block := formatChatBlock(period.Chats); block != "" {
		lines = append(lines, "", block)
	}
	lines = append(lines, strings.Repeat("=", 60), "")
	zap.L().Info("\n" + strings.Join(lines, "\n"))
}

func findStatisticsPeriod(snapshot DetailedStatisticsData, key string) (DetailedStatisticsPeriod, bool) {
	for _, period := range snapshot.Periods {
		if period.Key == key {
			return period, true
		}
	}
	return DetailedStatisticsPeriod{}, false
}

func formatTotalStatLine(summary DetailedStatisticsSummary) string {
	return fmt.Sprintf("在线 %s  消息 %s  回复 %s  请求 %s\nToken %s (输入 %s / 输出 %s)  缓存命中 %s\n花费 %.2f¥",
		FormatOnlineTime(int64(summary.OnlineTime)),
		FormatLargeNumber(float64(summary.TotalMessages)),
		FormatLargeNumber(float64(summary.TotalReplies)),
		FormatLargeNumber(float64(summary.TotalRequests)),
		FormatLargeNumber(float64(summary.TotalTokens)),
		FormatLargeNumber(float64(summary.InputTokens)),
		FormatLargeNumber(float64(summary.OutputTokens)),
		FormatCacheHitRate(summary.CacheHitTokens, summary.CacheMissTokens),
		summary.TotalCost,
	)
}

// formatBreakdownBlock 输出模型/模块分类表，无请求时返回空
func formatBreakdownBlock(title string, rows []DetailedStatisticsBreakdown) string {
	if len(rows) == 0 {
		return ""
	}
	lines := []string{fmt.Sprintf("%s                              调用     Token     花费      耗时     缓存命中", title)}
	sorted := append([]DetailedStatisticsBreakdown(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, row := range sorted {
		name := row.Name
		if len([]rune(name)) > 32 {
			name = string([]rune(name)[:29]) + "..."
		}
		lines = append(lines, fmt.Sprintf("%-32s  %6s  %8s  %6.2f¥  %6.1fs  %7s",
			name,
			FormatLargeNumber(float64(row.RequestCount)),
			FormatLargeNumber(float64(row.InputTokens+row.OutputTokens)),
			row.TotalCost,
			row.AvgTimeCost,
			FormatCacheHitRate(row.CacheHitTokens, row.CacheMissTokens),
		))
	}
	return strings.Join(lines, "\n")
}

// formatChatBlock 输出聊天消息前 5，对齐 _format_chat_stat
func formatChatBlock(rows []DetailedChatStatistics) string {
	if len(rows) == 0 {
		return ""
	}
	sorted := append([]DetailedChatStatistics(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MessageCount > sorted[j].MessageCount })
	lines := []string{"聊天消息（前5）:"}
	limit := len(sorted)
	if limit > 5 {
		limit = 5
	}
	var other int64
	for index, row := range sorted {
		if index >= limit {
			other += row.MessageCount
			continue
		}
		name := row.Name
		if strings.TrimSpace(name) == "" {
			name = "未知聊天"
		}
		if len([]rune(name)) > 32 {
			name = string([]rune(name)[:32])
		}
		lines = append(lines, fmt.Sprintf("%-32s  %10s", name, FormatLargeNumber(float64(row.MessageCount))))
	}
	if len(sorted) > limit {
		lines = append(lines, fmt.Sprintf("其余 %d 个会话合计: %s 条", len(sorted)-limit, FormatLargeNumber(float64(other))))
	}
	return strings.Join(lines, "\n")
}
