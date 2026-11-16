package tooling

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
)

// 工具内容类型，对齐 ToolContentItem.content_type
const (
	ContentTypeText         = "text"
	ContentTypeImage        = "image"
	ContentTypeAudio        = "audio"
	ContentTypeResourceLink = "resource_link"
	ContentTypeResource     = "resource"
	ContentTypeBinary       = "binary"
	ContentTypeUnknown      = "unknown"
)

// ToolIcon 统一工具图标信息
type ToolIcon struct {
	Src      string   `json:"src"`
	MimeType string   `json:"mime_type,omitempty"`
	Sizes    []string `json:"sizes,omitempty"`
}

// ToolAnnotation 统一工具注解信息
type ToolAnnotation struct {
	Audience    []string       `json:"audience,omitempty"`
	Priority    *float64       `json:"priority,omitempty"`
	Title       string         `json:"title,omitempty"`
	ReadOnly    *bool          `json:"read_only,omitempty"`
	Destructive *bool          `json:"destructive,omitempty"`
	Idempotent  *bool          `json:"idempotent,omitempty"`
	OpenWorld   *bool          `json:"open_world,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolContentItem 统一工具内容项
type ToolContentItem struct {
	ContentType string          `json:"content_type"`
	Text        string          `json:"text,omitempty"`
	Data        string          `json:"data,omitempty"`
	MimeType    string          `json:"mime_type,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Annotation  *ToolAnnotation `json:"annotation,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
}

// BuildHistoryText 生成适合写入历史消息的文本摘要
func (item ToolContentItem) BuildHistoryText() string {
	switch item.ContentType {
	case ContentTypeText:
		if text := strings.TrimSpace(item.Text); text != "" {
			return text
		}
	case ContentTypeImage:
		return "[图片内容 " + firstNonEmpty(item.MimeType, "unknown") + "]"
	case ContentTypeAudio:
		return "[音频内容 " + firstNonEmpty(item.MimeType, "unknown") + "]"
	case ContentTypeResourceLink:
		return "[资源链接] " + firstNonEmpty(item.Name, item.URI, "资源链接")
	case ContentTypeResource:
		if text := strings.TrimSpace(item.Text); text != "" {
			return text
		}
		return "[嵌入资源] " + firstNonEmpty(item.Name, item.URI, "嵌入资源")
	case ContentTypeBinary:
		return "[二进制内容 " + firstNonEmpty(item.MimeType, "unknown") + "]"
	}
	return "[" + item.ContentType + " 内容]"
}

// NewToolSpec 构造默认启用的工具声明；Go 零值无法表达 Python 的 enabled 默认 True
func NewToolSpec(name, description string) ToolSpec {
	return ToolSpec{Name: name, Description: description, Enabled: true}
}

// ToolSpec 统一工具声明
type ToolSpec struct {
	Name                string          `json:"name"`
	Description         string          `json:"description,omitempty"`
	Title               string          `json:"title,omitempty"`
	ParametersSchema    map[string]any  `json:"parameters_schema,omitempty"`
	OutputSchema        map[string]any  `json:"output_schema,omitempty"`
	ProviderName        string          `json:"provider_name,omitempty"`
	ProviderType        string          `json:"provider_type,omitempty"`
	Enabled             bool            `json:"enabled"`
	Icons               []ToolIcon      `json:"icons,omitempty"`
	Annotation          *ToolAnnotation `json:"annotation,omitempty"`
	Metadata            map[string]any  `json:"metadata,omitempty"`
	BriefDescription    string          `json:"-"`
	DetailedDescription string          `json:"-"`
}

// BuildLLMDescription 构建供 LLM 使用的描述文本
func (spec ToolSpec) BuildLLMDescription() string {
	return strings.TrimSpace(spec.EffectiveDescription())
}

// EffectiveDescription 返回 description，缺省回退 brief_description，detailed_description 已弃用
func (spec ToolSpec) EffectiveDescription() string {
	if strings.TrimSpace(spec.Description) != "" {
		return spec.Description
	}
	return spec.BriefDescription
}

// ToLLMDefinition 转换为统一的 LLM 工具定义
func (spec ToolSpec) ToLLMDefinition() map[string]any {
	definition := map[string]any{
		"name":        spec.Name,
		"description": spec.BuildLLMDescription(),
	}
	if spec.ParametersSchema != nil {
		definition["parameters_schema"] = cloneAny(spec.ParametersSchema)
	}
	return definition
}

// FilterToolArguments 只保留工具声明的参数；参数值本身保持原样
func FilterToolArguments(arguments map[string]any, schema map[string]any) map[string]any {
	if len(arguments) == 0 {
		return arguments
	}
	out := make(map[string]any, len(arguments))
	for name, value := range arguments {
		if toolSchemaDeclares(name, schema, schema, 0) {
			out[name] = value
		}
	}
	return out
}

