package maisaka

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"MaiGo/internal/core/tooling"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.uber.org/zap"
)

// PlannerActionJudgeSource 动作判断器补全的工具调用来源标记
const PlannerActionJudgeSource = "action_judge"

const defaultPlannerActionJudgePrompt = `根据 recent_chat 和 planner_text，必须且只能调用一次 reply 或 wait 工具。不要输出动作 JSON、解释或回复正文。`

// PlannerActionJudgment 判断结果和独立请求开销
type PlannerActionJudgment struct {
	ToolCall ToolCall
	Metadata map[string]any
}

// BuildRecentChatContext 只取最近五条真实聊天消息，排除通知、工具和内部上下文
func BuildRecentChatContext(chatHistory []ContextMessage) []map[string]string {
	recent := make([]map[string]string, 0, 5)
	for index := len(chatHistory) - 1; index >= 0; index-- {
		message := chatHistory[index]
		if !isSessionBackedHistoryMessage(message) || message.MetadataBool("is_notify") {
			continue
		}
		text := strings.TrimSpace(message.VisibleText)
		if text == "" {
			text = strings.TrimSpace(message.Content)
		}
		if text == "" && len(message.Images) > 0 {
			text = imagePlaceholderText(message.Images)
		}
		if text == "" && len(message.Emojis) > 0 {
			text = emojiPlaceholderText(message.Emojis, false)
		}
		recent = append(recent, map[string]string{
			"session_id": message.SessionID,
			"text":       text,
		})
		if len(recent) == 5 {
			break
		}
	}
	for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
		recent[left], recent[right] = recent[right], recent[left]
	}
	return recent
}

