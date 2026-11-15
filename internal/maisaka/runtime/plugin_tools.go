package runtime

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/plugin_runtime"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"go.uber.org/zap"
)

// pluginTool 把 Runner 注册的插件工具暴露给 LLM，执行时再按会话签发文件作用域
type pluginTool struct {
	host     *pluginruntime.Host
	name     string
	pluginID string
	info     *schema.ToolInfo
	deferred bool
}

// IsDeferredTool 标记该插件工具需要经 tool_search 发现后才进入模型工具列表
func (t *pluginTool) IsDeferredTool() bool {
	return t != nil && t.deferred
}

func (t *pluginTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *pluginTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	structured, err := t.InvokeStructuredToolResult(ctx, argumentsInJSON)
	if err != nil {
		return "", err
	}
	if !structured.Success {
		return "", fmt.Errorf("%s", firstNonEmptyPluginText(structured.ErrorMessage, "插件工具执行失败"))
	}
	return structured.Content, nil
}

// InvokeStructuredToolResult 保留插件结果语义，stop_after_execution 转为暂停标记
func (t *pluginTool) InvokeStructuredToolResult(ctx context.Context, argumentsInJSON string) (tools.StructuredToolResult, error) {
	if t == nil || t.host == nil {
		return tools.StructuredToolResult{}, fmt.Errorf("插件运行时未初始化")
	}
	tc := tools.GetToolContext(ctx)
	if tc == nil {
		return tools.StructuredToolResult{}, fmt.Errorf("工具上下文未初始化")
	}
	if t.deferred && !tc.IsToolDiscovered(t.name) {
		return tools.StructuredToolResult{}, fmt.Errorf("工具 %s 尚未通过 tool_search 发现", t.name)
	}
	args := map[string]any{}
	raw := strings.TrimSpace(argumentsInJSON)
	if raw != "" {
		if err := sonic.UnmarshalString(raw, &args); err != nil {
			return tools.StructuredToolResult{}, fmt.Errorf("插件工具参数必须是对象: %w", err)
		}
	}
	isGroup := tc.ConversationKind == memory.ConversationKindGroup
	sessionID := tc.SessionID
	groupID := ""
	if isGroup {
		groupID = strconv.FormatInt(tc.TargetID, 10)
	}
	result := t.host.ExecuteTool(ctx, t.name, args, pluginruntime.ToolExecutionContext{
		SessionID:   sessionID,
		StreamID:    sessionID,
		IsGroupChat: &isGroup,
		GroupID:     groupID,
		UserID:      pluginToolUserID(tc, isGroup),
		Platform:    tc.Platform,
	})
	converted := tools.StructuredToolResult{
		Success:            result.Success,
		Content:            result.Content,
		ErrorMessage:       result.ErrorMessage,
		StructuredContent:  result.StructuredContent,
		Metadata:           result.Metadata,
		StopAfterExecution: result.StopAfterExecution,
	}
	if !result.Success {
		return converted, nil
	}
	if strings.TrimSpace(converted.Content) == "" {
		if result.StructuredContent != nil {
			if encoded, err := sonic.MarshalString(result.StructuredContent); err == nil {
				converted.Content = encoded
			}
		} else if len(result.ContentItems) > 0 {
			if encoded, err := sonic.MarshalString(result.ContentItems); err == nil {
				converted.Content = encoded
			}
		}
		if strings.TrimSpace(converted.Content) == "" {
			converted.Content = "插件工具执行完成"
		}
	}
	return converted, nil
}

func pluginToolUserID(tc *tools.ToolContext, isGroup bool) string {
	if tc == nil || isGroup || tc.TargetID <= 0 {
		return ""
	}
	return strconv.FormatInt(tc.TargetID, 10)
}

func firstNonEmptyPluginText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// buildPluginTools 把当前 Host 中启用的非 legacy 工具转成 eino 工具，跳过与内置工具重名项
func (a *Agent) buildPluginTools(existing map[string]struct{}) []tool.BaseTool {
	if a == nil || a.pluginHost == nil || a.pluginHost.Components() == nil {
		return nil
	}
	entries := a.pluginHost.Components().Tools(false, "")
	sort.Slice(entries, func(i, j int) bool { return entries[i].FullName < entries[j].FullName })
	out := make([]tool.BaseTool, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || !entry.Enabled || entry.LegacyComponentType == pluginruntime.ComponentTypeAction {
			continue
		}
		if pluginToolHidden(entry) {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if _, dup := existing[name]; dup {
			zap.L().Warn("插件工具与现有工具重名，已跳过", zap.String("tool", name), zap.String("plugin_id", entry.PluginID))
			continue
		}
		info, err := buildPluginToolInfo(entry)
		if err != nil {
			zap.L().Warn("插件工具参数 schema 无效，已跳过", zap.String("tool", entry.FullName), zap.Error(err))
			continue
		}
		existing[name] = struct{}{}
		out = append(out, &pluginTool{host: a.pluginHost, name: name, pluginID: entry.PluginID, info: info, deferred: pluginToolDeferred(entry)})
	}
	return out
}

func buildPluginToolInfo(entry *pluginruntime.ComponentEntry) (*schema.ToolInfo, error) {
	description := strings.TrimSpace(entry.Description)
	if description == "" {
		description = "工具 " + entry.Name
	}
	info := &schema.ToolInfo{Name: entry.Name, Desc: description}
	parameters := entry.BuildParametersSchema()
	if len(parameters) == 0 {
		return info, nil
	}
	encoded, err := sonic.MarshalString(parameters)
	if err != nil {
		return nil, err
	}
	var parameterSchema jsonschema.Schema
	if err := sonic.UnmarshalString(encoded, &parameterSchema); err != nil {
		return nil, err
	}
	info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&parameterSchema)
	return info, nil
}

func pluginToolDeferred(entry *pluginruntime.ComponentEntry) bool {
	if entry == nil {
		return true
	}
	raw := ""
	if entry.Metadata != nil {
		raw = strings.ToLower(strings.TrimSpace(fmt.Sprint(entry.Metadata["visibility"])))
	}
	switch raw {
	case "visible":
		return false
	case "deferred":
		return true
	case "hidden":
		return false
	}
	if entry.Metadata == nil {
		return true
	}
	return !pluginMetadataTruthy(entry.Metadata["core_tool"])
}

func pluginMetadataTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case nil:
		return false
	default:
		return true
	}
}

func pluginToolHidden(entry *pluginruntime.ComponentEntry) bool {
	if entry == nil || entry.Metadata == nil {
		return false
	}
	visibility, _ := entry.Metadata["visibility"].(string)
	return strings.EqualFold(strings.TrimSpace(visibility), "hidden")
}
