package stats

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
)

var detailedPeriodLabels = map[string]string{
	"all_time":        "自部署以来",
	"last_30_days":    "近30天",
	"last_7_days":     "近7天",
	"last_3_days":     "近3天",
	"last_24_hours":   "近1天",
	"last_3_hours":    "近3小时",
	"last_hour":       "近1小时",
	"last_15_minutes": "近15分钟",
}

// RenderDetailedHTML 生成与详细统计快照同源的 HTML 报告
func RenderDetailedHTML(snapshot DetailedStatisticsData) ([]byte, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("序列化详细统计报告失败: %w", err)
	}
	jsonText := strings.NewReplacer(
		"&", "\\u0026",
		"<", "\\u003c",
		">", "\\u003e",
	).Replace(string(raw))

	var builder strings.Builder
	builder.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>")
	builder.WriteString("<meta charset=\"utf-8\">")
	builder.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">")
	builder.WriteString("<title>MaiBot运行统计报告</title>")
	builder.WriteString("<script>document.documentElement.className += \" js\";</script>")
	builder.WriteString("<style>")
	builder.WriteString(detailedHTMLCSS)
	builder.WriteString("</style></head><body><div class=\"container\">")

	builder.WriteString("<div class=\"toolbar\"><h1 style=\"margin:0;\">MaiBot运行统计报告</h1>")
	fmt.Fprintf(&builder, "<span class=\"info-item\" style=\"margin:0;\"><strong>统计截止时间:</strong> %s</span></div>\n", html.EscapeString(formatDetailedDisplayTime(snapshot.GeneratedAt)))

	builder.WriteString("<div class=\"tabs\" role=\"tablist\">\n")
	for index, period := range snapshot.Periods {
		active := ""
		if index == 0 {
			active = " active"
		}
		fmt.Fprintf(&builder, "<button type=\"button\" class=\"tab-link%s\" onclick=\"showTab(event, '%s')\">%s</button>\n",
			active, html.EscapeString(period.Key), html.EscapeString(detailedPeriodLabel(period.Key)))
	}
	chartsActive := ""
	if len(snapshot.Periods) == 0 {
		chartsActive = " active"
	}
	fmt.Fprintf(&builder, "<button type=\"button\" class=\"tab-link%s\" onclick=\"showTab(event, 'charts')\">数据图表</button>\n", chartsActive)
	builder.WriteString("<button type=\"button\" class=\"tab-link\" onclick=\"showTab(event, 'metrics')\">指标趋势</button>\n")
	builder.WriteString("</div>\n")

	for index, period := range snapshot.Periods {
		writeDetailedPeriodPanel(&builder, period, index == 0)
	}
	writeDetailedTrendsTab(&builder, snapshot)
	writeDetailedMetricsTab(&builder, snapshot)
	builder.WriteString("<div class=\"footer\">Made with ❤️ by MaiBot • 本页会定期自动覆盖生成</div>")
	builder.WriteString("</div>")

	builder.WriteString("<script type=\"application/json\" id=\"detailed-statistics\">")
	builder.WriteString(jsonText)
	builder.WriteString("</script>\n")
	builder.WriteString("<script>")
	builder.WriteString(detailedHTMLScript)
	builder.WriteString("</script>\n</body></html>\n")
	return []byte(builder.String()), nil
}

func detailedPeriodLabel(key string) string {
	if label, ok := detailedPeriodLabels[key]; ok {
		return label
	}
	return key
}

