package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/agent/files"
	"MaiGo/internal/core/localoperator"
	"MaiGo/internal/files"
	"MaiGo/internal/mcp_module"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// AgentFileToolBuilders 返回主聊天可用的 agent_* 与 files_* 工具构造器
// agent 工具对齐 Python 普通聊天只可见 agent_talk/agent_status，且初始为 deferred；
// files 工具来自 src/files/provider.py（本仓 internal/files/provider.go）。
func AgentFileToolBuilders(agentEnabled, filesEnabled bool, policy AgentChatPolicy) []func() (tool.BaseTool, error) {
	builders := make([]func() (tool.BaseTool, error), 0, 7)
	if agentEnabled {
		for _, name := range agentfiles.AgentChatTools() {
			name := name
			builders = append(builders, func() (tool.BaseTool, error) {
				return newAgentFileTool(name, policy)
			})
		}
	}
	if filesEnabled {
		for _, spec := range filesvc.FileToolSpecs() {
			name := spec.Name
			builders = append(builders, func() (tool.BaseTool, error) {
				return newFileTool(name)
			})
		}
	}
	return builders
}

// AgentChatPolicy 外接 Agent 工具的聊天白名单，对齐 AgentToolProvider._allowed_chat
type AgentChatPolicy struct {
	AgentID     string
	AllowUsers  []string
	AllowGroups []string
}

// Allows 判断当前聊天能否指挥外接 Agent：Agent 自身会话始终放行，白名单全空时不限制
func (p AgentChatPolicy) Allows(tc *ToolContext) bool {
	if tc != nil && strings.TrimSpace(tc.SessionID) != "" && tc.SessionID == p.AgentID {
		return true
	}
	if len(p.AllowUsers) == 0 && len(p.AllowGroups) == 0 {
		return true
	}
	if tc == nil {
		return false
	}
	target := ""
	if tc.TargetID != 0 {
		target = strconv.FormatInt(tc.TargetID, 10)
	}
	scoped := localoperator.BuildScopedUserID(tc.Platform, target)
	if scoped == "" {
		return false
	}
	if tc.ConversationKind == "group" {
		_, allowed := localoperator.NormalizeOperatorPermissions(p.AllowGroups)[scoped]
		return allowed
	}
	_, allowed := localoperator.NormalizeOperatorPermissions(p.AllowUsers)[scoped]
	return allowed
}

// AgentChatToolVisible 对齐 list_tools 的白名单过滤：不允许时工具不出现在可见列表
func AgentChatToolVisible(ctx context.Context, name string, policy AgentChatPolicy) bool {
	if !agentfiles.IsAgentChatTool(name) {
		return true
	}
	return policy.Allows(GetToolContext(ctx))
}

// agentFileTool 同时承载 agent_* 与 files_* 两类工具，分发时按来源字段区分
type agentFileTool struct {
	spec     agentfiles.Spec
	fileSpec filesvc.ToolSpec
	isFile   bool
	policy   AgentChatPolicy
	info     *schema.ToolInfo
}

func newAgentFileTool(name string, policy AgentChatPolicy) (tool.BaseTool, error) {
	spec, ok := agentfiles.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("未知的 agent 工具: %s", name)
	}
	info, err := toolInfoFromSchema(spec.Name, spec.Description, spec.Schema)
	if err != nil {
		return nil, err
	}
	return &agentFileTool{spec: spec, policy: policy, info: info}, nil
}

func newFileTool(name string) (tool.BaseTool, error) {
	spec, ok := filesvc.LookupFileTool(name)
	if !ok {
		return nil, fmt.Errorf("未知的文件工具: %s", name)
	}
	info, err := toolInfoFromSchema(spec.Name, spec.Description, spec.Schema)
	if err != nil {
		return nil, err
	}
	return &agentFileTool{fileSpec: spec, isFile: true, info: info}, nil
}

func toolInfoFromSchema(name, description string, schemaValue map[string]any) (*schema.ToolInfo, error) {
	encoded, err := sonic.MarshalString(schemaValue)
	if err != nil {
		return nil, fmt.Errorf("工具 %s 参数结构序列化失败: %w", name, err)
	}
	parameterSchema := &jsonschema.Schema{}
	if err := sonic.UnmarshalString(encoded, parameterSchema); err != nil {
		return nil, fmt.Errorf("工具 %s 参数结构解析失败: %w", name, err)
	}
	return &schema.ToolInfo{
		Name:        name,
		Desc:        description,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parameterSchema),
	}, nil
}

// Info 返回工具定义
func (t *agentFileTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

// IsDeferredTool 对齐 Python metadata.visibility=deferred（仅 agent 工具）
func (t *agentFileTool) IsDeferredTool() bool { return t.spec.Deferred }

// InvokableRun 供 eino 回退路径调用，只返回文本
func (t *agentFileTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	result := t.invoke(ctx, arguments)
	if result.Failed {
		return "", fmt.Errorf("%s", result.ErrorMessage)
	}
	return result.Content, nil
}

// InvokeToolResult 供 Maisaka 主路径调用，保留结构化结果与图片
func (t *agentFileTool) InvokeToolResult(ctx context.Context, arguments string) (mcp.ToolExecutionResult, error) {
	result := t.invoke(ctx, arguments)
	if result.Failed {
		return mcp.ToolExecutionResult{Success: false, ErrorMessage: result.ErrorMessage}, nil
	}
	items := make([]mcp.ToolContentItem, 0, len(result.Images))
	for _, image := range result.Images {
		items = append(items, mcp.ToolContentItem{ContentType: "image", Data: image.Data, MIMEType: image.MIMEType})
	}
	return mcp.ToolExecutionResult{
		Success:           true,
		Content:           result.Content,
		StructuredContent: result.Structured,
		ContentItems:      items,
	}, nil
}

func (t *agentFileTool) invoke(ctx context.Context, arguments string) agentfiles.Result {
	args := map[string]any{}
	trimmed := strings.TrimSpace(arguments)
	if trimmed != "" && trimmed != "{}" {
		if err := sonic.UnmarshalString(trimmed, &args); err != nil {
			return agentfiles.Result{Failed: true, ErrorMessage: fmt.Sprintf("参数不是 JSON 对象: %v", err)}
		}
	}
	var tc *ToolContext
	if value := GetToolContext(ctx); value != nil {
		tc = value
	}
	if t.isFile {
		if tc == nil || tc.FileToolHost == nil {
			return agentfiles.Result{Failed: true, ErrorMessage: "文件工具需要可信聊天上下文"}
		}
		payload, err := filesvc.InvokeFileTool(ctx, t.fileSpec.Name, args, tc.FileToolHost)
		if err != nil {
			return agentfiles.Result{Failed: true, ErrorMessage: err.Error()}
		}
		encoded, err := sonic.MarshalString(payload)
		if err != nil {
			return agentfiles.Result{Failed: true, ErrorMessage: fmt.Sprintf("工具结果序列化失败: %v", err)}
		}
		return agentfiles.Result{Content: encoded, Structured: payload}
	}
	var host agentfiles.Host
	if tc != nil {
		if !t.policy.Allows(tc) {
			return agentfiles.Result{Failed: true, ErrorMessage: "当前聊天没有指挥外接 Agent 的权限。"}
		}
		host = tc.AgentFilesHost
	}
	return agentfiles.Invoke(ctx, t.spec.Name, args, host)
}
