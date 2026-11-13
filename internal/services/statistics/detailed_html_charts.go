package stats

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strconv"
	"strings"
)

var detailedChartColors = []string{
	"#b35b34", "#0d4b50", "#cfa54b", "#6f665b", "#dfc79a",
	"#8f4b38", "#74825a", "#854f46", "#2f5f62", "#b98556",
}

type detailedChartSeries struct {
	Name   string
	Values []float64
}

func writeDetailedDistributionCharts(builder *strings.Builder, period DetailedStatisticsPeriod) {
	builder.WriteString("<h2>数据分布图表</h2>\n<div class=\"pie-chart-grid\">\n")
	writeDetailedPieCard(builder, "ownerPieChart_"+period.Key, "调用来源花费分布", period.Distributions.OwnerCosts, "cost")
	writeDetailedPieCard(builder, "modelPieChart_"+period.Key, "模型花费分布", period.Distributions.ModelCosts, "cost")
	writeDetailedPieCard(builder, "modulePieChart_"+period.Key, "模块花费分布", period.Distributions.ModuleCosts, "cost")
	writeDetailedPieCard(builder, "typePieChart_"+period.Key, "请求类型花费分布", period.Distributions.RequestTypeCosts, "cost")
	writeDetailedPieCard(builder, "chatPieChart_"+period.Key, "聊天消息分布", period.Distributions.ChatMessages, "count")
	writeDetailedPieCard(builder, "chatCostPieChart_"+period.Key, "聊天流花费分布", period.Distributions.ChatCosts, "cost")
	builder.WriteString("</div>\n")
}

func writeDetailedPieCard(builder *strings.Builder, chartID, title string, items []DetailedDistributionItem, valueKind string) {
	filtered := make([]DetailedDistributionItem, 0, len(items))
	var total float64
	for _, item := range items {
		if item.Value <= 0 || math.IsNaN(item.Value) || math.IsInf(item.Value, 0) {
			continue
		}
		filtered = append(filtered, item)
		total += item.Value
	}
	fmt.Fprintf(builder, "<div class=\"pie-chart-card\"><h3>%s</h3><div class=\"pie-chart-canvas-wrap\">", html.EscapeString(title))
	if len(filtered) == 0 || total <= 0 {
		builder.WriteString("<p class=\"empty\">暂无数据</p>")
	} else {
		writeDetailedPieSVG(builder, chartID, title, filtered, total, valueKind)
	}
	builder.WriteString("</div>")
	if len(filtered) > 0 && total > 0 {
		builder.WriteString("<ul class=\"pie-chart-legend\">")
		for index, item := range filtered {
			percentage := item.Value / total * 100
			label := html.EscapeString(item.Name)
			valueText := detailedPieValueText(item.Value, valueKind)
			description := fmt.Sprintf("%s: %s (%.1f%%)", item.Name, valueText, percentage)
			fmt.Fprintf(builder, "<li title=\"%s\"><span class=\"legend-dot\" style=\"background:%s\"></span><span class=\"legend-name\">%s</span><span class=\"legend-value\">%s (%.1f%%)</span></li>",
				html.EscapeString(description), detailedChartColors[index%len(detailedChartColors)], label,
				html.EscapeString(valueText), percentage)
		}
		builder.WriteString("</ul>")
	}
	builder.WriteString("</div>\n")
}

func writeDetailedPieSVG(builder *strings.Builder, chartID, title string, items []DetailedDistributionItem, total float64, valueKind string) {
	fmt.Fprintf(builder, "<svg class=\"pie-chart\" id=\"%s\" viewBox=\"0 0 220 220\" role=\"img\" aria-label=\"%s\">",
		html.EscapeString(chartID), html.EscapeString(title))
	startAngle := -math.Pi / 2
	for index, item := range items {
		fraction := item.Value / total
		color := detailedChartColors[index%len(detailedChartColors)]
		description := fmt.Sprintf("%s: %s (%.1f%%)", item.Name, detailedPieValueText(item.Value, valueKind), fraction*100)
		if fraction >= 0.999999 {
			fmt.Fprintf(builder, "<circle cx=\"110\" cy=\"110\" r=\"92\" fill=\"%s\"><title>%s</title></circle>", color, html.EscapeString(description))
			break
		}
		endAngle := startAngle + fraction*2*math.Pi
		if index == len(items)-1 {
			endAngle = 3 * math.Pi / 2
		}
		x1 := 110 + 92*math.Cos(startAngle)
		y1 := 110 + 92*math.Sin(startAngle)
		x2 := 110 + 92*math.Cos(endAngle)
		y2 := 110 + 92*math.Sin(endAngle)
		largeArc := 0
		if fraction > 0.5 {
			largeArc = 1
		}
		fmt.Fprintf(builder, "<path d=\"M110 110 L%.3f %.3f A92 92 0 %d 1 %.3f %.3f Z\" fill=\"%s\" stroke=\"hsl(36 66%% 89.6%%)\" stroke-width=\"1\"><title>%s</title></path>",
			x1, y1, largeArc, x2, y2, color, html.EscapeString(description))
		startAngle = endAngle
	}
	builder.WriteString("</svg>")
}

