package host

import (
	"context"
	"fmt"
	"strings"
)

// CommandExecutionContext 一次命令执行的会话上下文，对齐 component_query._build_command_executor 的 args
type CommandExecutionContext struct {
	Text            string
	SessionID       string
	GroupID         string
	Platform        string
	UserID          string
	IsLocalOperator bool
	MatchedGroups   map[string]string
	Message         map[string]any
	PluginConfig    map[string]any
}

// CommandExecutionResult 命令执行结果，对齐 Python 的 (success, response, intercept_message_level)
type CommandExecutionResult struct {
	CommandName           string
	PluginID              string
	Success               bool
	Response              string
	InterceptMessageLevel int
}

// ExecuteCommand 调用 Runner 的 plugin.invoke_command，对齐 component_query._build_command_executor
func (h *Host) ExecuteCommand(ctx context.Context, command *ComponentEntry, execCtx CommandExecutionContext) CommandExecutionResult {
	result := CommandExecutionResult{
		InterceptMessageLevel: 1,
	}
	if command == nil {
		result.Response = "命令不存在"
		return result
	}
	result.CommandName = command.Name
	result.PluginID = command.PluginID
	if command.ComponentType != ComponentTypeCommand {
		result.Response = "组件不是命令"
		return result
	}
	result.InterceptMessageLevel = 0
	if command.InterceptMessageLevel != 0 {
		result.InterceptMessageLevel = 1
	}
	if command.localHandler != nil {
		result = command.localHandler(ctx, execCtx)
		result.CommandName = command.Name
		result.PluginID = command.PluginID
		return result
	}
	args := map[string]any{
		"text":              execCtx.Text,
		"stream_id":         execCtx.SessionID,
		"group_id":          execCtx.GroupID,
		"platform":          execCtx.Platform,
		"user_id":           execCtx.UserID,
		"is_local_operator": execCtx.IsLocalOperator,
		"matched_groups":    stringMapToAny(execCtx.MatchedGroups),
	}
	if execCtx.Message != nil {
		args["message"] = execCtx.Message
	}
	if execCtx.PluginConfig != nil {
		args["plugin_config"] = execCtx.PluginConfig
	}
	resp, err := h.InvokePlugin(ctx, "plugin.invoke_command", command.PluginID, command.Name, args, command.TimeoutMS)
	if err != nil {
		result.Response = err.Error()
		result.InterceptMessageLevel = 1
		return result
	}
	if resp.Error != nil {
		result.Response = envelopeErrorMessage(resp.Error, "命令执行失败")
		result.InterceptMessageLevel = 1
		return result
	}
	result.Success = payloadBool(resp.Payload, "success", false)
	rawResult := resp.Payload["result"]
	if list, ok := rawResult.([]any); ok && len(list) >= 3 {
		result.Response = commandResultText(list[1])
		if truthyPayloadValue(list[2]) {
			result.InterceptMessageLevel = 1
		} else {
			result.InterceptMessageLevel = 0
		}
		return result
	}
	result.Response = commandResultText(rawResult)
	return result
}

func commandResultText(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringMapToAny(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
