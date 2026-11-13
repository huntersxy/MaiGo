package host

import (
	"context"
	"fmt"
	"strings"
)

// methodPluginInvokeAction Host→Runner 调用 legacy Action 的 RPC 方法
const methodPluginInvokeAction = "plugin.invoke_action"

// ActionInfo 供 Planner 使用的 Action 信息，对齐 component_query.ActionInfo
type ActionInfo struct {
	Name                        string            `json:"name"`
	Description                 string            `json:"description"`
	Enabled                     bool              `json:"enabled"`
	PluginName                  string            `json:"plugin_name"`
	ActionParameters            map[string]string `json:"action_parameters"`
	ActionRequire               []string          `json:"action_require"`
	AssociatedTypes             []string          `json:"associated_types"`
	ActivationType              string            `json:"activation_type"`
	RandomActivationProbability float64           `json:"random_activation_probability"`
	ActivationKeywords          []string          `json:"activation_keywords"`
	ParallelAction              bool              `json:"parallel_action"`
}

// ActionExecutionOptions 对齐旧 ActionManager 传入的执行 kwargs
//
// 只有显式提供的可选字段才会写入调用参数，保持与 Python 的 None 语义一致
type ActionExecutionOptions struct {
	ActionData          map[string]any
	StreamID            string
	ChatStreamSessionID string
	Reasoning           string
	ThinkingID          *string
	CycleTimers         map[string]any
	PluginConfig        map[string]any
	LogPrefix           string
	ShuttingDown        *bool
}

// ActionInfos 返回全部 ACTION 信息，按组件全名稳定排序
func (h *Host) ActionInfos() []ActionInfo {
	entries, err := h.actionEntries("")
	if err != nil {
		return nil
	}
	out := make([]ActionInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, buildActionInfo(entry))
	}
	return out
}

// GetActionInfo 按短名查找首个 ACTION 信息，对齐 get_action_info
func (h *Host) GetActionInfo(name string) *ActionInfo {
	entries, err := h.actionEntries(name)
	if err != nil || len(entries) == 0 {
		return nil
	}
	info := buildActionInfo(entries[0])
	return &info
}

// DefaultActions 返回当前启用的 ACTION 集合，对齐 get_default_actions
func (h *Host) DefaultActions() map[string]ActionInfo {
	out := map[string]ActionInfo{}
	for _, info := range h.ActionInfos() {
		if !info.Enabled {
			continue
		}
		if _, exists := out[info.Name]; exists {
			continue
		}
		out[info.Name] = info
	}
	return out
}

// ExecuteAction 调用 legacy Action，返回 (是否成功, 结果说明)
//
// 对齐 component_query._build_action_executor：plugin_id 为空时按动作短名唯一解析插件
func (h *Host) ExecuteAction(ctx context.Context, pluginID, name string, opts ActionExecutionOptions) (bool, string) {
	entries, err := h.actionEntries(strings.TrimSpace(name))
	if err != nil {
		return false, err.Error()
	}
	if len(entries) == 0 {
		return false, fmt.Sprintf("未找到动作: %s", strings.TrimSpace(name))
	}
	entry := entries[0]
	if strings.TrimSpace(pluginID) != "" {
		for _, candidate := range entries {
			if candidate.PluginID == strings.TrimSpace(pluginID) {
				entry = candidate
				break
			}
		}
	}
	args := map[string]any{}
	for key, value := range opts.ActionData {
		args[key] = value
	}
	actionData := map[string]any{}
	for key, value := range opts.ActionData {
		actionData[key] = value
	}
	streamID := strings.TrimSpace(opts.StreamID)
	if streamID == "" {
		streamID = strings.TrimSpace(opts.ChatStreamSessionID)
	}
	args["action_data"] = actionData
	args["stream_id"] = streamID
	args["chat_id"] = streamID
	args["reasoning"] = opts.Reasoning
	if opts.ThinkingID != nil {
		args["thinking_id"] = *opts.ThinkingID
	}
	if opts.CycleTimers != nil {
		args["cycle_timers"] = opts.CycleTimers
	}
	if opts.PluginConfig != nil {
		args["plugin_config"] = opts.PluginConfig
	}
	if opts.LogPrefix != "" {
		args["log_prefix"] = opts.LogPrefix
	}
	if opts.ShuttingDown != nil {
		args["shutting_down"] = *opts.ShuttingDown
	}
	timeoutMS, timeoutErr := ResolveComponentRPCTimeoutMS(entry.TimeoutMS)
	if timeoutErr != nil {
		return false, timeoutErr.Error()
	}
	env, err := h.InvokePlugin(ctx, methodPluginInvokeAction, entry.PluginID, entry.Name, args, timeoutMS)
	if err != nil {
		return false, err.Error()
	}
	if env.Error != nil {
		return false, envelopeErrorMessage(env.Error, "Action 执行失败")
	}
	return parseActionExecutionPayload(env.Payload)
}