func detailedPieValueText(value float64, kind string) string {
	if kind == "cost" {
		return fmt.Sprintf("%.2f ¥", value)
	}
	return formatDetailedLargeNumberText(value)
}

func writeDetailedTrendsTab(builder *strings.Builder, snapshot DetailedStatisticsData) {
	builder.WriteString("<div id=\"charts\" class=\"tab-content\">\n<h2>数据图表</h2>\n")
	builder.WriteString("<div class=\"chart-switch\"><label>时间范围:</label>")
	for _, item := range []struct {
		key   string
		label string
	}{{"6h", "6小时"}, {"12h", "12小时"}, {"24h", "24小时"}, {"48h", "48小时"}} {
		class := "chart-range-btn"
		if item.key == "24h" {
			class += " active"
		}
		fmt.Fprintf(builder, "<button type=\"button\" class=\"%s\" data-trend-range-btn onclick=\"switchTrendRange('%s', this)\">%s</button>",
			class, item.key, item.label)
	}
	builder.WriteString("</div>\n")
	for _, key := range []string{"6h", "12h", "24h", "48h"} {
		writeDetailedTrendPanel(builder, key, snapshot.Trends[key], key == "24h")
	}
	builder.WriteString("</div>\n")
}

func writeDetailedTrendPanel(builder *strings.Builder, rangeKey string, data DetailedStatisticsTrendData, active bool) {
	class := "chart-panel"
	if active {
		class += " active-range"
	}
	fmt.Fprintf(builder, "<div class=\"%s\" data-trend-panel=\"%s\">\n<div class=\"chart-grid\">\n", class, html.EscapeString(rangeKey))
	writeDetailedChartCard(builder, "trend-"+rangeKey+"-total-cost", "总花费", "花费 (¥)",
		data.TimeLabels, []detailedChartSeries{{Name: "总花费", Values: data.TotalCostData}}, true)
	writeDetailedChartCard(builder, "trend-"+rangeKey+"-cost-module", "各模块花费", "花费 (¥)",
		data.TimeLabels, detailedFloatSeries(data.CostByModule), false)
	writeDetailedChartCard(builder, "trend-"+rangeKey+"-cost-model", "各模型花费", "花费 (¥)",
		data.TimeLabels, detailedFloatSeries(data.CostByModel), false)
	writeDetailedChartCard(builder, "trend-"+rangeKey+"-message-chat", "各聊天流消息数", "消息数",
		data.TimeLabels, detailedIntSeries(data.MessageByChat), false)
	builder.WriteString("</div>\n</div>\n")
}

func writeDetailedMetricsTab(builder *strings.Builder, snapshot DetailedStatisticsData) {
	builder.WriteString("<div id=\"metrics\" class=\"tab-content\">\n<h2>指标趋势图表</h2>\n")
	builder.WriteString("<div class=\"chart-switch\"><label>时间尺度:</label>")
	for _, item := range []struct {
		key   string
		label string
	}{{"24h", "24小时"}, {"7d", "7天"}, {"30d", "30天"}} {
		class := "chart-range-btn"
		if item.key == "7d" {
			class += " active"
		}
		fmt.Fprintf(builder, "<button type=\"button\" class=\"%s\" data-metrics-range-btn onclick=\"switchMetricsRange('%s', this)\">%s</button>",
			class, item.key, item.label)
	}
	builder.WriteString("</div>\n")
	for _, key := range []string{"24h", "7d", "30d"} {
		writeDetailedMetricsPanel(builder, key, snapshot.Metrics[key], key == "7d")
	}
	builder.WriteString("</div>\n")
}

