package mcp

import (
	"encoding/json"
	"strings"

	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
)

// Icon MCP 图标，对应 MaiBot ToolIcon
type Icon struct {
	Src      string   `json:"src"`
	MIMEType string   `json:"mime_type,omitempty"`
	Sizes    []string `json:"sizes,omitempty"`
}

// Annotation MCP 注解，对应 MaiBot ToolAnnotation
type Annotation struct {
	Audience    []string       `json:"audience,omitempty"`
	Priority    *float64       `json:"priority,omitempty"`
	Title       string         `json:"title,omitempty"`
	ReadOnly    *bool          `json:"read_only,omitempty"`
	Destructive *bool          `json:"destructive,omitempty"`
	Idempotent  *bool          `json:"idempotent,omitempty"`
	OpenWorld   *bool          `json:"open_world,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolContentItem 统一内容项，对应 MaiBot ToolContentItem
type ToolContentItem struct {
	ContentType string         `json:"content_type"`
	Text        string         `json:"text,omitempty"`
	Data        string         `json:"data,omitempty"`
	MIMEType    string         `json:"mime_type,omitempty"`
	URI         string         `json:"uri,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Annotation  *Annotation    `json:"annotation,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// HistoryText 返回适合写入历史的内容文本，对齐 ToolContentItem.build_history_text 的常用分支
// HistoryText 对齐 core/tooling.py ToolContentItem.build_history_text
func (i ToolContentItem) HistoryText() string {
	if i.ContentType == "text" && strings.TrimSpace(i.Text) != "" {
		return strings.TrimSpace(i.Text)
	}
	mime := i.MIMEType
	if mime == "" {
		mime = "unknown"
	}
	switch i.ContentType {
	case "image":
		return "[图片内容 " + mime + "]"
	case "audio":
		return "[音频内容 " + mime + "]"
	case "resource_link":
		return "[资源链接] " + firstNonEmpty(i.Name, i.URI, "资源链接")
	case "resource":
		if strings.TrimSpace(i.Text) != "" {
			return strings.TrimSpace(i.Text)
		}
		return "[嵌入资源] " + firstNonEmpty(i.Name, i.URI, "嵌入资源")
	case "binary":
		return "[二进制内容 " + mime + "]"
	default:
		return "[" + i.ContentType + " 内容]"
	}
}

// firstNonEmpty 返回首个非空字符串，全空时用 fallback
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return values[len(values)-1]
}

// PromptArgumentSpec MCP Prompt 参数声明，对应 MaiBot MCPPromptArgumentSpec
type PromptArgumentSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// PromptSpec MCP Prompt 声明，对应 MaiBot MCPPromptSpec
type PromptSpec struct {
	Name        string               `json:"name"`
	ServerName  string               `json:"server_name"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Arguments   []PromptArgumentSpec `json:"arguments"`
	Icons       []Icon               `json:"icons,omitempty"`
	Metadata    map[string]any       `json:"metadata,omitempty"`
}

// PromptMessage MCP Prompt 消息，对应 MaiBot MCPPromptMessage
type PromptMessage struct {
	Role    string          `json:"role"`
	Content ToolContentItem `json:"content"`
}

// PromptResult MCP Prompt 获取结果，对应 MaiBot MCPPromptResult
type PromptResult struct {
	PromptName  string          `json:"prompt_name"`
	ServerName  string          `json:"server_name"`
	Description string          `json:"description"`
	Messages    []PromptMessage `json:"messages"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
}