func writeDetailedPeriodPanel(builder *strings.Builder, period DetailedStatisticsPeriod, active bool) {
	class := "tab-content"
	if active {
		class += " active"
	}
	fmt.Fprintf(builder, "<div id=\"%s\" class=\"%s\" data-tab-panel>\n", html.EscapeString(period.Key), class)
	fmt.Fprintf(builder, "<p class=\"info-item\"><strong>统计时段: </strong>%s ~ %s</p>\n",
		html.EscapeString(formatDetailedDisplayTime(period.StartTime)),
		html.EscapeString(formatDetailedDisplayTime(period.EndTime)))

	summary := period.Summary
	builder.WriteString("<div class=\"kpi-cards\">\n")
	writeDetailedKPI(builder, "总在线时间", html.EscapeString(formatDetailedOnlineTime(summary.OnlineTime)))
	writeDetailedKPI(builder, "总消息数", formatDetailedLargeNumber(float64(summary.TotalMessages)))
	writeDetailedKPI(builder, "总回复数", formatDetailedLargeNumber(float64(summary.TotalReplies)))
	writeDetailedKPI(builder, "总请求数", formatDetailedLargeNumber(float64(summary.TotalRequests)))
	writeDetailedKPI(builder, "总Token数", formatDetailedLargeNumber(float64(summary.TotalTokens)))
	writeDetailedKPI(builder, "总输入Token", formatDetailedLargeNumber(float64(summary.InputTokens)))
	writeDetailedKPI(builder, "总输出Token", formatDetailedLargeNumber(float64(summary.OutputTokens)))
	writeDetailedKPI(builder, "Prompt缓存命中率", html.EscapeString(formatDetailedCacheRate(summary.CacheHitTokens, summary.CacheMissTokens)))
	writeDetailedKPI(builder, "Prompt缓存命中Token", formatDetailedLargeNumber(float64(summary.CacheHitTokens)))
	writeDetailedKPI(builder, "Prompt缓存未命中Token", formatDetailedLargeNumber(float64(summary.CacheMissTokens)))
	writeDetailedKPI(builder, "总花费", fmt.Sprintf("%.2f ¥", summary.TotalCost))
	writeDetailedKPI(builder, "花费/消息数量", fmt.Sprintf("%.4f ¥/100条", summary.CostPer100Messages))
	writeDetailedKPI(builder, "花费/消息数量(排除回复)", fmt.Sprintf("%.4f ¥/100条", summary.CostPer100MessagesExcludingReplies))
	writeDetailedKPI(builder, "花费/回复数量", fmt.Sprintf("%.4f ¥/100条", summary.CostPer100Replies))
	writeDetailedKPI(builder, "花费/时间", fmt.Sprintf("%.2f ¥/小时", summary.CostPerHour))
	writeDetailedKPI(builder, "Token/时间", formatDetailedLargeNumber(summary.TokensPerHour)+"/小时")
	builder.WriteString("</div>\n")

	writeDetailedBreakdownTable(builder, "按模型分类统计", "模型名称", period.Models)
	writeDetailedBreakdownTable(builder, "按模块分类统计", "模块名称", period.Modules)
	writeDetailedBreakdownTable(builder, "按任务组分类统计", "任务组名称", period.TaskGroups)
	writeDetailedBreakdownTable(builder, "按请求类型分类统计", "请求类型", period.RequestTypes)
	writeDetailedChatTable(builder, period.Chats)
	writeDetailedDistributionCharts(builder, period)
	builder.WriteString("</div>\n")
}

func writeDetailedKPI(builder *strings.Builder, title, value string) {
	fmt.Fprintf(builder, "<div class=\"kpi-card\"><div class=\"kpi-title\">%s</div><div class=\"kpi-value\">%s</div></div>\n",
		html.EscapeString(title), value)
}

var detailedBreakdownHeaders = []string{
	"调用次数", "输入Token", "输出Token", "Token总量", "缓存命中Token", "缓存未命中Token", "缓存命中率",
	"累计花费", "平均耗时(秒)", "标准差(秒)", "每次回复平均调用次数", "每次回复平均Token数", "每次调用平均Token",
}