func writeDetailedMetricsPanel(builder *strings.Builder, rangeKey string, data DetailedStatisticsMetricsData, active bool) {
	class := "chart-panel"
	if active {
		class += " active-range"
	}
	fmt.Fprintf(builder, "<div class=\"%s\" data-metrics-panel=\"%s\">\n<div class=\"chart-grid\">\n", class, html.EscapeString(rangeKey))
	writeDetailedChartCard(builder, "metrics-"+rangeKey+"-cost-messages", "花费/消息数量", "花费 (¥/100条)",
		data.TimeLabels, []detailedChartSeries{{Name: "花费/消息数量", Values: data.CostPer100Messages}}, false)
	writeDetailedChartCard(builder, "metrics-"+rangeKey+"-cost-hour", "花费/时间", "花费 (¥/小时)",
		data.TimeLabels, []detailedChartSeries{{Name: "花费/时间", Values: data.CostPerHour}}, false)
	writeDetailedChartCard(builder, "metrics-"+rangeKey+"-tokens-hour", "Token/时间", "Token (/小时)",
		data.TimeLabels, []detailedChartSeries{{Name: "Token/时间", Values: data.TokensPerHour}}, false)
	writeDetailedChartCard(builder, "metrics-"+rangeKey+"-cost-replies", "花费/回复数量", "花费 (¥/100条)",
		data.TimeLabels, []detailedChartSeries{{Name: "花费/回复数量", Values: data.CostPer100Replies}}, false)
	builder.WriteString("</div>\n</div>\n")
}

func writeDetailedChartCard(builder *strings.Builder, chartID, title, yLabel string, labels []string, series []detailedChartSeries, fill bool) {
	fmt.Fprintf(builder, "<div class=\"chart-card\"><h3>%s</h3>", html.EscapeString(title))
	writeDetailedLineChart(builder, chartID, title, yLabel, labels, series, fill)
	if len(series) > 1 {
		builder.WriteString("<div class=\"chart-legend\">")
		for index, item := range series {
			fmt.Fprintf(builder, "<span><i style=\"background:%s\"></i>%s</span>",
				detailedChartColors[index%len(detailedChartColors)], html.EscapeString(item.Name))
		}
		builder.WriteString("</div>")
	}
	builder.WriteString("</div>\n")
}