// actionEntries 查询 Action 条目，name 非空时按短名过滤
func (h *Host) actionEntries(name string) ([]*ComponentEntry, error) {
	entries, err := h.components.ComponentsByType(ComponentTypeAction, false, ComponentFilter{})
	if err != nil {
		return nil, err
	}
	if name == "" {
		return entries, nil
	}
	out := make([]*ComponentEntry, 0, 1)
	for _, entry := range entries {
		if entry.Name == name {
			out = append(out, entry)
		}
	}
	return out, nil
}

func buildActionInfo(entry *ComponentEntry) ActionInfo {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return ActionInfo{
		Name:                        entry.Name,
		Description:                 stringValue(metadata["description"]),
		Enabled:                     entry.Enabled,
		PluginName:                  entry.PluginID,
		ActionParameters:            actionParametersValue(metadata["action_parameters"]),
		ActionRequire:               nonEmptyStringList(metadata["action_require"]),
		AssociatedTypes:             nonEmptyStringList(metadata["associated_types"]),
		ActivationType:              normalizeActionActivationType(metadata["activation_type"]),
		RandomActivationProbability: actionProbability(metadata["activation_probability"]),
		ActivationKeywords:          nonEmptyStringList(metadata["activation_keywords"]),
		ParallelAction:              truthyPayloadValue(metadata["parallel_action"]),
	}
}

func actionParametersValue(raw any) map[string]string {
	out := map[string]string{}
	mapping, ok := raw.(map[string]any)
	if !ok {
		return out
	}
	for key, value := range mapping {
		out[fmt.Sprint(key)] = stringValue(value)
	}
	return out
}

func nonEmptyStringList(raw any) []string {
	out := []string{}
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			if text := strings.TrimSpace(value); text != "" {
				out = append(out, value)
			}
		}
	case []any:
		for _, value := range values {
			if value == nil {
				continue
			}
			text := stringValue(value)
			if strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func normalizeActionActivationType(raw any) string {
	switch strings.ToLower(strings.TrimSpace(stringValue(raw))) {
	case "never":
		return "never"
	case "random":
		return "random"
	case "keyword":
		return "keyword"
	default:
		return "always"
	}
}

func actionProbability(raw any) float64 {
	value, err := coerceFloat64(raw)
	if err != nil {
		return 0
	}
	return value
}

func parseActionExecutionPayload(payload map[string]any) (bool, string) {
	success := boolValue(payload["success"])
	result := payload["result"]
	switch value := result.(type) {
	case []any:
		if len(value) >= 2 {
			return truthyPayloadValue(value[0]), resultText(value[1])
		}
		if len(value) == 1 {
			return truthyPayloadValue(value[0]), ""
		}
	case []string:
		if len(value) >= 2 {
			return truthyPayloadValue(value[0]), value[1]
		}
		if len(value) == 1 {
			return truthyPayloadValue(value[0]), ""
		}
	}
	return success, resultText(result)
}

func resultText(raw any) string {
	if raw == nil {
		return ""
	}
	return stringValue(raw)
}
