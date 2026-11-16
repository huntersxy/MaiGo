package runtime

// Maisaka 多轮子代理执行器：在独立上下文里调用被授权的工具，只把最终结论交还 Planner

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/maisaka"
)

const (
	subagentToolResultMaxChars = 20000
	subagentRunnerMaxRounds    = 5
	subagentRunnerSystemPrompt = "你是一个被派发任务的子代理。派发者看不到你的工作过程，只会收到你最后一条不带工具调用的文字。\n" +
		"工作方式：\n" +
		"1. 任务说明就是你知道的全部背景，不要假设任何没有写出来的信息。\n" +
		"2. 用提供的工具查清楚需要的事实；互不依赖的调用可以一次并行发出。\n" +
		"3. 工具返回无结果或失败时换一种查法，确实查不到就如实说明，不要编造。\n" +
		"4. 查清后直接输出结论：先给答案，再列出支撑它的关键事实；派发者后续可能用到的 id、链接、路径、版本号原样保留。\n" +
		"5. 你不能向任何人发言，也不能把任务再派发出去。结论控制在 800 字以内。"
	subagentFinalRoundHint = "可用的轮数已经用完，这一轮不能再调用工具。请根据已经拿到的结果直接给出结论，没有查清的部分如实说明。"
)

var subagentRunnerForbidden = map[string]struct{}{
	"subagent":        {},
	"subagent_status": {},
	"subagent_stop":   {},
	"react_message":   {},
	"reply":           {},
	"tool_search":     {},
	"wait":            {},
	"stop":            {},
}

// runMaisakaSubagent 在独立上下文里执行多轮工具调用，返回最终结论与工具使用统计
func (a *Agent) runMaisakaSubagent(ctx context.Context, kind string, targetID int64, task string, grantedTools []string, maxRounds int) (string, map[string]int, error) {
	if a == nil {
		return "", nil, fmt.Errorf("子代理执行器缺少 Agent")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return "", nil, fmt.Errorf("子代理任务说明为空")
	}
	generation := a.acquireGeneration()
	if generation == nil {
		return "", nil, fmt.Errorf("子代理模型未就绪")
	}
	defer generation.wg.Done()
	model := generation.plannerModel
	if model == nil {
		model = generation.model
	}
	if model == nil {
		return "", nil, fmt.Errorf("子代理模型未就绪")
	}
	if maxRounds <= 0 {
		maxRounds = subagentRunnerMaxRounds
	}
	granted := make(map[string]struct{}, len(grantedTools))
	for _, name := range grantedTools {
		name = strings.TrimSpace(name)
		if _, forbidden := subagentRunnerForbidden[name]; forbidden {
			continue
		}
		if name != "" {
			granted[name] = struct{}{}
		}
	}
	available := make(map[string]bool, len(granted))
	specs := make([]maisaka.ToolSpec, 0, len(granted))
	for _, base := range a.generationTools(kind) {
		if base == nil {
			continue
		}
		info, err := base.Info(ctx)
		if err != nil || info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		if _, ok := granted[info.Name]; !ok || available[info.Name] {
			continue
		}
		spec := maisaka.AnnotateBuiltinToolSpec(maisaka.ToolSpec{Name: info.Name, Description: info.Desc})
		if maisaka.GetBuiltinToolVisibility(spec) == maisaka.BuiltinHidden {
			continue
		}
		if info.ParamsOneOf != nil {
			params, err := jsonSchemaToMap(info.ParamsOneOf)
			if err != nil {
				return "", nil, fmt.Errorf("读取工具 %s 参数结构失败: %w", info.Name, err)
			}
			spec.Parameters = params
		}
		specs = append(specs, spec)
		available[info.Name] = true
	}
	if len(specs) == 0 {
		return "", nil, fmt.Errorf("没有可用的授权工具")
	}
	client := &maisakaChatClient{model: model}
	sessionID := a.resolveMaiBotSessionID(ctx, kind, targetID)
	items := []maisaka.ContextItem{
		{Role: maisaka.RoleSystem, Text: subagentRunnerSystemPrompt},
		{Role: maisaka.RoleUser, Text: task},
	}
	usage := make(map[string]int)
	for round := 0; round <= maxRounds; round++ {
		finalRound := round == maxRounds
		requestItems := items
		if finalRound {
			requestItems = append(append([]maisaka.ContextItem(nil), items...), maisaka.ContextItem{Role: maisaka.RoleSystem, Text: subagentFinalRoundHint})
		}
		requestTools := specs
		if finalRound {
			requestTools = nil
		}
		response, err := client.Chat(ctx, maisaka.ChatRequest{
			RequestKind: "sub_agent",
			Items:       requestItems,
			Tools:       requestTools,
			SessionID:   sessionID,
		})
		if err != nil {
			return "", usage, err
		}
		if response.Content != "" || finalRound || len(response.ToolCalls) == 0 {
			if finalRound || len(response.ToolCalls) == 0 {
				return strings.TrimSpace(response.Content), usage, nil
			}
		}
		items = append(items, maisaka.ContextItem{
			Role:      maisaka.RoleAssistant,
			Text:      response.Content,
			ToolCalls: response.ToolCalls,
		})
		runtime := &AgentToolRuntime{Agent: a, Kind: kind, TargetID: targetID}
		for _, call := range response.ToolCalls {
			content := ""
			if _, forbidden := subagentRunnerForbidden[call.Name]; forbidden {
				content = fmt.Sprintf("工具 %s 没有授权给你。", call.Name)
			} else if _, ok := granted[call.Name]; !ok {
				content = fmt.Sprintf("工具 %s 没有授权给你。", call.Name)
			} else {
				usage[call.Name]++
				result, invokeErr := runtime.Invoke(ctx, maisaka.ToolInvocation{
					ToolName:  call.Name,
					Arguments: call.Arguments,
					CallID:    call.CallID,
				})
				if invokeErr != nil {
					content = invokeErr.Error()
				} else {
					content = firstNonEmptyString(result.Content, result.Message)
					if strings.TrimSpace(content) == "" {
						if result.Success {
							content = "工具执行成功。"
						} else {
							content = fmt.Sprintf("工具 %s 执行失败。", call.Name)
						}
					}
				}
			}
			content = truncateSubagentText(content, subagentToolResultMaxChars)
			items = append(items, maisaka.ContextItem{
				Role:       maisaka.RoleTool,
				Text:       content,
				ToolCallID: call.CallID,
				ToolName:   call.Name,
			})
		}
	}
	return "", usage, fmt.Errorf("子代理没有给出结论")
}

func truncateSubagentText(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars])
}
