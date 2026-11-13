package native

// 记忆修正计划生成器，调用宿主文本网关并解析 JSON 计划

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"maibot-go/internal/A_memorix/core/utils"
)

// correctionPlanner 使用提示词与文本网关生成修正计划
type correctionPlanner struct {
	runtime *Runtime
}

// BuildFuzzyModifyPlan 对齐 _build_fuzzy_modify_llm_plan
func (p correctionPlanner) BuildFuzzyModifyPlan(ctx context.Context, requestText, scope, personID, chatID string, candidates []any) (map[string]any, error) {
	if p.runtime == nil || p.runtime.llmGateway == nil {
		return nil, fmt.Errorf("记忆修正 LLM 未接入，无法生成修改计划")
	}
	payload := map[string]any{
		"request_text": requestText, "scope": scope, "person_id": personID,
		"chat_id": chatID, "max_targets": 20, "candidates": candidates,
	}
	payloadJSON, err := utils.PyJSONMarshal(payload)
	if err != nil {
		return nil, err
	}
	prompt, err := p.runtime.summaryPrompt().Render("memory_fuzzy_modify_plan", map[string]any{"request_payload": payloadJSON})
	if err != nil {
		return nil, err
	}
	temperature := 0.2
	result, err := p.runtime.llmGateway.Generate(ctx, utils.TextGenerationRequest{
		TaskName: "utils", RequestType: "A_Memorix.fuzzy_modify_plan",
		Prompt: prompt, Temperature: &temperature,
	})
	if err != nil {
		return nil, err
	}
	if !result.Success {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = "记忆修正 LLM 未返回结果"
		}
		return nil, fmt.Errorf("%s", message)
	}
	completion := stripJSONFence(result.Completion)
	var plan map[string]any
	if err := sonic.Unmarshal([]byte(completion), &plan); err != nil {
		return nil, fmt.Errorf("记忆修正计划不是有效 JSON: %w", err)
	}
	if plan == nil {
		return nil, fmt.Errorf("记忆修正计划不是有效的对象")
	}
	return plan, nil
}

// stripJSONFence 去掉模型输出中的 Markdown 代码围栏
func stripJSONFence(value string) string {
	text := strings.TrimSpace(value)
	if strings.HasPrefix(text, "```") {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[index+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	return strings.TrimSpace(text)
}
