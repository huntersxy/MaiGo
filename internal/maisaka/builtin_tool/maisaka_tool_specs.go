package tools

import (
	"context"
	"fmt"
	"sync"

	"MaiGo/internal/mcp_module"
	"MaiGo/internal/services/schedules"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// MaisakaToolSpec maisaka 内置工具的 Python 声明
type MaisakaToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

type maisakaToolSpecPayload struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"parameters_schema"`
}

var (
	maisakaSpecsOnce sync.Once
	maisakaSpecs     map[string]MaisakaToolSpec
	maisakaSpecsErr  error
)

func loadMaisakaToolSpecs() (map[string]MaisakaToolSpec, error) {
	maisakaSpecsOnce.Do(func() {
		payload := map[string]maisakaToolSpecPayload{}
		if err := sonic.UnmarshalString(maisakaToolSpecsJSON, &payload); err != nil {
			maisakaSpecsErr = fmt.Errorf("内置工具声明解析失败: %w", err)
			return
		}
		loaded := make(map[string]MaisakaToolSpec, len(payload))
		for key, item := range payload {
			if item.Name == "" {
				maisakaSpecsErr = fmt.Errorf("内置工具声明缺少 name: %s", key)
				return
			}
			loaded[item.Name] = MaisakaToolSpec{
				Name:        item.Name,
				Description: item.Description,
				Schema:      item.Schema,
			}
		}
		loaded["schedule"] = scheduleToolSpec(false)
		maisakaSpecs = loaded
	})
	return maisakaSpecs, maisakaSpecsErr
}

// MaisakaToolSpecFor 返回指定内置工具的 Python 逐字声明
func MaisakaToolSpecFor(name string) (MaisakaToolSpec, bool) {
	specs, err := loadMaisakaToolSpecs()
	if err != nil {
		return MaisakaToolSpec{}, false
	}
	spec, ok := specs[name]
	return spec, ok
}

// MaisakaToolSpecNames 返回全部内置工具名，便于测试与对账
func MaisakaToolSpecNames() []string {
	specs, err := loadMaisakaToolSpecs()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	return names
}

// ToolInfo 由 Python 声明构造 eino 工具定义
func (s MaisakaToolSpec) ToolInfo() (*schema.ToolInfo, error) {
	encoded, err := sonic.MarshalString(s.Schema)
	if err != nil {
		return nil, fmt.Errorf("工具 %s 参数结构序列化失败: %w", s.Name, err)
	}
	parameterSchema := &jsonschema.Schema{}
	if err := sonic.UnmarshalString(encoded, parameterSchema); err != nil {
		return nil, fmt.Errorf("工具 %s 参数结构解析失败: %w", s.Name, err)
	}
	return &schema.ToolInfo{
		Name:        s.Name,
		Desc:        s.Description,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parameterSchema),
	}, nil
}

// maisakaSpecTool 只替换模型可见声明，执行仍走既有实现
type maisakaSpecTool struct {
	base tool.InvokableTool
	spec MaisakaToolSpec
}

// Info 返回 Python 对齐后的工具定义；reply 按当前会话配置动态裁剪或补充参数
func (t *maisakaSpecTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.spec.ToolInfoForContext(ctx)
}

// InvokableRun 转发到既有实现
func (t *maisakaSpecTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return t.base.InvokableRun(ctx, argumentsInJSON, opts...)
}

// InvokeStructuredToolResult 保留 Python 结果语义；既有工具只返回 Go 原生 JSON，这里统一转换
func (t *maisakaSpecTool) InvokeStructuredToolResult(ctx context.Context, argumentsInJSON string) (StructuredToolResult, error) {
	raw, err := t.base.InvokableRun(ctx, argumentsInJSON)
	if err != nil {
		return StructuredToolResult{Success: false, ErrorMessage: err.Error()}, nil
	}
	return convertMaisakaToolResult(ctx, t.spec.Name, raw)
}

// maisakaSpecResultTool 在声明覆盖之外保留底层工具的完整结果能力
// 只有底层实现 mcp.ToolResultInvoker 时才使用，避免其他工具被误判为结构化结果工具
type maisakaSpecResultTool struct {
	*maisakaSpecTool
	invoker mcp.ToolResultInvoker
}

// InvokeToolResult 透传底层工具结果，保留图片等内容项
func (t *maisakaSpecResultTool) InvokeToolResult(ctx context.Context, argumentsInJSON string) (mcp.ToolExecutionResult, error) {
	return t.invoker.InvokeToolResult(ctx, argumentsInJSON)
}

// ToolInfoForContext 按当前会话配置生成模型可见声明
func (s MaisakaToolSpec) ToolInfoForContext(ctx context.Context) (*schema.ToolInfo, error) {
	switch s.Name {
	case "schedule":
		tc := GetToolContext(ctx)
		return scheduleToolSpec(tc != nil && schedules.Default().IsOwner(ctx, tc.SessionID)).ToolInfo()
	case "reply":
		s.Schema = replySpecSchemaForContext(s.Schema, ctx)
		return s.ToolInfo()
	default:
		return s.ToolInfo()
	}
}

// replySpecSchemaForContext 对齐 Python reply.py:145-226 的动态参数门控
func replySpecSchemaForContext(base map[string]any, ctx context.Context) map[string]any {
	encoded, err := sonic.MarshalString(base)
	if err != nil {
		return base
	}
	clone := map[string]any{}
	if err := sonic.UnmarshalString(encoded, &clone); err != nil {
		return base
	}
	properties, _ := clone["properties"].(map[string]any)
	if properties == nil {
		return clone
	}
	tc := GetToolContext(ctx)
	if tc == nil {
		return clone
	}
	if !tc.UseVectorExpression {
		delete(properties, "expression_intent")
	}
	if tc.EnableReplyAt && tc.ConversationKind == "group" {
		properties["attach_at"] = map[string]any{
			"type":        "array",
			"description": "可选。随本次回复 at 一个或多个目标消息的发送者，填写目标 msg_id。",
			"items":       map[string]any{"type": "string"},
			"default":     []any{},
		}
	}
	return clone
}

// ApplyMaisakaToolSpec 用 Python 声明覆盖内置工具的模型可见定义
func ApplyMaisakaToolSpec(name string, base tool.InvokableTool) (tool.InvokableTool, error) {
	spec, ok := MaisakaToolSpecFor(name)
	if !ok {
		return nil, fmt.Errorf("缺少内置工具声明: %s", name)
	}
	if _, err := spec.ToolInfo(); err != nil {
		return nil, err
	}
	wrapped := &maisakaSpecTool{base: base, spec: spec}
	if invoker, ok := base.(mcp.ToolResultInvoker); ok {
		return &maisakaSpecResultTool{maisakaSpecTool: wrapped, invoker: invoker}, nil
	}
	return wrapped, nil
}

// maisakaSpecBuilder 包装构造器，使工具声明与 Python 逐字一致
func maisakaSpecBuilder(name string, build func() (tool.InvokableTool, error)) func() (tool.BaseTool, error) {
	return func() (tool.BaseTool, error) {
		base, err := build()
		if err != nil {
			return nil, err
		}
		return ApplyMaisakaToolSpec(name, base)
	}
}