func writeDetailedLineChart(builder *strings.Builder, chartID, title, yLabel string, labels []string, series []detailedChartSeries, fill bool) {
	const width, height = 800.0, 360.0
	const left, right, top, bottom = 72.0, 20.0, 34.0, 52.0
	plotWidth := width - left - right
	plotHeight := height - top - bottom
	maxY := 0.0
	hasData := false
	for _, item := range series {
		for _, value := range item.Values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			if value > maxY {
				maxY = value
			}
			if value != 0 {
				hasData = true
			}
		}
	}
	if maxY <= 0 {
		maxY = 1
	}
	maxY *= 1.1
	xAt := func(index, count int) float64 {
		if count <= 1 {
			return left
		}
		return left + float64(index)/float64(count-1)*plotWidth
	}
	yAt := func(value float64) float64 {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			value = 0
		}
		return top + plotHeight - value/maxY*plotHeight
	}

	fmt.Fprintf(builder, "<svg class=\"line-chart\" id=\"%s\" viewBox=\"0 0 %.0f %.0f\" role=\"img\" aria-label=\"%s\"><title>%s</title>",
		html.EscapeString(chartID), width, height, html.EscapeString(title), html.EscapeString(title))

	for tick := 0; tick <= 4; tick++ {
		value := maxY * float64(tick) / 4
		y := top + plotHeight - float64(tick)/4*plotHeight
		fmt.Fprintf(builder, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"#2f5f62\" stroke-opacity=\"0.22\" />", left, y, left+plotWidth, y)
		fmt.Fprintf(builder, "<text x=\"%.1f\" y=\"%.1f\" text-anchor=\"end\" font-size=\"11\" fill=\"#4c5a4e\">%s</text>", left-8, y+4, html.EscapeString(formatDetailedChartAxis(value)))
	}
	fmt.Fprintf(builder, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"#264a4d\" />", left, top+plotHeight, left+plotWidth, top+plotHeight)
	fmt.Fprintf(builder, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"#264a4d\" />", left, top, left, top+plotHeight)
	fmt.Fprintf(builder, "<text x=\"18\" y=\"%.1f\" transform=\"rotate(-90 18 %.1f)\" text-anchor=\"middle\" font-size=\"11\" fill=\"#4c5a4e\">%s</text>",
		top+plotHeight/2, top+plotHeight/2, html.EscapeString(yLabel))

	for _, index := range detailedChartTickIndices(len(labels), 8) {
		x := xAt(index, len(labels))
		fmt.Fprintf(builder, "<text x=\"%.1f\" y=\"%.1f\" text-anchor=\"middle\" font-size=\"10\" fill=\"#4c5a4e\">%s</text>",
			x, top+plotHeight+18, html.EscapeString(labels[index]))
	}

	if len(labels) > 0 && len(series) > 0 {
		for index, item := range series {
			points := make([]string, 0, len(labels))
			for pointIndex := 0; pointIndex < len(labels); pointIndex++ {
				value := 0.0
				if pointIndex < len(item.Values) {
					value = item.Values[pointIndex]
				}
				points = append(points, fmt.Sprintf("%.2f,%.2f", xAt(pointIndex, len(labels)), yAt(value)))
			}
			color := detailedChartColors[index%len(detailedChartColors)]
			if fill && index == 0 && len(labels) > 1 {
				area := fmt.Sprintf("M %.2f %.2f", xAt(0, len(labels)), top+plotHeight)
				for pointIndex := 0; pointIndex < len(labels); pointIndex++ {
					value := 0.0
					if pointIndex < len(item.Values) {
						value = item.Values[pointIndex]
					}
					area += fmt.Sprintf(" L %.2f %.2f", xAt(pointIndex, len(labels)), yAt(value))
				}
				area += fmt.Sprintf(" L %.2f %.2f Z", xAt(len(labels)-1, len(labels)), top+plotHeight)
				fmt.Fprintf(builder, "<path d=\"%s\" fill=\"%s\" opacity=\"0.12\" stroke=\"none\" />", area, color)
			}
			fmt.Fprintf(builder, "<polyline points=\"%s\" fill=\"none\" stroke=\"%s\" stroke-width=\"2\" />", strings.Join(points, " "), color)
		}
	}
	if !hasData {
		builder.WriteString("<text x=\"50%\" y=\"50%\" text-anchor=\"middle\" font-size=\"14\" fill=\"#777\">暂无数据</text>")
	}
	builder.WriteString("</svg>")
}

func detailedChartTickIndices(count, maxTicks int) []int {
	if count <= 0 {
		return nil
	}
	if count <= maxTicks {
		indices := make([]int, count)
		for i := range indices {
			indices[i] = i
		}
		return indices
	}
	indices := make([]int, 0, maxTicks)
	for i := 0; i < maxTicks; i++ {
		indices = append(indices, int(math.Round(float64(i)*float64(count-1)/float64(maxTicks-1))))
	}
	return indices
}

func formatDetailedChartAxis(value float64) string {
	if value == 0 {
		return "0"
	}
	absolute := math.Abs(value)
	if absolute >= 10000 {
		return strconv.FormatFloat(value/1000, 'f', 1, 64) + "K"
	}
	if absolute >= 100 {
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
	if absolute >= 1 {
		return strconv.FormatFloat(value, 'f', 2, 64)
	}
	return strconv.FormatFloat(value, 'f', 4, 64)
}

func formatDetailedLargeNumberText(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "-"
	}
	if value >= 10000 {
		kValue := value / 1000
		if kValue < 10 {
			return fmt.Sprintf("%.1fK", kValue)
		}
		return fmt.Sprintf("%dK", int64(kValue))
	}
	if value != math.Trunc(value) {
		return fmt.Sprintf("%.1f", value)
	}
	return strconv.FormatInt(int64(value), 10)
}

func detailedFloatSeries(values map[string][]float64) []detailedChartSeries {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]detailedChartSeries, 0, len(names))
	for _, name := range names {
		out = append(out, detailedChartSeries{Name: name, Values: values[name]})
	}
	return out
}

func detailedIntSeries(values map[string][]int64) []detailedChartSeries {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]detailedChartSeries, 0, len(names))
	for _, name := range names {
		source := values[name]
		converted := make([]float64, len(source))
		for index, value := range source {
			converted[index] = float64(value)
		}
		out = append(out, detailedChartSeries{Name: name, Values: converted})
	}
	return out
}
