// 聚合查询结果文本渲染，对齐 A_memorix/core/utils/aggregate_query_service.py 的 _build_content

package utils

import (
	"fmt"
	"sort"
	"strings"
)

// BuildAggregateContent 对齐 _build_content
func (s *AggregateQueryService) BuildAggregateContent(query string, branches map[string]map[string]any, errors []map[string]any, mixedResults []map[string]any) string {
	displayQuery := query
	if displayQuery == "" {
		displayQuery = "N/A"
	}
	lines := []string{
		fmt.Sprintf("🔀 聚合查询结果（query='%s'）", displayQuery),
		"",
		"分支状态：",
	}
	for _, name := range []string{"search", "time", "episode"} {
		branch := branches[name]
		status := AggregateStatus(branch)
		count := aggregateAsInt(branch["count"], 0)
		line := fmt.Sprintf("- %s: %s, count=%d", name, status, count)
		reason := aggregateString(branch["skip_reason"])
		errText := aggregateString(branch["error"])
		if status == "skipped" && reason != "" {
			line += " (" + reason + ")"
		}
		if status == "failed" && errText != "" {
			line += " (" + errText + ")"
		}
		lines = append(lines, line)
	}
	if len(errors) > 0 {
		lines = append(lines, "", "错误：")
		limit := len(errors)
		if limit > 6 {
			limit = 6
		}
		for _, item := range errors[:limit] {
			// 对齐 Python dict.get(key, default)：键存在但为空串时原样输出空串
			branchName := "unknown"
			if raw, exists := item["branch"]; exists {
				branchName = aggregateString(raw)
			}
			errText := "unknown error"
			if raw, exists := item["error"]; exists {
				errText = aggregateString(raw)
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", branchName, errText))
		}
	}
	if mixedResults != nil {
		lines = append(lines, "", fmt.Sprintf("🧩 混合结果（%d 条）：", len(mixedResults)))
		limit := len(mixedResults)
		if limit > 5 {
			limit = 5
		}
		for index, item := range mixedResults[:limit] {
			sources := aggregateSourceBranches(item)
			sourceText := strings.Join(sources, ",")
			if aggregateString(item["type"]) == "episode" {
				title := aggregateString(item["title"])
				if title == "" {
					title = "Untitled"
				}
				lines = append(lines, fmt.Sprintf("%d. 🧠 %s [%s]", index+1, title, sourceText))
			} else {
				text := aggregateRawString(item["content"])
				runes := []rune(text)
				if len(runes) > 80 {
					text = string(runes[:80]) + "..."
				}
				lines = append(lines, fmt.Sprintf("%d. %s [%s]", index+1, text, sourceText))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func aggregateSourceBranches(item map[string]any) []string {
	raw, exists := item["source_branches"]
	if !exists {
		return []string{}
	}
	switch values := raw.(type) {
	case []string:
		out := append([]string{}, values...)
		sort.Strings(out)
		return out
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, aggregateString(value))
		}
		sort.Strings(out)
		return out
	case map[string]struct{}:
		out := make([]string, 0, len(values))
		for value := range values {
			out = append(out, value)
		}
		sort.Strings(out)
		return out
	}
	return []string{}
}
