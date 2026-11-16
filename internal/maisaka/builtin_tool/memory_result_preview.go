package tools

// 限制记忆工具首次返回的正文，原文沿用工具结果存储按需读取。

import (
	"fmt"
	"strings"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/maisaka"
)

// memoryResultMaxChars 对齐 MEMORY_RESULT_MAX_CHARS
const memoryResultMaxChars = 2800

// buildMemoryResultPreview 保留各命中的短正文，并提供同会话内完整原文的读取编号
func buildMemoryResultPreview(result memorix.SearchResult, content string, sessionID string, limit int, notice string) string {
	fullContent := content
	if notice != "" {
		fullContent = notice + "\n" + content
	}
	if len([]rune(fullContent)) <= memoryResultMaxChars {
		return fullContent
	}
	resultID := maisaka.DefaultToolResultStore.Save(sessionID, "query_memory", fullContent)
	count := len(result.Hits)
	if limit < 1 {
		limit = 1
	}
	if limit < count {
		count = limit
	}
	header := ""
	if notice != "" {
		header += notice + "\n"
	}
	header += fmt.Sprintf(
		"以下为记忆片段预览，共 %d 条；片段被截短，不代表完整事实。\n"+
			"完整原文已保存为 %s，需要细节时调用 read_tool_result(result_id=\"%s\")，可加 keyword 查找或用 offset 分段读取。\n",
		count, resultID, resultID,
	)
	budget := memoryResultMaxChars - len([]rune(header))
	preview := content
	if count > 0 {
		// 给每条命中分配正文预算，避免首条长记忆挤掉其余命中。
		perHit := budget/count - 16
		if perHit < 1 {
			perHit = 1
		}
		preview = renderQueryMemoryHitsTruncated(result.Hits, limit, perHit)
	}
	previewRunes := []rune(preview)
	if budget >= 0 && len(previewRunes) > budget {
		preview = string(previewRunes[:budget])
	}
	return header + preview
}

// renderQueryMemoryHitsTruncated 对齐 MemorySearchResult.to_text(max_content_chars=...)
func renderQueryMemoryHitsTruncated(hits []memorix.Hit, limit int, maxContentChars int) string {
	if len(hits) == 0 {
		return ""
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(hits) {
		limit = len(hits)
	}
	lines := make([]string, 0, limit)
	for index, hit := range hits[:limit] {
		content := strings.TrimSpace(hit.Content)
		content = strings.ReplaceAll(content, "\n", " ")
		runes := []rune(content)
		if maxContentChars > 0 && len(runes) > maxContentChars {
			content = string(runes[:maxContentChars]) + "..."
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, content))
	}
	return strings.Join(lines, "\n")
}
