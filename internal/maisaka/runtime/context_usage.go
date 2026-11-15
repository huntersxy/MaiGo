package runtime

import (
	maisaka "MaiGo/internal/maisaka"
	maisakactx "MaiGo/internal/maisaka/context"
)

// measurePlannerSections 按 Python measure_request_sections 统计 Planner 请求的提示词分段用量
//
// 工具定义按 OpenAI function 结构测量，来源类型取自 ToolSpec.ProviderType；
// 历史条数以请求构建时的边界为准，hook 改写后按实际长度收敛。
func (a *Agent) measurePlannerSections(items []maisaka.ContextItem, historyItemCount int, tools []maisaka.ToolSpec) []maisaka.ContextSectionUsage {
	definitions := make([]map[string]any, 0, len(tools))
	providerTypes := make([]string, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, tool.ToLLMDefinition())
		providerTypes = append(providerTypes, tool.ProviderType)
	}
	usages := maisakactx.MeasureRequestSections(items, historyItemCount, definitions, providerTypes)
	out := make([]maisaka.ContextSectionUsage, 0, len(usages))
	for _, usage := range usages {
		out = append(out, maisaka.ContextSectionUsage{Key: usage.Key, Chars: usage.Chars, Count: usage.Count})
	}
	return out
}