func writeDetailedBreakdownTable(builder *strings.Builder, title, firstName string, rows []DetailedStatisticsBreakdown) {
	fmt.Fprintf(builder, "<h2>%s</h2>\n<div class=\"table-wrap\"><table><thead><tr><th>%s</th>",
		html.EscapeString(title), html.EscapeString(firstName))
	for _, header := range detailedBreakdownHeaders {
		fmt.Fprintf(builder, "<th>%s</th>", html.EscapeString(header))
	}
	builder.WriteString("</tr></thead><tbody>\n")
	if len(rows) == 0 {
		builder.WriteString("<tr><td colspan=\"14\" style=\"text-align:center;color:#999;\">暂无数据</td></tr>\n")
	} else {
		for _, row := range rows {
			fmt.Fprintf(builder, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%.2f ¥</td><td>%.1f 秒</td><td>%.1f 秒</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
				html.EscapeString(row.Name),
				formatDetailedLargeNumber(float64(row.RequestCount)),
				formatDetailedLargeNumber(float64(row.InputTokens)),
				formatDetailedLargeNumber(float64(row.OutputTokens)),
				formatDetailedLargeNumber(float64(row.TotalTokens)),
				formatDetailedLargeNumber(float64(row.CacheHitTokens)),
				formatDetailedLargeNumber(float64(row.CacheMissTokens)),
				html.EscapeString(formatDetailedCacheRate(row.CacheHitTokens, row.CacheMissTokens)),
				row.TotalCost,
				row.AvgTimeCost,
				row.StdTimeCost,
				formatDetailedOptionalLarge(row.AvgCallsPerReply),
				formatDetailedOptionalLarge(row.AvgTokensPerReply),
				formatDetailedOptionalLarge(row.AvgTokensPerCall),
			)
		}
	}
	builder.WriteString("</tbody></table></div>\n")
}

func writeDetailedChatTable(builder *strings.Builder, rows []DetailedChatStatistics) {
	builder.WriteString("<h2>聊天消息统计</h2>\n<div class=\"table-wrap\"><table><thead><tr><th>联系人/群组名称</th><th>消息数量</th></tr></thead><tbody>\n")
	if len(rows) == 0 {
		builder.WriteString("<tr><td colspan=\"2\" style=\"text-align:center;color:#999;\">暂无数据</td></tr>\n")
	} else {
		for _, row := range rows {
			fmt.Fprintf(builder, "<tr><td>%s</td><td>%s</td></tr>\n", html.EscapeString(row.Name), formatDetailedLargeNumber(float64(row.MessageCount)))
		}
	}
	builder.WriteString("</tbody></table></div>\n")
}

func formatDetailedOnlineTime(seconds float64) string {
	total := int64(seconds)
	if total < 0 {
		total = 0
	}
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	remaining := total % 60
	if days > 0 {
		return fmt.Sprintf("%d天%d小时%d分钟%d秒", days, hours, minutes, remaining)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分钟%d秒", hours, minutes, remaining)
	}
	return fmt.Sprintf("%d分钟%d秒", minutes, remaining)
}

func formatDetailedLargeNumber(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "-"
	}
	if value >= 10000 {
		kValue := value / 1000
		number := strconv.FormatInt(int64(kValue), 10)
		if kValue < 10 {
			number = fmt.Sprintf("%.1f", kValue)
		}
		return number + "<span class=\"k-suffix\">K</span>"
	}
	if value != math.Trunc(value) {
		return fmt.Sprintf("%.1f", value)
	}
	return strconv.FormatInt(int64(value), 10)
}

func formatDetailedOptionalLarge(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return formatDetailedLargeNumber(*value)
}

func formatDetailedCacheRate(hitTokens, missTokens int64) string {
	total := hitTokens + missTokens
	if total <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.2f%%", float64(hitTokens)/float64(total)*100)
}

func formatDetailedDisplayTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if parsed, err := time.ParseInLocation("2006-01-02T15:04:05", value, time.Local); err == nil {
		return parsed.Format("2006-01-02 15:04:05")
	}
	if index := strings.IndexByte(value, '.'); index > 0 {
		value = value[:index]
	}
	return strings.Replace(value, "T", " ", 1)
}