func toolSchemaDeclares(name string, node, root map[string]any, depth int) bool {
	if node == nil || depth > 32 {
		return false
	}
	if reference, ok := node["$ref"].(string); ok && strings.HasPrefix(reference, "#/") {
		target := root
		for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			target, _ = target[part].(map[string]any)
			if target == nil {
				break
			}
		}
		if target != nil && toolSchemaDeclares(name, target, root, depth+1) {
			return true
		}
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		if _, exists := properties[name]; exists {
			return true
		}
	}
	if patterns, ok := node["patternProperties"].(map[string]any); ok {
		for pattern := range patterns {
			if matched, err := regexp.MatchString(pattern, name); err == nil && matched {
				return true
			}
		}
	}
	switch additional := node["additionalProperties"].(type) {
	case bool:
		if additional {
			return true
		}
	case map[string]any:
		return true
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := node[keyword].([]any)
		for _, raw := range branches {
			if branch, ok := raw.(map[string]any); ok && toolSchemaDeclares(name, branch, root, depth+1) {
				return true
			}
		}
	}
	return false
}

// ToolInvocation 统一工具调用请求
type ToolInvocation struct {
	ToolName  string         `json:"tool_name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	CallID    string         `json:"call_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	StreamID  string         `json:"stream_id,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// ToolExecutionContext 统一工具执行上下文
type ToolExecutionContext struct {
	SessionID   string         `json:"session_id,omitempty"`
	StreamID    string         `json:"stream_id,omitempty"`
	Reasoning   string         `json:"reasoning,omitempty"`
	IsGroupChat *bool          `json:"is_group_chat,omitempty"`
	GroupID     string         `json:"group_id,omitempty"`
	UserID      string         `json:"user_id,omitempty"`
	Platform    string         `json:"platform,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolAvailabilityContext 工具暴露可用性判断上下文
type ToolAvailabilityContext struct {
	SessionID   string `json:"session_id,omitempty"`
	StreamID    string `json:"stream_id,omitempty"`
	IsGroupChat *bool  `json:"is_group_chat,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	Platform    string `json:"platform,omitempty"`
}

// AvailabilityContext 从执行上下文派生可用性上下文
func (ctx *ToolExecutionContext) AvailabilityContext() *ToolAvailabilityContext {
	if ctx == nil {
		return nil
	}
	return &ToolAvailabilityContext{
		SessionID:   ctx.SessionID,
		StreamID:    ctx.StreamID,
		IsGroupChat: ctx.IsGroupChat,
		GroupID:     ctx.GroupID,
		UserID:      ctx.UserID,
		Platform:    ctx.Platform,
	}
}

// ToolExecutionResult 统一工具执行结果
type ToolExecutionResult struct {
	ToolName            string            `json:"tool_name"`
	Success             bool              `json:"success"`
	Content             string            `json:"content,omitempty"`
	ErrorMessage        string            `json:"error_message,omitempty"`
	StructuredContent   any               `json:"structured_content,omitempty"`
	ContentItems        []ToolContentItem `json:"content_items,omitempty"`
	PostHistoryMessages []any             `json:"post_history_messages,omitempty"`
	Metadata            map[string]any    `json:"metadata,omitempty"`
	StopAfterExecution  bool              `json:"stop_after_execution,omitempty"`
}

// GetHistoryContent 获取适合写入对话历史的结果文本
func (result ToolExecutionResult) GetHistoryContent() string {
	if content := strings.TrimSpace(result.Content); content != "" {
		return content
	}
	if len(result.ContentItems) > 0 {
		parts := make([]string, 0, len(result.ContentItems))
		for _, item := range result.ContentItems {
			if text := strings.TrimSpace(item.BuildHistoryText()); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, string(byte(10))))
		}
	}
	if result.StructuredContent != nil {
		if text, ok := result.StructuredContent.(string); ok {
			return strings.TrimSpace(text)
		}
		if encoded, err := sonic.MarshalString(result.StructuredContent); err == nil {
			return encoded
		}
		return strings.TrimSpace(toString(result.StructuredContent))
	}
	return strings.TrimSpace(result.ErrorMessage)
}

// ToolProvider 统一工具提供者协议
type ToolProvider interface {
	ProviderName() string
	ProviderType() string
	ListTools(ctx context.Context, availability *ToolAvailabilityContext) ([]ToolSpec, error)
	Invoke(ctx context.Context, invocation ToolInvocation, execution *ToolExecutionContext) (ToolExecutionResult, error)
	Close(ctx context.Context) error
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if encoded, err := json.Marshal(value); err == nil {
		return string(encoded)
	}
	return ""
}
