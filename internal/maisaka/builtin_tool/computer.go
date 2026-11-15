package tools

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/computer"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// ComputerToolBuilders 在 computer.enabled 或 MAIBOT_COMPUTER_LOCAL=1 时注册 MaiBot computer_* 工具名
func ComputerToolBuilders() []func() (tool.BaseTool, error) {
	if !computer.ToolsEnabled() {
		return nil
	}
	return []func() (tool.BaseTool, error){
		func() (tool.BaseTool, error) { return NewComputerDesktopTool() },
		func() (tool.BaseTool, error) { return NewComputerObserveTool() },
		func() (tool.BaseTool, error) { return NewComputerPollTool() },
		func() (tool.BaseTool, error) { return NewComputerCancelTool() },
	}
}

type computerDesktopInput struct {
	Actions []map[string]any `json:"actions" jsonschema:"description=桌面动作数组"`
	Timeout *float64         `json:"timeout,omitempty" jsonschema:"description=超时秒数，可选"`
}

type computerObserveInput struct {
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"description=超时秒数，可选"`
}

type computerPollInput struct {
	OperationID string   `json:"operation_id" jsonschema:"description=原操作 ID"`
	Timeout     *float64 `json:"timeout,omitempty" jsonschema:"description=等待秒数，可选"`
}

type computerCancelInput struct {
	OperationID string `json:"operation_id" jsonschema:"description=要取消的操作 ID"`
}

func computerToolSpec(name computer.ToolName) *computer.ToolSpec {
	for _, spec := range computer.ListTools(true) {
		if spec.Name == name {
			copy := spec
			return &copy
		}
	}
	return nil
}

func computerToolDescription(name computer.ToolName) string {
	if spec := computerToolSpec(name); spec != nil {
		return spec.Description
	}
	return string(name)
}

// newComputerEnhancedTool 把电脑结果同时作为文本与截图交给模型，对齐 provider.py 的 content_items
func newComputerEnhancedTool(name computer.ToolName, infer func() (tool.InvokableTool, error), decode func(*schema.ToolArgument) (map[string]any, error)) (tool.EnhancedInvokableTool, error) {
	plain, err := infer()
	if err != nil {
		return nil, err
	}
	info, err := plain.Info(context.Background())
	if err != nil {
		return nil, err
	}
	// InferTool 只能看到 Go 结构体，必须用 provider.tool_specs 的 oneOf 动作 schema 覆盖
	if spec := computerToolSpec(name); spec != nil && len(spec.ParametersSchema) > 0 {
		encoded, marshalErr := sonic.MarshalString(spec.ParametersSchema)
		if marshalErr == nil {
			parameterSchema := &jsonschema.Schema{}
			if unmarshalErr := sonic.UnmarshalString(encoded, parameterSchema); unmarshalErr == nil {
				info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(parameterSchema)
			}
		}
	}
	return &enhancedTool{info: info, run: func(ctx context.Context, argument *schema.ToolArgument) (*schema.ToolResult, error) {
		args, err := decode(argument)
		if err != nil {
			return nil, err
		}
		return runComputerInvoke(ctx, name, args)
	}}, nil
}

func runComputerInvoke(ctx context.Context, name computer.ToolName, args map[string]any) (*schema.ToolResult, error) {
	sessionID := ""
	if tc := GetToolContext(ctx); tc != nil {
		sessionID = tc.SessionID
		if sessionID == "" {
			sessionID = tc.RunID
		}
	}
	result, callErr := computer.Invoke(ctx, computer.InvokeRequest{
		Tool:      name,
		Arguments: args,
		SessionID: sessionID,
	})
	payload := result.Payload
	if payload == nil {
		payload = map[string]any{"success": result.Success}
	}
	if result.Error != "" {
		payload["error"] = result.Error
	}
	encoded, err := sonic.MarshalString(payload)
	if err != nil {
		return nil, fmt.Errorf("电脑工具结果序列化失败: %w", err)
	}
	parts := []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: encoded}}
	for _, image := range result.Images {
		encodedImage := image.Base64
		parts = append(parts, schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{
				Base64Data: &encodedImage,
				MIMEType:   image.MIMEType,
			}},
		})
	}
	return &schema.ToolResult{Parts: parts}, callErr
}

func decodeDesktopArgument(argument *schema.ToolArgument) (map[string]any, error) {
	input := &computerDesktopInput{}
	if argument != nil && strings.TrimSpace(argument.Text) != "" {
		if err := sonic.UnmarshalString(argument.Text, input); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
	}
	if input.Actions == nil {
		return nil, fmt.Errorf("参数不能为空")
	}
	args := map[string]any{"actions": any(input.Actions)}
	if input.Timeout != nil {
		args["timeout"] = *input.Timeout
	}
	return args, nil
}

func decodeObserveArgument(argument *schema.ToolArgument) (map[string]any, error) {
	input := &computerObserveInput{}
	if argument != nil && strings.TrimSpace(argument.Text) != "" {
		if err := sonic.UnmarshalString(argument.Text, input); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
	}
	args := map[string]any{}
	if input.Timeout != nil {
		args["timeout"] = *input.Timeout
	}
	return args, nil
}

func decodePollArgument(argument *schema.ToolArgument) (map[string]any, error) {
	input := &computerPollInput{}
	if argument != nil && strings.TrimSpace(argument.Text) != "" {
		if err := sonic.UnmarshalString(argument.Text, input); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
	}
	if strings.TrimSpace(input.OperationID) == "" {
		return nil, fmt.Errorf("operation_id 不能为空")
	}
	args := map[string]any{"operation_id": input.OperationID}
	if input.Timeout != nil {
		args["timeout"] = *input.Timeout
	}
	return args, nil
}

func decodeCancelArgument(argument *schema.ToolArgument) (map[string]any, error) {
	input := &computerCancelInput{}
	if argument != nil && strings.TrimSpace(argument.Text) != "" {
		if err := sonic.UnmarshalString(argument.Text, input); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
	}
	if strings.TrimSpace(input.OperationID) == "" {
		return nil, fmt.Errorf("operation_id 不能为空")
	}
	return map[string]any{"operation_id": input.OperationID}, nil
}

func NewComputerDesktopTool() (tool.EnhancedInvokableTool, error) {
	return newComputerEnhancedTool(computer.ToolDesktop, func() (tool.InvokableTool, error) {
		return utils.InferTool(
			string(computer.ToolDesktop),
			computerToolDescription(computer.ToolDesktop),
			func(ctx context.Context, input *computerDesktopInput) (map[string]any, error) {
				return map[string]any{"success": true}, nil
			},
		)
	}, decodeDesktopArgument)
}

func NewComputerObserveTool() (tool.EnhancedInvokableTool, error) {
	return newComputerEnhancedTool(computer.ToolObserve, func() (tool.InvokableTool, error) {
		return utils.InferTool(
			string(computer.ToolObserve),
			computerToolDescription(computer.ToolObserve),
			func(ctx context.Context, input *computerObserveInput) (map[string]any, error) {
				return map[string]any{"success": true}, nil
			},
		)
	}, decodeObserveArgument)
}

func NewComputerPollTool() (tool.EnhancedInvokableTool, error) {
	return newComputerEnhancedTool(computer.ToolPoll, func() (tool.InvokableTool, error) {
		return utils.InferTool(
			string(computer.ToolPoll),
			computerToolDescription(computer.ToolPoll),
			func(ctx context.Context, input *computerPollInput) (map[string]any, error) {
				return map[string]any{"success": true}, nil
			},
		)
	}, decodePollArgument)
}

func NewComputerCancelTool() (tool.EnhancedInvokableTool, error) {
	return newComputerEnhancedTool(computer.ToolCancel, func() (tool.InvokableTool, error) {
		return utils.InferTool(
			string(computer.ToolCancel),
			computerToolDescription(computer.ToolCancel),
			func(ctx context.Context, input *computerCancelInput) (map[string]any, error) {
				return map[string]any{"success": true}, nil
			},
		)
	}, decodeCancelArgument)
}
