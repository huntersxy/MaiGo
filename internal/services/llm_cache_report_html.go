package services

// LLM 缓存统计 HTML 报告
// 对齐 MaiBot src/services/llm_cache_stats.py 的 _render_html_report：
//   - run 对比、当前/上一个 run 调用点对比
//   - 每个历史 run 的显著性切换表与脚本
//   - 低/高 API 命中率摘要、全量调用点统计
//   - overview/sessions 两份报告共用同一批事件与 run 汇总

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cacheEscape 对齐 Python html.escape(quote=True) 的实体形式
func cacheEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#x27;").Replace(value)
}

func cacheString(item map[string]any, key string) string {
	return stringFromCacheAny(item[key])
}

func cacheFloat(item map[string]any, key string) float64 {
	return float64FromCacheAny(item[key])
}

func cacheInt(item map[string]any, key string) int64 {
	return int64FromCacheAny(item[key])
}

// formatPythonFloat 对齐 Python str(float)：整数值保留 .0
func formatPythonFloat(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

func renderLLMCacheHTMLReport(statsSnapshot []map[string]any, usageEvents []map[string]any, runStats []map[string]any, includeSession bool) string {
	updatedAt := time.Now().Format(llmCacheReportTimeLayout)
	visibleStats := AggregateLLMCacheStatsSnapshot(statsSnapshot, includeSession)
	currentRunID := llmCacheRunID
	previousRunID := getPreviousLLMCacheRunID(runStats, currentRunID)
	currentByCallSite := aggregateLLMCacheCallSiteMap(usageEvents, currentRunID, includeSession)
	previousByCallSite := map[string]map[string]any{}
	if previousRunID != "" {
		previousByCallSite = aggregateLLMCacheCallSiteMap(usageEvents, previousRunID, includeSession)
	}

	sortedByRate := append([]map[string]any(nil), visibleStats...)
	sort.SliceStable(sortedByRate, func(i, j int) bool {
		leftRate := cacheFloat(sortedByRate[i], "prompt_cache_hit_rate")
		rightRate := cacheFloat(sortedByRate[j], "prompt_cache_hit_rate")
		if leftRate != rightRate {
			return leftRate < rightRate
		}
		return cacheInt(sortedByRate[i], "prompt_cache_miss_tokens") > cacheInt(sortedByRate[j], "prompt_cache_miss_tokens")
	})
	lowStats := sortedByRate
	if len(lowStats) > llmCacheSummaryLimit {
		lowStats = lowStats[:llmCacheSummaryLimit]
	}
	highStart := len(sortedByRate) - llmCacheSummaryLimit
	if highStart < 0 {
		highStart = 0
	}
	highStats := make([]map[string]any, 0, len(sortedByRate)-highStart)
	for index := len(sortedByRate) - 1; index >= highStart; index-- {
		highStats = append(highStats, sortedByRate[index])
	}

	allStats := append([]map[string]any(nil), visibleStats...)
	sort.SliceStable(allStats, func(i, j int) bool {
		left, right := allStats[i], allStats[j]
		if cacheString(left, "task_name") != cacheString(right, "task_name") {
			return cacheString(left, "task_name") < cacheString(right, "task_name")
		}
		if cacheString(left, "request_type") != cacheString(right, "request_type") {
			return cacheString(left, "request_type") < cacheString(right, "request_type")
		}
		return cacheString(left, "model_name") < cacheString(right, "model_name")
	})

	var totalCalls, totalPromptTokens, totalHitTokens int64
	var totalTheoreticalHitTokens, totalMissTokens, totalTheoreticalMissTokens int64
	for _, item := range visibleStats {
		totalCalls += cacheInt(item, "calls")
		totalPromptTokens += cacheInt(item, "prompt_tokens")
		totalHitTokens += cacheInt(item, "prompt_cache_hit_tokens")
		totalTheoreticalHitTokens += cacheInt(item, "theoretical_prompt_cache_hit_tokens")
		totalMissTokens += cacheInt(item, "prompt_cache_miss_tokens")
		totalTheoreticalMissTokens += cacheInt(item, "theoretical_prompt_cache_miss_tokens")
	}
	totalCacheTokens := totalHitTokens + totalMissTokens
	totalTheoreticalCacheTokens := totalTheoreticalHitTokens + totalTheoreticalMissTokens
	overallHitRate := 0.0
	if totalCacheTokens > 0 {
		overallHitRate = float64(totalHitTokens) / float64(totalCacheTokens) * 100
	}
	overallTheoreticalHitRate := 0.0
	if totalTheoreticalCacheTokens > 0 {
		overallTheoreticalHitRate = float64(totalTheoreticalHitTokens) / float64(totalTheoreticalCacheTokens) * 100
	}

	reportTitle := "LLM Prompt Cache Stats"
	if includeSession {
		reportTitle = "LLM Prompt Cache Stats By Session"
	}
	peerReportLink := "<a href=\"sessions.html\">Session detail report</a>"
	if includeSession {
		peerReportLink = "<a href=\"report.html\">Overview report</a>"
	}
	sessionHead := ""
	if includeSession {
		sessionHead = "<th>Session</th>"
	}
	tableHead := "<thead><tr><th>Task</th><th>Request</th><th>Model</th>" + sessionHead +
		"<th>API hit</th><th>Theory hit</th><th>Delta</th><th>API hit tok</th><th>API miss tok</th>" +
		"<th>Theory hit tok</th><th>Theory miss tok</th><th>Prompt tok</th><th>Calls</th><th>Reported</th>" +
		"<th>Compared</th><th>Pool hits</th><th>Avg prefix</th><th>Sliding calls</th>" +
		"<th>Avg dropped msg</th><th>Avg aligned msg</th><th>Top dynamic diff paths</th></tr></thead>"
	runTableHead := "<thead><tr><th></th><th>Run ID</th><th>Process started</th><th>First event</th><th>Last event</th>" +
		"<th>Calls</th><th>Prompt tok</th><th>API hit</th><th>Theory hit</th><th>Avg prefix</th><th>Sliding calls</th></tr></thead>"
	runCompareHead := "<thead><tr><th>Task</th><th>Request</th><th>Model</th>" + sessionHead +
		"<th>Current calls</th><th>Previous calls</th><th>Current API</th><th>Previous API</th><th>API delta</th>" +
		"<th>Current Theory</th><th>Previous Theory</th><th>Theory delta</th>" +
		"<th>Current Prefix</th><th>Previous Prefix</th><th>Prefix delta</th>" +
		"<th>Current Sliding</th><th>Previous Sliding</th></tr></thead>"
	runSignificanceHead := "<thead><tr><th>Baseline run</th><th>Baseline time</th><th>Task</th><th>Request</th><th>Model</th>" +
		sessionHead + "<th>Current calls</th><th>Baseline calls</th>" +
		"<th>API delta</th><th>API confidence</th><th>API significant</th>" +
		"<th>Prefix delta</th><th>Prefix confidence</th><th>Prefix significant</th>" +
		"<th>Current sliding</th><th>Baseline sliding</th></tr></thead>"

	var builder strings.Builder
	builder.WriteString("<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n  <meta charset=\"utf-8\">\n  <title>")
	builder.WriteString(cacheEscape(reportTitle))
	builder.WriteString("</title>\n  <style>\n")
	builder.WriteString("    body { font-family: \"Segoe UI\", \"Microsoft YaHei\", sans-serif; margin: 24px; color: #202124; background: #f7f8fa; }\n" +
		"    h1 { font-size: 24px; margin: 0 0 8px; }\n" +
		"    h2 { font-size: 18px; margin: 28px 0 12px; }\n" +
		"    .meta { color: #5f6368; margin-bottom: 20px; }\n" +
		"    .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin: 16px 0 24px; }\n" +
		"    .card { background: #fff; border: 1px solid #e3e6ea; border-radius: 8px; padding: 14px 16px; }\n" +
		"    .label { color: #5f6368; font-size: 13px; }\n" +
		"    .value { font-size: 22px; font-weight: 650; margin-top: 6px; }\n" +
		"    .muted { color: #5f6368; font-size: 13px; }\n" +
		"    .run-switcher { display: flex; align-items: center; gap: 10px; margin: 0 0 12px; padding: 12px; background: #fff; border: 1px solid #e3e6ea; border-radius: 8px; }\n" +
		"    .run-switcher button { width: 32px; height: 32px; border: 1px solid #cbd3dc; background: #f8fafc; border-radius: 6px; cursor: pointer; }\n" +
		"    .run-switcher input[type=\"range\"] { min-width: 180px; flex: 1; }\n" +
		"    .run-switcher-meta { min-width: 320px; }\n" +
		"    .run-switcher-meta .value { font-size: 15px; overflow-wrap: anywhere; }\n" +
		"    table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #e3e6ea; }\n" +
		"    th, td { padding: 9px 10px; border-bottom: 1px solid #edf0f2; text-align: left; font-size: 13px; vertical-align: top; }\n" +
		"    th { background: #f0f3f6; position: sticky; top: 0; }\n" +
		"    tr:last-child td { border-bottom: 0; }\n")
	builder.WriteString("  </style>\n</head>\n<body>\n  <h1>")
	builder.WriteString(cacheEscape(reportTitle))
	builder.WriteString("</h1>\n  <div class=\"meta\">Updated at: ")
	builder.WriteString(cacheEscape(updatedAt))
	builder.WriteString(". Current run: ")
	builder.WriteString(cacheEscape(currentRunID))
	builder.WriteString(". Process started at: ")
	builder.WriteString(cacheEscape(llmCacheProcessStartedAt.Format(llmCacheReportTimeLayout)))
	builder.WriteString(". Grouped by task_name / request_type / model_name")
	if includeSession {
		builder.WriteString(" / session_id")
	}
	builder.WriteString(". Local prompt pool size: ")
	builder.WriteString(strconv.Itoa(llmCachePromptPoolSize))
	builder.WriteString(". ")
	builder.WriteString(peerReportLink)
	builder.WriteString("</div>\n  <div class=\"cards\">\n")
	writeLLMCacheCard(&builder, "Calls", formatLLMCacheInt(totalCalls))
	writeLLMCacheCard(&builder, "Prompt tokens", formatLLMCacheInt(totalPromptTokens))
	writeLLMCacheCard(&builder, "API hit tokens", formatLLMCacheInt(totalHitTokens))
	writeLLMCacheCard(&builder, "API hit rate", formatLLMCacheRate(overallHitRate))
	writeLLMCacheCard(&builder, "Theory hit tokens", formatLLMCacheInt(totalTheoreticalHitTokens))
	writeLLMCacheCard(&builder, "Theory hit rate", formatLLMCacheRate(overallTheoreticalHitRate))
	builder.WriteString("  </div>\n  <h2>Run Comparison</h2>\n  <table>\n    ")
	builder.WriteString(runTableHead)
	builder.WriteString("\n    <tbody>")
	builder.WriteString(renderLLMCacheRunRows(runStats, currentRunID))
	builder.WriteString("</tbody>\n  </table>\n  <h2>Current vs Previous Run By Call Site</h2>\n  <table>\n    ")
	builder.WriteString(runCompareHead)
	builder.WriteString("\n    <tbody>")
	builder.WriteString(renderLLMCacheRunComparisonRows(currentByCallSite, previousByCallSite, includeSession))
	builder.WriteString("</tbody>\n  </table>\n  <h2>Current vs Every Previous Run Significance</h2>\n  ")
	builder.WriteString(renderLLMCacheRunSignificanceControls(runStats, currentRunID))
	builder.WriteString("\n  <table>\n    ")
	builder.WriteString(runSignificanceHead)
	builder.WriteString("\n    <tbody id=\"run-significance-body\">")
	builder.WriteString(renderLLMCacheRunSignificanceRows(usageEvents, runStats, currentRunID, includeSession))
	builder.WriteString("</tbody>\n  </table>\n  <h2>Low API Hit Rate</h2>\n  <table>\n    ")
	builder.WriteString(tableHead)
	builder.WriteString("\n    <tbody>")
	builder.WriteString(renderLLMCacheStatRows(lowStats, includeSession))
	builder.WriteString("</tbody>\n  </table>\n  <h2>High API Hit Rate</h2>\n  <table>\n    ")
	builder.WriteString(tableHead)
	builder.WriteString("\n    <tbody>")
	builder.WriteString(renderLLMCacheStatRows(highStats, includeSession))
	builder.WriteString("</tbody>\n  </table>\n  <h2>All Call Sites</h2>\n  <table>\n    ")
	builder.WriteString(tableHead)
	builder.WriteString("\n    <tbody>")
	builder.WriteString(renderLLMCacheStatRows(allStats, includeSession))
	builder.WriteString("</tbody>\n  </table>\n  ")
	builder.WriteString(renderLLMCacheRunSignificanceScript())
	builder.WriteString("\n</body>\n</html>\n")
	return builder.String()
}

func writeLLMCacheCard(builder *strings.Builder, label, value string) {
	builder.WriteString("    <div class=\"card\"><div class=\"label\">")
	builder.WriteString(cacheEscape(label))
	builder.WriteString("</div><div class=\"value\">")
	builder.WriteString(value)
	builder.WriteString("</div></div>\n")
}

func renderLLMCacheRunRows(runStats []map[string]any, currentRunID string) string {
	start := len(runStats) - llmCacheReportRunLimit
	if start < 0 {
		start = 0
	}
	rows := make([]string, 0, len(runStats)-start)
	for index := len(runStats) - 1; index >= start; index-- {
		item := runStats[index]
		marker := ""
		if cacheString(item, "run_id") == currentRunID {
			marker = "当前"
		}
		rows = append(rows, "<tr><td>"+cacheEscape(marker)+"</td><td>"+cacheEscape(cacheString(item, "run_id"))+
			"</td><td>"+cacheEscape(cacheString(item, "process_started_at"))+
			"</td><td>"+cacheEscape(cacheString(item, "first_seen_at"))+
			"</td><td>"+cacheEscape(cacheString(item, "last_seen_at"))+
			"</td><td>"+formatLLMCacheInt(item["calls"])+
			"</td><td>"+formatLLMCacheInt(item["prompt_tokens"])+
			"</td><td>"+formatLLMCacheRate(item["prompt_cache_hit_rate"])+
			"</td><td>"+formatLLMCacheRate(item["theoretical_prompt_cache_hit_rate"])+
			"</td><td>"+formatLLMCacheRate(item["avg_common_prefix_rate"])+
			"</td><td>"+formatLLMCacheInt(item["suspected_context_sliding_calls"])+"</td></tr>")
	}
	return strings.Join(rows, "\n")
}

func renderLLMCacheRunComparisonRows(current, previous map[string]map[string]any, includeSession bool) string {
	keySet := map[string]llmCacheCallSiteKey{}
	for _, source := range []map[string]map[string]any{current, previous} {
		for _, item := range source {
			key := llmCacheCallSiteKey{
				TaskName:    cacheString(item, "task_name"),
				RequestType: cacheString(item, "request_type"),
				ModelName:   cacheString(item, "model_name"),
				SessionID:   cacheString(item, "session_id"),
			}
			keySet[key.id()] = key
		}
	}
	keys := make([]llmCacheCallSiteKey, 0, len(keySet))
	for _, key := range keySet {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	rows := make([]string, 0, len(keys))
	for _, key := range keys {
		currentItem := current[key.id()]
		if currentItem == nil {
			currentItem = map[string]any{}
		}
		previousItem := previous[key.id()]
		if previousItem == nil {
			previousItem = map[string]any{}
		}
		currentAPI := cacheFloat(currentItem, "prompt_cache_hit_rate")
		previousAPI := cacheFloat(previousItem, "prompt_cache_hit_rate")
		currentTheory := cacheFloat(currentItem, "theoretical_prompt_cache_hit_rate")
		previousTheory := cacheFloat(previousItem, "theoretical_prompt_cache_hit_rate")
		currentPrefix := cacheFloat(currentItem, "avg_common_prefix_rate")
		previousPrefix := cacheFloat(previousItem, "avg_common_prefix_rate")
		row := "<tr><td>" + cacheEscape(key.TaskName) + "</td><td>" + cacheEscape(key.RequestType) + "</td><td>" + cacheEscape(key.ModelName) + "</td>"
		if includeSession {
			row += "<td>" + cacheEscape(key.SessionID) + "</td>"
		}
		row += "<td>" + formatLLMCacheInt(currentItem["calls"]) +
			"</td><td>" + formatLLMCacheInt(previousItem["calls"]) +
			"</td><td>" + formatLLMCacheRate(currentAPI) +
			"</td><td>" + formatLLMCacheRate(previousAPI) +
			"</td><td>" + formatLLMCacheRate(currentAPI-previousAPI) +
			"</td><td>" + formatLLMCacheRate(currentTheory) +
			"</td><td>" + formatLLMCacheRate(previousTheory) +
			"</td><td>" + formatLLMCacheRate(currentTheory-previousTheory) +
			"</td><td>" + formatLLMCacheRate(currentPrefix) +
			"</td><td>" + formatLLMCacheRate(previousPrefix) +
			"</td><td>" + formatLLMCacheRate(currentPrefix-previousPrefix) +
			"</td><td>" + formatLLMCacheInt(currentItem["suspected_context_sliding_calls"]) +
			"</td><td>" + formatLLMCacheInt(previousItem["suspected_context_sliding_calls"]) + "</td></tr>"
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func renderLLMCacheRunSignificanceControls(runStats []map[string]any, currentRunID string) string {
	previous := getPreviousLLMCacheRunStats(runStats, currentRunID)
	if len(previous) == 0 {
		return "<div class=\"run-switcher\" id=\"run-significance-switcher\" data-run-count=\"0\"><span class=\"muted\">No previous runs to compare.</span></div>"
	}
	options := make([]map[string]any, 0, len(previous))
	for _, item := range previous {
		options = append(options, map[string]any{
			"run_id":     cacheString(item, "run_id"),
			"time_label": formatLLMCacheRunTimeLabel(item),
			"calls":      cacheInt(item, "calls"),
		})
	}
	rawOptions, _ := json.Marshal(options)
	optionJSON := cacheEscape(string(rawOptions))
	maxIndex := len(previous) - 1
	return "<div class=\"run-switcher\" id=\"run-significance-switcher\" data-run-options=\"" + optionJSON +
		"\" data-run-count=\"" + strconv.Itoa(len(previous)) + "\">" +
		"<button type=\"button\" id=\"run-significance-prev\" aria-label=\"Previous baseline run\">&lt;</button>" +
		"<input id=\"run-significance-slider\" type=\"range\" min=\"0\" max=\"" + strconv.Itoa(maxIndex) +
		"\" value=\"" + strconv.Itoa(maxIndex) + "\" step=\"1\">" +
		"<button type=\"button\" id=\"run-significance-next\" aria-label=\"Next baseline run\">&gt;</button>" +
		"<div class=\"run-switcher-meta\"><div class=\"label\">Baseline run</div>" +
		"<div class=\"value\" id=\"run-significance-label\"></div>" +
		"<div class=\"muted\" id=\"run-significance-time\"></div></div></div>"
}

func renderLLMCacheRunSignificanceRows(usageEvents []map[string]any, runStats []map[string]any, currentRunID string, includeSession bool) string {
	currentByCallSite := aggregateLLMCacheCallSiteMap(usageEvents, currentRunID, includeSession)
	rows := make([]string, 0)
	for _, previousRunStat := range getPreviousLLMCacheRunStats(runStats, currentRunID) {
		previousRunID := cacheString(previousRunStat, "run_id")
		baselineTime := formatLLMCacheRunTimeLabel(previousRunStat)
		previousByCallSite := aggregateLLMCacheCallSiteMap(usageEvents, previousRunID, includeSession)
		keys := make([]llmCacheCallSiteKey, 0)
		for id, item := range currentByCallSite {
			if _, exists := previousByCallSite[id]; !exists {
				continue
			}
			keys = append(keys, llmCacheCallSiteKey{
				TaskName:    cacheString(item, "task_name"),
				RequestType: cacheString(item, "request_type"),
				ModelName:   cacheString(item, "model_name"),
				SessionID:   cacheString(item, "session_id"),
			})
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
		for _, key := range keys {
			currentItem := currentByCallSite[key.id()]
			previousItem := previousByCallSite[key.id()]
			currentHit := cacheInt(currentItem, "prompt_cache_hit_tokens")
			currentMiss := cacheInt(currentItem, "prompt_cache_miss_tokens")
			previousHit := cacheInt(previousItem, "prompt_cache_hit_tokens")
			previousMiss := cacheInt(previousItem, "prompt_cache_miss_tokens")
			currentTotal := currentHit + currentMiss
			previousTotal := previousHit + previousMiss
			currentAPI := cacheRate(currentHit, currentMiss)
			previousAPI := cacheRate(previousHit, previousMiss)
			apiConfidence := calculateTwoProportionConfidence(currentHit, currentTotal, previousHit, previousTotal)
			currentCalls := cacheInt(currentItem, "calls")
			previousCalls := cacheInt(previousItem, "calls")
			currentPrefix := cacheFloat(currentItem, "avg_common_prefix_rate")
			previousPrefix := cacheFloat(previousItem, "avg_common_prefix_rate")
			prefixConfidence := calculateMeanDifferenceConfidence(
				currentPrefix, cacheFloat(currentItem, "common_prefix_rate_variance"), currentCalls,
				previousPrefix, cacheFloat(previousItem, "common_prefix_rate_variance"), previousCalls,
			)
			row := "<tr data-baseline-run-id=\"" + cacheEscape(previousRunID) + "\"><td>" + cacheEscape(previousRunID) +
				"</td><td>" + cacheEscape(baselineTime) +
				"</td><td>" + cacheEscape(key.TaskName) + "</td><td>" + cacheEscape(key.RequestType) +
				"</td><td>" + cacheEscape(key.ModelName) + "</td>"
			if includeSession {
				row += "<td>" + cacheEscape(key.SessionID) + "</td>"
			}
			row += "<td>" + formatLLMCacheInt(currentCalls) +
				"</td><td>" + formatLLMCacheInt(previousCalls) +
				"</td><td>" + formatLLMCacheRate(currentAPI-previousAPI) +
				"</td><td>" + formatLLMCacheRate(apiConfidence) +
				"</td><td>" + cacheEscape(formatSignificanceLabel(apiConfidence)) +
				"</td><td>" + formatLLMCacheRate(currentPrefix-previousPrefix) +
				"</td><td>" + formatLLMCacheRate(prefixConfidence) +
				"</td><td>" + cacheEscape(formatSignificanceLabel(prefixConfidence)) +
				"</td><td>" + formatLLMCacheInt(currentItem["suspected_context_sliding_calls"]) +
				"</td><td>" + formatLLMCacheInt(previousItem["suspected_context_sliding_calls"]) + "</td></tr>"
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return "<tr><td colspan=\"14\">当前 run 还没有可与历史 run 比较的同类调用点，或历史数据缺少 run_id。</td></tr>"
	}
	return strings.Join(rows, "\n")
}

func renderLLMCacheStatRows(stats []map[string]any, includeSession bool) string {
	rows := make([]string, 0, len(stats))
	for _, item := range stats {
		row := "<tr><td>" + cacheEscape(cacheString(item, "task_name")) +
			"</td><td>" + cacheEscape(cacheString(item, "request_type")) +
			"</td><td>" + cacheEscape(cacheString(item, "model_name")) + "</td>"
		if includeSession {
			row += "<td>" + cacheEscape(cacheString(item, "session_id")) + "</td>"
		}
		row += "<td>" + formatLLMCacheRate(item["prompt_cache_hit_rate"]) +
			"</td><td>" + formatLLMCacheRate(item["theoretical_prompt_cache_hit_rate"]) +
			"</td><td>" + formatLLMCacheRate(item["prompt_cache_hit_rate_delta"]) +
			"</td><td>" + formatLLMCacheInt(item["prompt_cache_hit_tokens"]) +
			"</td><td>" + formatLLMCacheInt(item["prompt_cache_miss_tokens"]) +
			"</td><td>" + formatLLMCacheInt(item["theoretical_prompt_cache_hit_tokens"]) +
			"</td><td>" + formatLLMCacheInt(item["theoretical_prompt_cache_miss_tokens"]) +
			"</td><td>" + formatLLMCacheInt(item["prompt_tokens"]) +
			"</td><td>" + formatLLMCacheInt(item["calls"]) +
			"</td><td>" + formatLLMCacheInt(item["cache_reported_calls"]) +
			"</td><td>" + formatLLMCacheInt(item["theoretical_compared_calls"]) +
			"</td><td>" + formatLLMCacheInt(item["theoretical_cache_pool_hits"]) +
			"</td><td>" + formatLLMCacheRate(item["avg_common_prefix_rate"]) +
			"</td><td>" + formatLLMCacheInt(item["suspected_context_sliding_calls"]) +
			"</td><td>" + formatPythonFloat(cacheFloat(item, "avg_sliding_dropped_messages")) +
			"</td><td>" + formatPythonFloat(cacheFloat(item, "avg_sliding_aligned_messages")) +
			"</td><td>" + cacheEscape(cacheString(item, "top_dynamic_diff_paths")) + "</td></tr>"
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func renderLLMCacheRunSignificanceScript() string {
	return "<script>\n" +
		"    (() => {\n" +
		"      const switcher = document.getElementById(\"run-significance-switcher\");\n" +
		"      const tableBody = document.getElementById(\"run-significance-body\");\n" +
		"      if (!switcher || !tableBody) {\n" +
		"        return;\n" +
		"      }\n" +
		"      let options = [];\n" +
		"      try {\n" +
		"        options = JSON.parse(switcher.dataset.runOptions || \"[]\");\n" +
		"      } catch {\n" +
		"        options = [];\n" +
		"      }\n" +
		"      if (!options.length) {\n" +
		"        return;\n" +
		"      }\n" +
		"\n" +
		"      const slider = document.getElementById(\"run-significance-slider\");\n" +
		"      const prevButton = document.getElementById(\"run-significance-prev\");\n" +
		"      const nextButton = document.getElementById(\"run-significance-next\");\n" +
		"      const label = document.getElementById(\"run-significance-label\");\n" +
		"      const time = document.getElementById(\"run-significance-time\");\n" +
		"\n" +
		"      const clampIndex = (value) => Math.max(0, Math.min(options.length - 1, value));\n" +
		"      const showRun = (index) => {\n" +
		"        const activeIndex = clampIndex(Number.parseInt(String(index), 10) || 0);\n" +
		"        const active = options[activeIndex];\n" +
		"        slider.value = String(activeIndex);\n" +
		"        label.textContent = (activeIndex + 1) + \"/\" + options.length + \" - \" + active.run_id;\n" +
		"        time.textContent = (active.time_label || \"No timestamp\") + \" - \" + active.calls + \" calls\";\n" +
		"        tableBody.querySelectorAll(\"tr[data-baseline-run-id]\").forEach((row) => {\n" +
		"          row.hidden = row.dataset.baselineRunId !== active.run_id;\n" +
		"        });\n" +
		"      };\n" +
		"\n" +
		"      slider.addEventListener(\"input\", () => showRun(slider.value));\n" +
		"      prevButton.addEventListener(\"click\", () => showRun(Number(slider.value) - 1));\n" +
		"      nextButton.addEventListener(\"click\", () => showRun(Number(slider.value) + 1));\n" +
		"      showRun(options.length - 1);\n" +
		"    })();\n" +
		"  </script>"
}