const detailedHTMLCSS = `:root {
  --statistics-background: hsl(35.4 61.9% 87.6%);
  --statistics-foreground: hsl(189 72% 18.2%);
  --statistics-card: hsl(36 66% 89.6%);
  --statistics-card-strong: hsl(34.1 54.8% 81.8%);
  --statistics-muted: hsl(34.9 48.3% 82.5%);
  --statistics-muted-foreground: hsl(39.1 11.6% 39%);
  --statistics-primary: hsl(15.6 68.7% 45.1%);
  --statistics-primary-foreground: hsl(39.5 100% 92%);
  --statistics-accent: hsl(34.7 45.6% 75.5%);
  --statistics-border: hsl(188.1 74% 19.6%);
  --statistics-ring: hsl(15.6 68.7% 45.1%);
  --statistics-row-alt: hsl(34.1 54.8% 81.8% / 0.42);
}
html { box-sizing: border-box; }
*, *::before, *::after { box-sizing: inherit; }
body {
  font-family: "Bahnschrift Condensed", "Agency FB", "Arial Narrow", "Microsoft YaHei UI", system-ui, sans-serif;
  margin: 0; padding: 20px; line-height: 1.6; color: var(--statistics-foreground);
  background:
    linear-gradient(90deg, hsl(188.1 74% 19.6% / 0.05) 1px, transparent 1px),
    linear-gradient(0deg, hsl(188.1 74% 19.6% / 0.04) 1px, transparent 1px),
    var(--statistics-background);
  background-size: 28px 28px;
}
.container { width: 100%; max-width: none; margin: 20px auto; padding: 25px; border-radius: 4px;
  background-color: hsl(36 66% 89.6% / 0.94); border: 2px solid var(--statistics-border); }
h1, h2 { color: var(--statistics-foreground); border-bottom: 2px solid var(--statistics-border); padding-bottom: 10px; margin-top: 0; letter-spacing: 0; }
h1 { text-align: center; font-size: 2em; }
h2 { font-size: 1.5em; margin-top: 30px; }
h3 { color: var(--statistics-foreground); }
p { margin-bottom: 10px; }
.toolbar { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; margin-bottom: 8px; }
.info-item { background-color: var(--statistics-muted); padding: 8px 12px; border: 1px solid var(--statistics-border); border-radius: 3px; margin-bottom: 8px; font-size: 0.95em; }
.info-item strong { color: var(--statistics-primary); }
.kpi-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin: 12px 0 6px; }
.kpi-card { background: var(--statistics-card); border: 2px solid var(--statistics-border); border-radius: 4px; padding: 14px 16px; }
.kpi-title { font-size: 12px; color: var(--statistics-muted-foreground); letter-spacing: 0; margin-bottom: 6px; }
.kpi-value { font-size: 20px; font-weight: 800; letter-spacing: 0; color: var(--statistics-primary); overflow-wrap: anywhere; }
.k-suffix { color: var(--statistics-primary); font-weight: bold; }
table { width: 100%; border-collapse: collapse; margin-top: 15px; font-size: 0.9em; }
.table-wrap { width: 100%; overflow-x: auto; border-radius: 3px; border: 1px solid var(--statistics-border); }
th, td { border: 1px solid hsl(188.1 74% 19.6% / 0.35); padding: 10px; text-align: left; }
th { background-color: var(--statistics-border); color: var(--statistics-primary-foreground); font-weight: bold; position: sticky; top: 0; z-index: 1; }
tr:nth-child(even) { background-color: var(--statistics-row-alt); }
.footer { text-align: center; margin-top: 30px; font-size: 0.8em; color: var(--statistics-muted-foreground); }
.tabs { overflow: hidden; background: var(--statistics-card-strong); display: flex; flex-wrap: wrap; border: 2px solid var(--statistics-border); border-radius: 4px; }
.tabs button { background: inherit; border: none; outline: none; padding: 12px 14px; cursor: pointer; transition: 0.2s; font-size: 15px; color: var(--statistics-foreground); }
.tabs button:hover { background-color: var(--statistics-accent); }
.tabs button.active { background-color: var(--statistics-primary); color: var(--statistics-primary-foreground); }
.tab-content { display: block; padding: 20px; background-color: hsl(36 66% 89.6% / 0.76); border: 2px solid var(--statistics-border); border-top: none; border-radius: 0 0 4px 4px; }
html.js .tab-content { display: none; }
html.js .tab-content.active { display: block; }
.pie-chart-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; margin-top: 20px; align-items: stretch; }
.pie-chart-card { display: flex; min-width: 0; flex-direction: column; gap: 12px; padding: 14px; border: 1px solid var(--statistics-border); border-radius: 4px; background: hsl(36 66% 89.6% / 0.55); }
.pie-chart-card h3 { margin: 0; min-height: 1.6em; font-size: 1.1em; }
.pie-chart-canvas-wrap { width: 100%; height: 330px; min-height: 330px; }
.pie-chart { width: 100%; height: 100%; }
.pie-chart-legend { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 10px; max-height: 150px; min-height: 44px; overflow-y: auto; padding: 8px; border: 1px solid hsl(188.1 74% 19.6% / 0.35); border-radius: 3px; background: hsl(34.9 48.3% 82.5% / 0.5); list-style: none; margin: 0; }
.pie-chart-legend li { display: grid; grid-template-columns: 12px minmax(0, 1fr) auto; align-items: center; gap: 6px; min-width: 0; padding: 3px 4px; line-height: 1.25; }
.pie-chart-legend .legend-dot { width: 10px; height: 10px; border-radius: 2px; border: 1px solid hsl(188.1 74% 19.6% / 0.25); }
.pie-chart-legend .legend-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pie-chart-legend .legend-value { color: var(--statistics-muted-foreground); white-space: nowrap; }
.chart-switch { margin: 20px 0; text-align: center; }
.chart-switch label { margin-right: 10px; font-weight: bold; }
.chart-switch button { background-color: var(--statistics-card); border: 1px solid var(--statistics-border); color: var(--statistics-foreground); padding: 8px 16px; margin: 0 5px; border-radius: 3px; cursor: pointer; font-size: 14px; transition: all 0.3s ease; }
.chart-switch button:hover { background-color: var(--statistics-accent); }
.chart-switch button.active { background-color: var(--statistics-primary); color: var(--statistics-primary-foreground); border-color: var(--statistics-ring); }
.chart-panel { display: block; }
html.js .chart-panel { display: none; }
html.js .chart-panel.active-range { display: block; }
.chart-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; margin-top: 20px; }
.chart-card { min-width: 0; padding: 14px; border: 1px solid var(--statistics-border); border-radius: 4px; background: hsl(36 66% 89.6% / 0.55); }
.chart-card h3 { margin: 0 0 8px; font-size: 1.1em; }
.line-chart { width: 100%; height: auto; display: block; background: hsl(36 66% 89.6% / 0.35); border-radius: 3px; }
.chart-legend { display: flex; flex-wrap: wrap; gap: 4px 12px; margin-top: 6px; font-size: 12px; }
.chart-legend span { display: inline-flex; align-items: center; gap: 4px; }
.chart-legend i { width: 10px; height: 3px; display: inline-block; }
.empty { color: #777; text-align: center; padding: 24px 8px; }
@media (max-width: 900px) { .chart-grid { grid-template-columns: 1fr; } }
@media (max-width: 760px) { .pie-chart-grid { grid-template-columns: 1fr; } .pie-chart-legend { grid-template-columns: 1fr; } .toolbar { align-items: flex-start; } }
`

