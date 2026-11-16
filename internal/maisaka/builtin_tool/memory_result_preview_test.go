package tools

// 记忆工具预览与原文回查的局部回归测试

import (
	"regexp"
	"strings"
	"testing"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/maisaka"
)

func TestBuildMemoryResultPreviewSavesFullContent(t *testing.T) {
	long := strings.Repeat("长", 3000)
	hits := []memorix.Hit{
		{Content: long},
		{Content: long},
		{Content: long},
	}
	content := "1. " + long + "\n2. " + long + "\n3. " + long
	sessionID := "preview-test-session"
	notice := "提示：人物定向检索未命中，已自动降级为关键词检索。"
	preview := buildMemoryResultPreview(memorix.SearchResult{Hits: hits}, content, sessionID, 3, notice)

	if !strings.HasPrefix(preview, notice+"\n以下为记忆片段预览，共 3 条；") {
		t.Fatalf("预览缺少命中数或降级提示: %.120s", preview)
	}
	if count := len([]rune(preview)); count > memoryResultMaxChars {
		t.Fatalf("预览长度超过上限: %d", count)
	}
	matches := regexp.MustCompile(`read_tool_result\(result_id="([^"]+)"\)`).FindStringSubmatch(preview)
	if len(matches) != 2 {
		t.Fatalf("预览缺少 read_tool_result 编号: %.200s", preview)
	}
	stored, ok := maisaka.DefaultToolResultStore.Get(sessionID, matches[1])
	if !ok {
		t.Fatalf("完整原文未暂存: result_id=%s", matches[1])
	}
	if stored.Content != notice+"\n"+content {
		t.Fatalf("暂存正文与完整原文不一致: stored=%d want=%d", len(stored.Content), len(notice+"\n"+content))
	}

	shortContent := "短正文"
	if got := buildMemoryResultPreview(memorix.SearchResult{Hits: hits}, shortContent, sessionID, 3, notice); got != notice+"\n"+shortContent {
		t.Fatalf("短正文应保留降级提示: %q", got)
	}
	if got := buildMemoryResultPreview(memorix.SearchResult{Hits: hits}, shortContent, sessionID, 3, ""); got != shortContent {
		t.Fatalf("短正文不应改写: %q", got)
	}
}
