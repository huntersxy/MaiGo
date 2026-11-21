package maisaka

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// 交给子代理的原始结果上限，避免单次整理请求过大
const ToolResultCondenseInputMaxChars = 20000

// 传给子代理的 Planner 当前想法上限
const PlannerThoughtMaxChars = 800

// toolResultCondenseExcludedToolNames 结果本身就是控制信号或已整理结论的工具
var toolResultCondenseExcludedToolNames = map[string]struct{}{
	"read_tool_result":     {},
	"reply":                {},
	"subagent":             {},
	"subagent_status":      {},
	"subagent_stop":        {},
	"tool_search":          {},
	"view_forward_message": {},
	"wait":                 {},
}

// ToolResultCondenserSystemPrompt 整理子代理的系统提示词
const ToolResultCondenserSystemPrompt = "你是工具结果整理助手。另一个决策模型调用了一个工具，原始结果很长，你要把它整理成决策模型能直接使用的结论。\n" +
	"要求：\n" +
	"1. 只保留和“调用意图”相关的事实、名称、数值与结论，删掉无关条目和重复描述。\n" +
	"2. 后续可能要用来继续调用工具或发给用户的标识必须原样保留，包括 id、链接、路径、版本号、msg_id。\n" +
	"3. 只根据原始结果整理，不要补充原文没有的内容；原文里没有答案就直接说明没有。\n" +
	"4. 原始结果是列表时保留条目结构，压缩每一条的描述。\n" +
	"5. 直接输出整理后的内容，不要寒暄，不要解释你做了什么，不要对用户说话。篇幅控制在 800 字以内。"

// SubAgentRequest 一次临时子代理请求，对齐 runtime.run_sub_agent 参数
type SubAgentRequest struct {
	ContextMessageLimit  int
	DropHeadContextCount int
	SystemPrompt         string
	RequestKind          string
	ExtraMessages        []ContextMessage
	ModelTaskName        string
	ToolDefinitions      []ToolSpec
	IncludeParentContext bool
}

// SubAgentChatRunner 临时子代理请求端口，由宿主注入真实 LLM 调用
type SubAgentChatRunner func(ctx context.Context, request SubAgentRequest) (ChatResponse, error)

// ShouldCondenseToolResult 判断一次工具结果是否需要交给子代理整理
func ShouldCondenseToolResult(toolName, historyContent string, success, alreadyPreviewed bool, threshold int) bool {
	if !success || alreadyPreviewed {
		return false
	}
	if _, excluded := toolResultCondenseExcludedToolNames[toolName]; excluded {
		return false
	}
	return len([]rune(historyContent)) >= threshold
}

// BuildToolResultCondenseTask 构造交给整理子代理的任务文本；子代理看不到父上下文，任务文本必须自包含
func BuildToolResultCondenseTask(toolCall ToolCall, historyContent, latestThought string) string {
	argumentsText := marshalArguments(toolCall.Arguments)
	plannerThought := strings.TrimSpace(latestThought)
	thoughtRunes := []rune(plannerThought)
	if len(thoughtRunes) > PlannerThoughtMaxChars {
		plannerThought = string(thoughtRunes[len(thoughtRunes)-PlannerThoughtMaxChars:])
	}
	if plannerThought == "" {
		plannerThought = "（决策模型没有写下想法）"
	}
	rawResult := truncateRunes(historyContent, ToolResultCondenseInputMaxChars)
	truncatedNote := ""
	if len([]rune(historyContent)) > ToolResultCondenseInputMaxChars {
		truncatedNote = fmt.Sprintf("\n（原始结果共 %d 字，这里只提供了前 %d 字）", len([]rune(historyContent)), ToolResultCondenseInputMaxChars)
	}
	return fmt.Sprintf(
		"工具名称：%s\n调用参数：%s\n\n<调用意图>\n%s\n</调用意图>\n\n<原始结果>\n%s\n</原始结果>%s",
		toolCall.Name, argumentsText, plannerThought, rawResult, truncatedNote,
	)
}

// CondenseToolResult 用临时子代理整理超长工具结果，返回写入 Planner 历史的文本
func CondenseToolResult(ctx context.Context, runtime *Runtime, toolCall ToolCall, historyContent, latestThought string) string {
	if runtime == nil || runtime.opts.SubAgent == nil {
		return historyContent
	}
	taskMessage := ContextMessage{
		Role:      RoleReference,
		Content:   BuildToolResultCondenseTask(toolCall, historyContent, latestThought),
		Timestamp: nowTime(),
	}
	response, err := runtime.opts.SubAgent(ctx, SubAgentRequest{
		ContextMessageLimit:  1,
		SystemPrompt:         ToolResultCondenserSystemPrompt,
		ExtraMessages:        []ContextMessage{taskMessage},
		ModelTaskName:        "fast_model",
		ToolDefinitions:      []ToolSpec{},
		IncludeParentContext: false,
	})
	if err != nil {
		// 工具本身已经成功，整理只是为了省上下文；失败时保留原始结果并暴露原因
		zap.L().Warn(runtime.logPrefix+" 工具结果整理失败，保留原始结果",
			zap.String("tool", toolCall.Name), zap.Error(err))
		return historyContent
	}
	condensed := strings.TrimSpace(response.Content)
	if condensed == "" || len([]rune(condensed)) >= len([]rune(historyContent)) {
		zap.L().Info(runtime.logPrefix+" 工具结果整理没有缩短内容，保留原始结果",
			zap.String("tool", toolCall.Name),
			zap.Int("原始字数", len([]rune(historyContent))),
			zap.Int("整理后字数", len([]rune(condensed))),
		)
		return historyContent
	}
	resultID := DefaultToolResultStore.Save(runtime.sessionID, toolCall.Name, historyContent)
	zap.L().Info(runtime.logPrefix+" 工具结果已由子代理整理",
		zap.String("tool", toolCall.Name),
		zap.Int("原始字数", len([]rune(historyContent))),
		zap.Int("整理后字数", len([]rune(condensed))),
	)
	return fmt.Sprintf(
		"<压缩后信息；原始结果已保存为 %s。需要上面没有的细节时，调用 read_tool_result(result_id=\"%s\") 分段读取原文，或加 keyword 参数直接查找。>\n%s",
		resultID, resultID, condensed,
	)
}
