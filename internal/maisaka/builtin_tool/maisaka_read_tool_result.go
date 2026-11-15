package tools

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/maisaka"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	readToolResultDefaultMaxChars = 2000
	readToolResultMaxCharsLimit   = 2800
	readToolResultKeywordContext  = 250
	readToolResultKeywordMaxMatch = 5
)

// readToolResultStore 指向 Maisaka 的进程级工具结果暂存表，测试可替换
var readToolResultStore = maisaka.DefaultToolResultStore

// ReadToolResultMaisakaInput 对齐 Python maisaka_builtin.read_tool_result 的参数
type ReadToolResultMaisakaInput struct {
	ResultID string `json:"result_id" jsonschema:"description=工具返回的原始结果编号，例如 r3。"`
	Keyword  string `json:"keyword,omitempty" jsonschema:"description=可选。只返回原文中包含该关键词的片段及其前后文；不填则按 offset 顺序读取。"`
	Offset   int    `json:"offset,omitempty" jsonschema:"description=可选。从原文第几个字开始读，默认 0；上一次读取会告诉你下一段的 offset。"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"description=可选。本次最多读取多少字，默认 2000。"`
}

type readToolResultMaisakaOutput struct {
	Success    bool   `json:"success"`
	ResultID   string `json:"result_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Content    string `json:"content,omitempty"`
	TotalChars int    `json:"total_chars,omitempty"`
	Message    string `json:"message,omitempty"`
}

func readToolResultMaisakaFailure(message string) *readToolResultMaisakaOutput {
	return &readToolResultMaisakaOutput{Success: false, Message: message}
}

func readToolResultMaisakaFunc(ctx context.Context, input *ReadToolResultMaisakaInput) (any, error) {
	if input == nil {
		return readToolResultMaisakaFailure("read_tool_result 需要提供 result_id。"), nil
	}
	resultID := strings.TrimSpace(input.ResultID)
	if resultID == "" {
		return readToolResultMaisakaFailure("read_tool_result 需要提供 result_id。"), nil
	}
	tc := GetToolContext(ctx)
	if tc == nil || strings.TrimSpace(tc.SessionID) == "" {
		return readToolResultMaisakaFailure("read_tool_result 缺少会话上下文，无法读取原始结果。"), nil
	}
	sessionID := strings.TrimSpace(tc.SessionID)
	stored, ok := readToolResultStore.Get(sessionID, resultID)
	if !ok {
		return readToolResultMaisakaFailure(fmt.Sprintf("没有找到编号为 %s 的原始结果：它可能已经过期。需要这部分内容时请重新调用原来的工具。", resultID)), nil
	}
	output := &readToolResultMaisakaOutput{
		Success:  true,
		ResultID: stored.ResultID,
		ToolName: stored.ToolName,
	}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		output.Content, output.TotalChars = readToolResultMaisakaByKeyword(stored, keyword)
		return output, nil
	}
	maxChars := input.MaxChars
	if maxChars <= 0 {
		maxChars = readToolResultDefaultMaxChars
	}
	if maxChars > readToolResultMaxCharsLimit {
		maxChars = readToolResultMaxCharsLimit
	}
	output.Content, output.TotalChars = readToolResultMaisakaByOffset(stored, input.Offset, maxChars)
	return output, nil
}

// readToolResultMaisakaByOffset 按 rune 偏移读取一段原文，头尾文案与 Python 对齐
func readToolResultMaisakaByOffset(stored maisaka.StoredToolResult, offset, maxChars int) (string, int) {
	runes := []rune(stored.Content)
	total := len(runes)
	start := offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + maxChars
	if end > total {
		end = total
	}
	header := fmt.Sprintf("[%s · %s 原始结果 · 共 %d 字 · 本段 %d–%d]", stored.ResultID, stored.ToolName, total, start, end)
	if start >= total {
		return header + "\n（offset 已超出原文末尾，没有更多内容。）", total
	}
	remaining := total - end
	footer := "\n（已到原文末尾。）"
	if remaining > 0 {
		footer = fmt.Sprintf("\n（后面还有 %d 字，用 offset=%d 继续读取。）", remaining, end)
	}
	return header + "\n" + string(runes[start:end]) + footer, total
}

// readToolResultMaisakaByKeyword 返回关键词命中片段，命中总数与片段截断提示保持 Python 语义
func readToolResultMaisakaByKeyword(stored maisaka.StoredToolResult, keyword string) (string, int) {
	contentRunes := []rune(stored.Content)
	keywordRunes := []rune(keyword)
	total := len(contentRunes)
	totalMatches := 0
	searchFrom := 0
	lastSnippetEnd := -1
	var snippets []string
	for len(keywordRunes) > 0 && searchFrom+len(keywordRunes) <= total {
		if !strings.EqualFold(string(contentRunes[searchFrom:searchFrom+len(keywordRunes)]), keyword) {
			searchFrom++
			continue
		}
		matchIndex := searchFrom
		totalMatches++
		searchFrom = matchIndex + len(keywordRunes)
		if matchIndex < lastSnippetEnd || len(snippets) >= readToolResultKeywordMaxMatch {
			continue
		}
		start := matchIndex - readToolResultKeywordContext
		if start < 0 {
			start = 0
		}
		end := matchIndex + len(keywordRunes) + readToolResultKeywordContext
		if end > total {
			end = total
		}
		lastSnippetEnd = end
		snippets = append(snippets, fmt.Sprintf("--- 位置 %d–%d ---\n%s", start, end, string(contentRunes[start:end])))
	}
	header := fmt.Sprintf("[%s · %s 原始结果 · 共 %d 字 · 关键词“%s”命中 %d 处]", stored.ResultID, stored.ToolName, total, keyword, totalMatches)
	if len(snippets) == 0 {
		return header + "\n（原文中没有这个关键词，可以换一个词，或不填 keyword 按 offset 顺序读取。）", total
	}
	footer := ""
	if totalMatches > len(snippets) {
		footer = fmt.Sprintf("\n（只显示了前 %d 个片段；可以用 offset 读取指定位置附近的原文。）", len(snippets))
	}
	return header + "\n" + strings.Join(snippets, "\n") + footer, total
}

// NewReadToolResultMaisakaTool 声明 read_tool_result，参数与 Python 内置工具一致
func NewReadToolResultMaisakaTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"read_tool_result",
		"取回某次工具调用保存的原始结果。联网工具预览或超长结果的整理结论会附有编号（如 r3）；当你需要其中没有的细节时，用这个编号分段读取原文，或用 keyword 直接查找相关片段。已有内容够用时不需要调用。",
		readToolResultMaisakaFunc,
	)
}
