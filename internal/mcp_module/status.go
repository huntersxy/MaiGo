package mcp

// ServerStatus 单台 MCP 服务器的连接快照（供 WebUI/运维只读展示）
type ServerStatus struct {
	Name            string                    `json:"name"`
	Transport       string                    `json:"transport"`
	Connected       bool                      `json:"connected"`
	ProtocolVersion string                    `json:"protocol_version"`
	ToolCount       int                       `json:"tool_count"`
	PromptCount     int                       `json:"prompt_count"`
	ResourceCount   int                       `json:"resource_count"`
	TemplateCount   int                       `json:"template_count"`
	Prompts         []PromptPreview           `json:"prompts,omitempty"`
	Resources       []ResourcePreview         `json:"resources,omitempty"`
	Templates       []ResourceTemplatePreview `json:"resource_templates,omitempty"`
	Error           string                    `json:"error"`
}

// StatusSnapshot 进程内 MCP 运行态摘要，对齐 MaiBot MCPManager.get_status_snapshot 字段语义
type StatusSnapshot struct {
	Initialized   bool           `json:"initialized"`
	ServerCount   int            `json:"server_count"`
	ToolCount     int            `json:"tool_count"`
	PromptCount   int            `json:"prompt_count"`
	ResourceCount int            `json:"resource_count"`
	TemplateCount int            `json:"template_count"`
	Servers       []ServerStatus `json:"servers"`
}
