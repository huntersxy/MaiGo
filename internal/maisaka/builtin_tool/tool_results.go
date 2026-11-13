package tools

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultToolResultPreviewRunes = 4000
	maxToolResultStoredRunes      = 500_000
)

// StoredToolResultPreview 大工具结果写入内容寻址文件后的预览元数据
type StoredToolResultPreview struct {
	Text       string
	ResultRef  string
	Truncated  bool
	TotalRunes int
	NextOffset int
}

// PreviewStoredToolResult 对齐 MaiBot web 工具：超长正文落盘，模型侧只保留预览和 result_ref
func PreviewStoredToolResult(ctx context.Context, full string, previewRunes int) (StoredToolResultPreview, error) {
	if previewRunes <= 0 {
		previewRunes = defaultToolResultPreviewRunes
	}
	runes := []rune(full)
	out := StoredToolResultPreview{Text: full, TotalRunes: len(runes)}
	if len(runes) <= previewRunes {
		return out, nil
	}
	if len(runes) > maxToolResultStoredRunes {
		return StoredToolResultPreview{}, fmt.Errorf("工具结果超过 %d 字符的存储限制，请缩小查询范围", maxToolResultStoredRunes)
	}
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil {
		out.Text = truncateRunes(full, previewRunes)
		out.Truncated = true
		out.NextOffset = len([]rune(out.Text))
		return out, nil
	}
	ref, err := tc.MemoryMgr.SaveToolResultText(ctx, full)
	if err != nil {
		return StoredToolResultPreview{}, err
	}
	preview := truncateRunes(full, previewRunes)
	preview += fmt.Sprintf("\n\n[正文已保存为 %s，使用 readToolResult(result_ref=%q) 继续阅读]", ref, ref)
	out.Text = preview
	out.ResultRef = ref
	out.Truncated = true
	out.NextOffset = previewRunes
	return out, nil
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

type ReadToolResultInput struct {
	ResultRef string `json:"result_ref"`
	Offset    int    `json:"offset,omitempty"`
	Length    int    `json:"length,omitempty" jsonschema:"description=默认4000字符，最大8000"`
}

func readToolResult(ctx context.Context, input *ReadToolResultInput) (any, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil {
		return nil, NewTerminalToolError(fmt.Errorf("工具结果读取能力未初始化"))
	}
	if input == nil || input.ResultRef == "" {
		return nil, fmt.Errorf("result_ref 不能为空")
	}
	content, total, complete, err := tc.MemoryMgr.ReadToolResultText(ctx, input.ResultRef, input.Offset, input.Length)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "result_ref": input.ResultRef, "content": content, "complete": complete, "total": total, "next_offset": input.Offset + len([]rune(content))}, nil
}

func NewReadToolResultTool() (tool.InvokableTool, error) {
	return utils.InferTool("readToolResult", "读取已经保存的大型工具结果，按字符窗口分页；不要用它重新执行原工具。", readToolResult)
}