const detailedHTMLScript = `function showTab(evt, name) {
  var panels = document.querySelectorAll('.tab-content');
  for (var i = 0; i < panels.length; i++) panels[i].classList.remove('active');
  var links = document.querySelectorAll('.tab-link');
  for (var j = 0; j < links.length; j++) links[j].classList.remove('active');
  var target = document.getElementById(name);
  if (target) target.classList.add('active');
  if (evt && evt.currentTarget) evt.currentTarget.classList.add('active');
}
function switchTrendRange(range, button) {
  var buttons = document.querySelectorAll('[data-trend-range-btn]');
  for (var i = 0; i < buttons.length; i++) buttons[i].classList.remove('active');
  if (button) button.classList.add('active');
  var panels = document.querySelectorAll('[data-trend-panel]');
  for (var j = 0; j < panels.length; j++) panels[j].classList.toggle('active-range', panels[j].getAttribute('data-trend-panel') === range);
}
function switchMetricsRange(range, button) {
  var buttons = document.querySelectorAll('[data-metrics-range-btn]');
  for (var i = 0; i < buttons.length; i++) buttons[i].classList.remove('active');
  if (button) button.classList.add('active');
  var panels = document.querySelectorAll('[data-metrics-panel]');
  for (var j = 0; j < panels.length; j++) panels[j].classList.toggle('active-range', panels[j].getAttribute('data-metrics-panel') === range);
}
`