// ResourceSpec MCP Resource 声明，对应 MaiBot MCPResourceSpec
type ResourceSpec struct {
	URI         string         `json:"uri"`
	ServerName  string         `json:"server_name"`
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	MIMEType    string         `json:"mime_type,omitempty"`
	Size        *int64         `json:"size,omitempty"`
	Icons       []Icon         `json:"icons,omitempty"`
	Annotation  *Annotation    `json:"annotation,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ResourceTemplateSpec MCP Resource Template 声明，对应 MaiBot MCPResourceTemplateSpec
type ResourceTemplateSpec struct {
	URITemplate string         `json:"uri_template"`
	ServerName  string         `json:"server_name"`
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	MIMEType    string         `json:"mime_type,omitempty"`
	Icons       []Icon         `json:"icons,omitempty"`
	Annotation  *Annotation    `json:"annotation,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ResourceReadResult MCP Resource 读取结果，对应 MaiBot MCPResourceReadResult
type ResourceReadResult struct {
	URI        string            `json:"uri"`
	ServerName string            `json:"server_name"`
	Contents   []ToolContentItem `json:"contents"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
}

// ToolDefinition 原始工具声明，供状态快照与 MaiBot 裸名注册表使用
type ToolDefinition struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema,omitempty"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	Annotation   *Annotation    `json:"annotation,omitempty"`
	Icons        []Icon         `json:"icons,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// ToolExecutionResult 一次工具调用的完整结果，对应 MaiBot ToolExecutionResult
//
// Success=false 表示工具自身报告失败（MCP isError），ErrorMessage 保留服务端错误文本；
// 协议/连接错误仍由调用方以 error 返回。
type ToolExecutionResult struct {
	ToolName          string            `json:"tool_name"`
	ServerName        string            `json:"server_name,omitempty"`
	Success           bool              `json:"success"`
	Content           string            `json:"content"`
	ErrorMessage      string            `json:"error_message"`
	StructuredContent any               `json:"structured_content,omitempty"`
	ContentItems      []ToolContentItem `json:"content_items"`
	Metadata          map[string]any    `json:"metadata,omitempty"`
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func buildIcon(raw mcpprotocol.Icon) Icon {
	sizes := make([]string, 0, len(raw.Sizes))
	for _, item := range raw.Sizes {
		sizes = append(sizes, item)
	}
	return Icon{Src: raw.Src, MIMEType: raw.MIMEType, Sizes: sizes}
}

func buildIcons(raw []mcpprotocol.Icon) []Icon {
	if len(raw) == 0 {
		return nil
	}
	out := make([]Icon, 0, len(raw))
	for _, item := range raw {
		out = append(out, buildIcon(item))
	}
	return out
}

func metadataFromMeta(meta *mcpprotocol.Meta) map[string]any {
	if meta == nil {
		return nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

func buildAnnotation(raw *mcpprotocol.Annotations) *Annotation {
	if raw == nil {
		return nil
	}
	out := &Annotation{Priority: raw.Priority}
	for _, role := range raw.Audience {
		out.Audience = append(out.Audience, string(role))
	}
	if raw.LastModified != "" {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		out.Metadata["lastModified"] = raw.LastModified
	}
	if len(out.Audience) == 0 && out.Priority == nil && out.Metadata == nil {
		return nil
	}
	return out
}

func buildToolAnnotation(raw mcpprotocol.ToolAnnotation) *Annotation {
	title := strings.TrimSpace(raw.Title)
	out := &Annotation{
		Title:       title,
		ReadOnly:    raw.ReadOnlyHint,
		Destructive: raw.DestructiveHint,
		Idempotent:  raw.IdempotentHint,
		OpenWorld:   raw.OpenWorldHint,
	}
	if title == "" && raw.ReadOnlyHint == nil && raw.DestructiveHint == nil &&
		raw.IdempotentHint == nil && raw.OpenWorldHint == nil {
		return nil
	}
	return out
}

func buildContentItem(raw mcpprotocol.Content) ToolContentItem {
	switch content := raw.(type) {
	case mcpprotocol.TextContent:
		return ToolContentItem{
			ContentType: "text",
			Text:        content.Text,
			Annotation:  buildAnnotation(content.Annotations),
			Metadata:    metadataFromMeta(content.Meta),
		}
	case mcpprotocol.ImageContent:
		return ToolContentItem{
			ContentType: "image",
			Data:        content.Data,
			MIMEType:    content.MIMEType,
			Annotation:  buildAnnotation(content.Annotations),
			Metadata:    metadataFromMeta(content.Meta),
		}
	case mcpprotocol.AudioContent:
		return ToolContentItem{
			ContentType: "audio",
			Data:        content.Data,
			MIMEType:    content.MIMEType,
			Annotation:  buildAnnotation(content.Annotations),
			Metadata:    metadataFromMeta(content.Meta),
		}
	case mcpprotocol.ResourceLink:
		item := ToolContentItem{
			ContentType: "resource_link",
			URI:         content.URI,
			Name:        content.Name,
			Description: content.Description,
			MIMEType:    content.MIMEType,
			Annotation:  buildAnnotation(content.Annotations),
		}
		if content.Title != "" {
			if item.Metadata == nil {
				item.Metadata = map[string]any{}
			}
			item.Metadata["title"] = content.Title
		}
		return item
	case mcpprotocol.EmbeddedResource:
		item := ToolContentItem{
			ContentType: "resource",
			Annotation:  buildAnnotation(content.Annotations),
			Metadata:    metadataFromMeta(content.Meta),
		}
		switch resource := content.Resource.(type) {
		case mcpprotocol.TextResourceContents:
			item.Text = resource.Text
			item.URI = resource.URI
			item.MIMEType = resource.MIMEType
			item.Metadata = mergeMetadata(item.Metadata, resource.Meta)
		case mcpprotocol.BlobResourceContents:
			item.Data = resource.Blob
			item.URI = resource.URI
			item.MIMEType = resource.MIMEType
			item.Metadata = mergeMetadata(item.Metadata, resource.Meta)
		}
		return item
	case mcpprotocol.ToolUseContent:
		return ToolContentItem{
			ContentType: "tool_use",
			Name:        content.Name,
			Data:        content.ID,
			Metadata:    metadataFromMeta(content.Meta),
		}
	case mcpprotocol.ToolResultContent:
		items := buildContentItems(content.Content)
		texts := make([]string, 0, len(items))
		for _, item := range items {
			if text := strings.TrimSpace(item.HistoryText()); text != "" {
				texts = append(texts, text)
			}
		}
		return ToolContentItem{
			ContentType: "tool_result",
			Data:        content.ToolUseID,
			Text:        strings.Join(texts, "\n"),
			Metadata:    metadataFromMeta(content.Meta),
		}
	default:
		return ToolContentItem{ContentType: "unknown"}
	}
}

func mergeMetadata(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func buildContentItems(raw []mcpprotocol.Content) []ToolContentItem {
	if len(raw) == 0 {
		return []ToolContentItem{}
	}
	out := make([]ToolContentItem, 0, len(raw))
	for _, item := range raw {
		out = append(out, buildContentItem(item))
	}
	return out
}

func buildPromptArgumentSpec(raw mcpprotocol.PromptArgument) PromptArgumentSpec {
	return PromptArgumentSpec{
		Name:        raw.Name,
		Description: raw.Description,
		Required:    raw.Required,
	}
}

func buildPromptSpec(raw mcpprotocol.Prompt, serverName string) PromptSpec {
	args := make([]PromptArgumentSpec, 0, len(raw.Arguments))
	for _, item := range raw.Arguments {
		args = append(args, buildPromptArgumentSpec(item))
	}
	return PromptSpec{
		Name:        raw.Name,
		ServerName:  serverName,
		Title:       raw.Title,
		Description: raw.Description,
		Arguments:   args,
		Icons:       buildIcons(raw.Icons),
		Metadata:    metadataFromMeta(raw.Meta),
	}
}

func buildPromptResult(raw mcpprotocol.GetPromptResult, promptName, serverName string) PromptResult {
	messages := make([]PromptMessage, 0, len(raw.Messages))
	for _, item := range raw.Messages {
		messages = append(messages, PromptMessage{
			Role:    string(item.Role),
			Content: buildContentItem(item.Content),
		})
	}
	return PromptResult{
		PromptName:  promptName,
		ServerName:  serverName,
		Description: raw.Description,
		Messages:    messages,
		Metadata:    metadataFromMeta(raw.Meta),
	}
}

func buildResourceSpec(raw mcpprotocol.Resource, serverName string) ResourceSpec {
	return ResourceSpec{
		URI:         raw.URI,
		ServerName:  serverName,
		Name:        raw.Name,
		Title:       raw.Title,
		Description: raw.Description,
		MIMEType:    raw.MIMEType,
		Size:        raw.Size,
		Icons:       buildIcons(raw.Icons),
		Annotation:  buildAnnotation(raw.Annotations),
		Metadata:    metadataFromMeta(raw.Meta),
	}
}

func buildResourceTemplateSpec(raw mcpprotocol.ResourceTemplate, serverName string) ResourceTemplateSpec {
	uriTemplate := ""
	if raw.URITemplate != nil {
		uriTemplate = raw.URITemplate.Raw()
	}
	return ResourceTemplateSpec{
		URITemplate: uriTemplate,
		ServerName:  serverName,
		Name:        raw.Name,
		Title:       raw.Title,
		Description: raw.Description,
		MIMEType:    raw.MIMEType,
		Icons:       buildIcons(raw.Icons),
		Annotation:  buildAnnotation(raw.Annotations),
		Metadata:    metadataFromMeta(raw.Meta),
	}
}

func buildResourceReadResult(raw mcpprotocol.ReadResourceResult, uri, serverName string) ResourceReadResult {
	contents := make([]ToolContentItem, 0, len(raw.Contents))
	for _, item := range raw.Contents {
		switch resource := item.(type) {
		case mcpprotocol.TextResourceContents:
			contents = append(contents, ToolContentItem{
				ContentType: "resource",
				Text:        resource.Text,
				MIMEType:    resource.MIMEType,
				URI:         fallback(resource.URI, uri),
				Metadata:    resource.Meta,
			})
		case mcpprotocol.BlobResourceContents:
			contents = append(contents, ToolContentItem{
				ContentType: "resource",
				Data:        resource.Blob,
				MIMEType:    resource.MIMEType,
				URI:         fallback(resource.URI, uri),
				Metadata:    resource.Meta,
			})
		}
	}
	return ResourceReadResult{
		URI:        uri,
		ServerName: serverName,
		Contents:   contents,
		Metadata:   metadataFromMeta(raw.Meta),
	}
}
