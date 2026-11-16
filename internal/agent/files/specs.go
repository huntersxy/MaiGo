package agentfiles

// Spec 描述一个 agent_* 工具，schema 逐字对齐 Python src/agent/provider.py 的 tool_specs
//
// files_* 工具定义在 src/files/provider.py，对应本仓 internal/files/provider.go。
type Spec struct {
	Name        string
	Description string
	Schema      map[string]any
	Deferred    bool
	Provider    string
}

// ProviderAgent 对齐 Python AgentToolProvider.provider_name
const ProviderAgent = "agent"

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringProperty(description string) map[string]any {
	property := map[string]any{"type": "string"}
	if description != "" {
		property["description"] = description
	}
	return property
}

func integerProperty(description string) map[string]any {
	property := map[string]any{"type": "integer"}
	if description != "" {
		property["description"] = description
	}
	return property
}

var agentSpecs = []Spec{
	{
		Name:        "agent_talk",
		Description: "与独立 Agent 双向图文传话。普通聊天 target 留空；Agent 回信填写 agent_chats 返回的真实 session_id。回执表示入队并触发 Planner，不代表已经向 IM 发言。",
		Schema: objectSchema(map[string]any{
			"text":   stringProperty(""),
			"target": stringProperty(""),
			"images": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":        "string",
					"description": "普通聊天填写当前带图消息 ID 或图片 hash；Agent 填工作目录图片路径。",
				},
			},
		}, []string{"text"}),
		Deferred: true,
		Provider: ProviderAgent,
	},
	{
		Name:        "agent_status",
		Description: "查看 Agent 后端及运行状态。",
		Schema:      objectSchema(map[string]any{}, []string{}),
		Deferred:    true,
		Provider:    ProviderAgent,
	},
	{
		Name:        "agent_chats",
		Description: "列出已加载的真实聊天流及其名称和最近活跃时间。",
		Schema:      objectSchema(map[string]any{}, []string{}),
		Deferred:    true,
		Provider:    ProviderAgent,
	},
	{
		Name:        "agent_recent",
		Description: "读取真实聊天流历史，limit 默认 20，0 为全部。",
		Schema: objectSchema(map[string]any{
			"target": stringProperty(""),
			"limit": map[string]any{
				"type":    "integer",
				"minimum": 0,
				"default": 20,
			},
		}, []string{"target"}),
		Deferred: true,
		Provider: ProviderAgent,
	},
}

// AgentSpecs 返回 agent 工具定义，顺序与 Python provider.tool_specs 一致
func AgentSpecs() []Spec { return append([]Spec(nil), agentSpecs...) }

// Lookup 按工具名查找 agent 工具定义
func Lookup(name string) (Spec, bool) {
	for _, spec := range agentSpecs {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

// AgentChatTools 是普通聊天可见的 agent 工具，对应 Python CHAT_TOOLS
func AgentChatTools() []string { return []string{"agent_talk", "agent_status"} }

// IsAgentChatTool 判断普通聊天是否可用该 agent 工具
func IsAgentChatTool(name string) bool {
	return name == "agent_talk" || name == "agent_status"
}