// CompleteMissingPlannerAction 仅对有正文、无工具调用的响应做一次短分类，不重跑 Planner
func (s *ChatLoopService) CompleteMissingPlannerAction(
	ctx context.Context,
	response ChatResponse,
	tools []ToolSpec,
	chatHistory []ContextMessage,
) (*PlannerActionJudgment, error) {
	if s == nil || s.JudgeClient == nil {
		return nil, nil
	}
	if !s.FocusMode && s.PassiveReplyer() {
		return nil, nil
	}
	content := strings.TrimSpace(response.Content)
	if content == "" || len(response.ToolCalls) > 0 || len(response.NativeToolCalls) > 0 {
		return nil, nil
	}
	replySpec, hasReply := findToolSpec(tools, "reply")
	waitSpec, hasWait := findToolSpec(tools, "wait")
	if !hasReply || !hasWait {
		return nil, nil
	}
	replyRoutes := toolSessionRoutes(replySpec)
	waitRoutes := toolSessionRoutes(waitSpec)
	routes := make([]string, 0, len(replyRoutes))
	waitSet := make(map[string]struct{}, len(waitRoutes))
	for _, sessionID := range waitRoutes {
		waitSet[sessionID] = struct{}{}
	}
	for _, sessionID := range replyRoutes {
		if _, ok := waitSet[sessionID]; ok {
			routes = append(routes, sessionID)
		}
	}
	if (len(replyRoutes) > 0 || len(waitRoutes) > 0) && len(routes) == 0 {
		return nil, fmt.Errorf("Planner 动作判断没有同时允许 reply 和 wait 的真实会话")
	}

	payload := map[string]any{
		"recent_chat":  BuildRecentChatContext(chatHistory),
		"planner_text": content,
	}
	if len(routes) > 0 {
		payload["reply_sessions"] = routes
	} else {
		for _, message := range payload["recent_chat"].([]map[string]string) {
			delete(message, "session_id")
		}
	}
	payloadText, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("Planner 动作判断输入序列化失败: %w", err)
	}
	systemPrompt := s.localizedText("planner_action_judge")
	if systemPrompt == "" {
		systemPrompt = defaultPlannerActionJudgePrompt
	}
	if len(routes) > 0 {
		systemPrompt += "\n" + s.localizedText("planner_action_judge_focus")
	}
	items := []ContextItem{
		{Role: RoleSystem, Text: systemPrompt},
		{Role: RoleUser, Text: string(payloadText)},
	}
	logicalTurnID := strings.TrimSpace(s.LogicalTurnID)
	if logicalTurnID == "" {
		logicalTurnID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	cold := float32(0)
	request := ChatRequest{
		RequestKind:   "maisaka.planner_action_judge",
		ModelTaskName: "fast_model",
		Items:         items,
		Tools:         []ToolSpec{replySpec, waitSpec},
		Temperature:   &cold,
		InterruptFlag: s.InterruptFlag,
		SessionID:     s.SessionID,
		LogicalTurnID: logicalTurnID,
	}
	started := time.Now()
	result, err := s.JudgeClient.Chat(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("Planner 动作判断请求失败: %w", err)
	}
	durationMS := math.Round(float64(time.Since(started).Microseconds())/10) / 100
	if len(result.ToolCalls) != 1 || len(result.NativeToolCalls) != 0 {
		return nil, fmt.Errorf("Planner 动作判断器必须返回且仅返回一个 reply 或 wait 工具调用")
	}
	selected := result.ToolCalls[0]
	action, arguments := selected.Name, selected.Arguments
	if action != "reply" && action != "wait" {
		return nil, fmt.Errorf("Planner 动作判断器返回了无效工具：%q", action)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	spec := waitSpec
	if action == "reply" {
		spec = replySpec
	}
	if target, _ := arguments["session_id"].(string); len(routes) > 0 && !containsString(routes, target) {
		return nil, fmt.Errorf("Planner 动作判断器返回了不可用的会话：%q", target)
	}
	if err := validatePlannerActionArguments(arguments, spec.Parameters); err != nil {
		return nil, fmt.Errorf("Planner 动作判断器的 %s 参数不符合工具声明: %w", action, err)
	}
	call := ToolCall{
		CallID:       "call_action_judge_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Name:         action,
		Arguments:    arguments,
		ExtraContent: map[string]any{"tool_call_source": PlannerActionJudgeSource, "tool_call_source_label": "轻量判断器补全"},
	}
	metadata := map[string]any{
		"action":            action,
		"model_name":        result.ModelName,
		"duration_ms":       durationMS,
		"prompt_tokens":     result.Usage.PromptTokens,
		"completion_tokens": result.Usage.CompletionTokens,
	}
	zap.L().Info("Planner 正文动作补全",
		zap.String("session_id", s.SessionID),
		zap.String("action", action),
		zap.String("call_id", call.CallID),
		zap.String("model", result.ModelName),
		zap.Float64("duration_ms", durationMS))
	return &PlannerActionJudgment{ToolCall: call, Metadata: metadata}, nil
}

func validatePlannerActionArguments(arguments, parameters map[string]any) error {
	if !reflect.DeepEqual(tooling.FilterToolArguments(arguments, parameters), arguments) {
		return fmt.Errorf("包含未声明的参数")
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return err
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return err
	}
	if len(schema) == 0 {
		schema = map[string]any{"type": "object"}
	}
	compiler := jsonschema.NewCompiler()
	const resource = "https://maibot.local/planner-action.json"
	if err := compiler.AddResource(resource, schema); err != nil {
		return err
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return err
	}
	return compiled.Validate(arguments)
}

func findToolSpec(tools []ToolSpec, name string) (ToolSpec, bool) {
	for _, spec := range tools {
		if strings.TrimSpace(spec.Name) == name {
			return spec, true
		}
	}
	return ToolSpec{}, false
}

func toolSessionRoutes(spec ToolSpec) []string {
	properties, _ := spec.Parameters["properties"].(map[string]any)
	sessionSchema, _ := properties["session_id"].(map[string]any)
	raw, ok := sessionSchema["enum"]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
