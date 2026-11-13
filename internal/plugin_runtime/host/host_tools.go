package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"maibot-go/internal/core/tooling"
)

// FileScopeIssuer 为插件工具签发文件作用域，由 filesvc.Service 实现
type FileScopeIssuer interface {
	Enabled() bool
	PluginScope(pluginID, sessionID string, lifetime time.Duration) (string, error)
	Revoke(token string)
}

// ToolExecutionContext 插件工具执行上下文，对齐 core.tooling.ToolExecutionContext 的可复用字段
type ToolExecutionContext struct {
	SessionID   string
	StreamID    string
	IsGroupChat *bool
	GroupID     string
	UserID      string
	Platform    string
}

// ToolContentItem 插件工具返回的结构化内容项
type ToolContentItem struct {
	ContentType string         `json:"content_type"`
	Text        string         `json:"text,omitempty"`
	Data        string         `json:"data,omitempty"`
	MimeType    string         `json:"mime_type,omitempty"`
	URI         string         `json:"uri,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolExecutionResult 插件工具统一执行结果，对齐 core.tooling.ToolExecutionResult
type ToolExecutionResult struct {
	ToolName           string
	Success            bool
	Content            string
	ErrorMessage       string
	StructuredContent  any
	ContentItems       []ToolContentItem
	Metadata           map[string]any
	StopAfterExecution bool
}

// SetFileScopeIssuer 注入文件作用域签发器；nil 表示文件能力未接
func (h *Host) SetFileScopeIssuer(issuer FileScopeIssuer) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.fileScopes = issuer
	h.mu.Unlock()
}

func (h *Host) fileScopeIssuer() FileScopeIssuer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.fileScopes
}

// ExecuteTool 按短名执行插件工具，对齐 ComponentQueryService.invoke_tool_as_tool
//
// 执行链统一经过 core/tooling 注册表，使插件工具与 MCP/内置工具共享命名冲突处理、
// 可用性过滤与调用结果结构；注册表不可用时才回退直连。
func (h *Host) ExecuteTool(ctx context.Context, toolName string, args map[string]any, execCtx ToolExecutionContext) ToolExecutionResult {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return pluginToolFailure(toolName, "工具名称不能为空")
	}
	if h == nil {
		return pluginToolFailure(toolName, "插件运行时未初始化")
	}
	if h.toolRegistry == nil {
		return h.executePluginToolDirect(ctx, toolName, args, execCtx)
	}
	isGroup := execCtx.IsGroupChat
	coreCtx := &tooling.ToolExecutionContext{
		SessionID:   execCtx.SessionID,
		StreamID:    execCtx.StreamID,
		IsGroupChat: isGroup,
		GroupID:     execCtx.GroupID,
		UserID:      execCtx.UserID,
		Platform:    execCtx.Platform,
	}
	result := h.toolRegistry.Invoke(ctx, tooling.ToolInvocation{
		ToolName:  toolName,
		Arguments: args,
		SessionID: execCtx.SessionID,
		StreamID:  execCtx.StreamID,
	}, coreCtx)
	return coreToolResultToPlugin(result)
}

// executePluginToolDirect 直接按组件短名解析并调用插件工具
func (h *Host) executePluginToolDirect(ctx context.Context, toolName string, args map[string]any, execCtx ToolExecutionContext) ToolExecutionResult {
	if h == nil || h.components == nil {
		return pluginToolFailure(toolName, "插件运行时未初始化")
	}
	isGroup := execCtx.IsGroupChat
	entry := h.components.FindTool(toolName, ComponentFilter{
		SessionID:   execCtx.SessionID,
		IsGroupChat: isGroup,
		GroupID:     execCtx.GroupID,
	})
	if entry == nil {
		return pluginToolFailure(toolName, "未找到插件工具："+toolName)
	}
	if len(entry.Platforms) > 0 {
		platform := strings.ToLower(strings.TrimSpace(execCtx.Platform))
		if platform == "" || !containsString(entry.Platforms, platform) {
			return pluginToolFailure(entry.Name, "当前平台不在插件工具的 platforms 作用域内。")
		}
	}
	payload := pluginToolPayload(entry, args, execCtx)
	scope := ""
	issuer := h.fileScopeIssuer()
	if issuer != nil && issuer.Enabled() && strings.TrimSpace(execCtx.SessionID) != "" {
		token, err := issuer.PluginScope(entry.PluginID, execCtx.SessionID, time.Hour)
		if err != nil {
			return pluginToolFailure(entry.Name, "签发文件作用域失败: "+err.Error())
		}
		scope = token
		defer issuer.Revoke(scope)
	}
	// 作用域最后写入，调用参数不能覆盖
	payload["_file_scope"] = scope

	resp, err := h.InvokePlugin(ctx, entry.InvokeMethod, entry.PluginID, entry.Name, payload, entry.TimeoutMS)
	if err != nil {
		zap.L().Warn("插件工具执行失败",
			zap.String("plugin_id", entry.PluginID),
			zap.String("tool", entry.Name),
			zap.Error(err))
		return pluginToolFailure(entry.Name, err.Error())
	}
	if resp.Error != nil {
		return pluginToolFailure(entry.Name, envelopeErrorMessage(resp.Error, "插件工具执行失败"))
	}
	transportSuccess := payloadBool(resp.Payload, "success", false)
	result := resp.Payload["result"]
	if !transportSuccess {
		message := ""
		if result != nil {
			message = strings.TrimSpace(fmt.Sprint(result))
		}
		return pluginToolFailure(entry.Name, message)
	}
	return parsePluginToolResult(entry, result)
}

func coreToolResultToPlugin(result tooling.ToolExecutionResult) ToolExecutionResult {
	output := ToolExecutionResult{
		ToolName:           result.ToolName,
		Success:            result.Success,
		Content:            result.Content,
		ErrorMessage:       result.ErrorMessage,
		StructuredContent:  result.StructuredContent,
		Metadata:           cloneAnyMap(result.Metadata),
		StopAfterExecution: result.StopAfterExecution,
	}
	if len(result.ContentItems) > 0 {
		output.ContentItems = make([]ToolContentItem, 0, len(result.ContentItems))
		for _, item := range result.ContentItems {
			output.ContentItems = append(output.ContentItems, ToolContentItem{
				ContentType: item.ContentType,
				Text:        item.Text,
				Data:        item.Data,
				MimeType:    item.MimeType,
				URI:         item.URI,
				Name:        item.Name,
				Description: item.Description,
				Metadata:    cloneAnyMap(item.Metadata),
			})
		}
	}
	return output
}

func pluginToolFailure(toolName, message string) ToolExecutionResult {
	return ToolExecutionResult{
		ToolName:     toolName,
		Success:      false,
		ErrorMessage: message,
	}
}

func pluginToolPayload(entry *ComponentEntry, args map[string]any, execCtx ToolExecutionContext) map[string]any {
	payload := make(map[string]any, len(args)+5)
	for key, value := range args {
		payload[key] = value
	}
	contextPayload := pluginToolContextPayload(execCtx)
	if entry.InvokeMethod == "plugin.invoke_action" {
		streamID := firstNonEmpty(stringValue(contextPayload["stream_id"]), execCtx.StreamID, execCtx.SessionID)
		for key, value := range contextPayload {
			if _, exists := payload[key]; !exists || !truthyPayloadValue(payload[key]) {
				payload[key] = value
			}
		}
		if streamID != "" {
			payload["stream_id"] = streamID
			payload["chat_id"] = streamID
		}
		payload["action_data"] = cloneAnyMap(args)
		return payload
	}
	for key, value := range contextPayload {
		if _, exists := payload[key]; !exists || !truthyPayloadValue(payload[key]) {
			payload[key] = value
		}
	}
	return payload
}

func pluginToolContextPayload(execCtx ToolExecutionContext) map[string]any {
	payload := map[string]any{}
	streamID := firstNonEmpty(strings.TrimSpace(execCtx.StreamID), strings.TrimSpace(execCtx.SessionID))
	if streamID != "" {
		payload["stream_id"] = streamID
		payload["chat_id"] = streamID
	}
	if groupID := strings.TrimSpace(execCtx.GroupID); groupID != "" {
		payload["group_id"] = groupID
	}
	if userID := strings.TrimSpace(execCtx.UserID); userID != "" {
		payload["user_id"] = userID
	}
	if platform := strings.TrimSpace(execCtx.Platform); platform != "" {
		payload["platform"] = platform
	}
	return payload
}

func parsePluginToolResult(entry *ComponentEntry, result any) ToolExecutionResult {
	output := ToolExecutionResult{
		ToolName:          entry.Name,
		Success:           true,
		StructuredContent: result,
		Metadata:          map[string]any{"plugin_id": entry.PluginID},
	}
	switch value := result.(type) {
	case map[string]any:
		if raw, exists := value["stop_after_execution"]; exists {
			flag, ok := raw.(bool)
			if !ok {
				output.Success = false
				output.ErrorMessage = "插件工具返回字段 `stop_after_execution` 必须为布尔值。"
				return output
			}
			output.StopAfterExecution = flag
		}
		if raw, exists := value["success"]; exists {
			if flag, ok := raw.(bool); ok {
				output.Success = flag
			} else {
				output.Success = truthyPayloadValue(raw)
			}
		}
		output.Content = strings.TrimSpace(firstNonEmpty(stringValue(value["content"]), stringValue(value["message"])))
		output.ContentItems = parseToolContentItems(value["content_items"])
		if !output.Success {
			output.ErrorMessage = strings.TrimSpace(firstNonEmpty(stringValue(value["error"]), stringValue(value["message"]), "插件工具执行失败"))
		}
		return output
	case []any:
		if len(value) > 0 {
			if flag, ok := value[0].(bool); ok {
				output.Success = flag
				message := ""
				if len(value) > 1 && value[1] != nil {
					message = strings.TrimSpace(fmt.Sprint(value[1]))
				}
				if flag {
					output.Content = message
				} else {
					output.ErrorMessage = message
				}
				return output
			}
		}
	}
	if result != nil {
		output.Content = strings.TrimSpace(fmt.Sprint(result))
	}
	return output
}

func parseToolContentItems(raw any) []ToolContentItem {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	items := make([]ToolContentItem, 0, len(list))
	for _, item := range list {
		value, ok := item.(map[string]any)
		if !ok {
			continue
		}
		contentType := strings.TrimSpace(firstNonEmpty(stringValue(value["content_type"]), stringValue(value["type"]), "unknown"))
		switch contentType {
		case "text", "image", "audio", "resource_link", "resource", "binary":
		default:
			contentType = "unknown"
		}
		metadata, _ := value["metadata"].(map[string]any)
		items = append(items, ToolContentItem{
			ContentType: contentType,
			Text:        stringValue(value["text"]),
			Data:        firstNonEmpty(stringValue(value["data"]), stringValue(value["base64"])),
			MimeType:    stringValue(value["mime_type"]),
			URI:         stringValue(value["uri"]),
			Name:        stringValue(value["name"]),
			Description: stringValue(value["description"]),
			Metadata:    cloneAnyMap(metadata),
		})
	}
	return items
}

func stringValue(raw any) string {
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return fmt.Sprint(raw)
}

func truthyPayloadValue(raw any) bool {
	switch value := raw.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return strings.TrimSpace(value) != ""
	case int:
		return value != 0
	case int8:
		return value != 0
	case int16:
		return value != 0
	case int32:
		return value != 0
	case int64:
		return value != 0
	case uint:
		return value != 0
	case uint8:
		return value != 0
	case uint16:
		return value != 0
	case uint32:
		return value != 0
	case uint64:
		return value != 0
	case float32:
		return value != 0
	case float64:
		return value != 0
	}
	return true
}

// FindTool 按短名查找当前会话可执行的工具，重复名称只保留排序后的首个
func (r *ComponentRegistry) FindTool(name string, filter ComponentFilter) *ComponentEntry {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matched []*ComponentEntry
	for _, entry := range r.byType[ComponentTypeTool] {
		if entry.Name != name || !checkComponentEnabled(entry, filter) {
			continue
		}
		matched = append(matched, entry)
	}
	if len(matched) == 0 {
		return nil
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].FullName < matched[j].FullName })
	if len(matched) > 1 {
		zap.L().Warn("检测到重复工具名称，将只保留首个匹配项", zap.String("name", name))
	}
	return matched[0]
}
