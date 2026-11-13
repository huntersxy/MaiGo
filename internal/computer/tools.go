package computer

// 四个内置电脑工具：参数解析与结果投影对齐 provider.py，执行委托给共享 Service

import (
	"context"
	"fmt"
	"strings"
)

// ToolName 内置电脑工具名，对齐 provider.tool_specs
type ToolName string

const (
	ToolDesktop ToolName = "computer_desktop"
	ToolObserve ToolName = "computer_observe"
	ToolPoll    ToolName = "computer_poll"
	ToolCancel  ToolName = "computer_cancel"
)

// ToolSpec 供 Agent/Planner 注册的静态描述
type ToolSpec struct {
	Name             ToolName
	Description      string
	Provider         string
	Visibility       string // deferred / visible
	ParametersSchema map[string]any
	Required         []string
}

var numberProperty = map[string]any{"type": "number", "minimum": 0.0}

func desktopToolSpec() ToolSpec {
	schemas := make([]any, 0, len(ActionSchemas()))
	for _, schema := range ActionSchemas() {
		schemas = append(schemas, schema)
	}
	return ToolSpec{
		Name: ToolDesktop, Provider: "computer", Visibility: "deferred",
		Description: "在 Windows Sandbox 内批量操作并返回截图。先 observe，坐标按最新截图。观察用 computer_observe，查询操作用 computer_poll。" +
			"动作：observe；move/click/double_click(x,y,button)；drag(x,y,end_x,end_y,seconds,button)；mouse_down/mouse_up(button)；scroll(delta)；" +
			"keys/key_down/key_up(keys 数组，Windows 键名，keys 可加 seconds 持续按住)；type(text)；open(path,arguments 数组，任意沙盒程序或 URL)；" +
			"shell(command，沙盒内任意 PowerShell，返回 stdout/stderr/exit_code，工作目录 C:\\MaiWorkspace)；wait(seconds)。" +
			"inspect(depth,limit) 读取 UIA；uia(runtime_id,operation,value) 操作控件；browser(method,target,params) 调用沙盒 CDP；" +
			"wait_window(title,seconds)、wait_process(pid,seconds) 等待具体条件。" +
			"没有探索步数、时长或应用名单限制。timeout 默认60秒，0一直等待；超时返回 running，用 computer_poll 查询，不重复输入。" +
			localModeToolSuffix(),
		ParametersSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"actions": map[string]any{"type": "array", "items": map[string]any{"oneOf": schemas}, "minItems": 1},
				"timeout": numberProperty,
			},
			"required":             []any{"actions"},
			"additionalProperties": false,
		},
		Required: []string{"actions"},
	}
}

func localModeToolSuffix() string {
	if !LocalModeEnabled() {
		return ""
	}
	return " 【本机调试】MAIBOT_COMPUTER_LOCAL=1 且沙盒未启用时 computer_desktop 仅支持纯 shell 批次且在宿主机执行。"
}

func toolSpecs() []ToolSpec {
	return []ToolSpec{
		desktopToolSpec(),
		{
			Name: ToolObserve, Provider: "computer", Visibility: "deferred",
			Description: "独立获取当前桌面，长命令执行期间也可观察；返回截图编号、时间、活动窗口及尺寸。",
			ParametersSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"timeout": numberProperty},
				"required":             []any{},
				"additionalProperties": false,
			},
		},
		{
			Name: ToolPoll, Provider: "computer", Visibility: "deferred",
			Description: "查询原操作，不重复执行。running 是正常等待，completed/failed/cancelled 是最终结果。",
			ParametersSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"operation_id": map[string]any{"type": "string"},
					"timeout":      numberProperty,
				},
				"required":             []any{"operation_id"},
				"additionalProperties": false,
			},
			Required: []string{"operation_id"},
		},
		{
			Name: ToolCancel, Provider: "computer", Visibility: "deferred",
			Description: "取消指定操作并释放按键，不关闭桌面；已完成的动作不会回滚。running 表示尚未确认取消。",
			ParametersSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"operation_id": map[string]any{"type": "string"},
				},
				"required":             []any{"operation_id"},
				"additionalProperties": false,
			},
			Required: []string{"operation_id"},
		},
	}
}

// ListTools 返回已定义工具；enabled=false 时与 Python 一致返回空列表
func ListTools(enabled bool) []ToolSpec {
	if !enabled {
		return nil
	}
	return toolSpecs()
}

// InvokeRequest 工具调用参数
type InvokeRequest struct {
	Tool      ToolName
	Arguments map[string]any
	SessionID string
	Reason    string
}

// ImageContent 工具结果中的截图，供 Agent 多模态输出使用
type ImageContent struct {
	Base64    string
	MIMEType  string
	CaptureID string
}

// InvokeResult 工具执行结果
type InvokeResult struct {
	Success bool
	Payload map[string]any
	Error   string
	Images  []ImageContent
}

// Invoke 执行电脑工具；无沙盒且未开启本机调试时返回真实错误
func Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if req.SessionID == "" {
		return InvokeResult{Success: false, Error: "电脑工具需要可信执行上下文。"},
			fmt.Errorf("电脑工具需要可信执行上下文。")
	}
	service := DefaultService()
	switch req.Tool {
	case ToolDesktop:
		actions, err := parseActionBatch(req.Arguments["actions"])
		if err != nil {
			return InvokeResult{Success: false, Error: err.Error()}, err
		}
		if LocalModeEnabled() && !service.Enabled() {
			return invokeLocalDesktop(ctx, req.Arguments)
		}
		timeout, err := optionalTimeout(req.Arguments)
		if err != nil {
			return InvokeResult{Success: false, Error: err.Error()}, err
		}
		result, err := service.Desktop(ctx, actions, timeout, false, req.Reason, req.SessionID)
		return projectInvokeResult(result, err)
	case ToolObserve:
		timeout, err := optionalTimeout(req.Arguments)
		if err != nil {
			return InvokeResult{Success: false, Error: err.Error()}, err
		}
		result, err := service.Observe(ctx, timeout)
		return projectInvokeResult(result, err)
	case ToolPoll:
		operationID, _ := req.Arguments["operation_id"].(string)
		if strings.TrimSpace(operationID) == "" {
			err := fmt.Errorf("operation_id 不能为空")
			return InvokeResult{Success: false, Error: err.Error()}, err
		}
		timeout, err := optionalTimeout(req.Arguments)
		if err != nil {
			return InvokeResult{Success: false, Error: err.Error()}, err
		}
		result, err := service.Poll(ctx, strings.TrimSpace(operationID), timeout)
		return projectInvokeResult(result, err)
	case ToolCancel:
		operationID, _ := req.Arguments["operation_id"].(string)
		if strings.TrimSpace(operationID) == "" {
			err := fmt.Errorf("operation_id 不能为空")
			return InvokeResult{Success: false, Error: err.Error()}, err
		}
		result, err := service.Cancel(ctx, strings.TrimSpace(operationID))
		return projectInvokeResult(result, err)
	default:
		err := fmt.Errorf("未知电脑工具：%s", req.Tool)
		return InvokeResult{Success: false, Error: err.Error()}, ErrUnknownTool
	}
}

func projectInvokeResult(result map[string]any, callErr error) (InvokeResult, error) {
	output := InvokeResult{}
	if result != nil {
		output.Payload = make(map[string]any, len(result))
		for key, value := range result {
			if key == "image" {
				continue
			}
			output.Payload[key] = value
		}
		if success, ok := result["success"].(bool); ok {
			output.Success = success
		} else {
			output.Success = true
		}
		if image, ok := result["image"].(string); ok && image != "" {
			mime, _ := result["mime_type"].(string)
			if mime == "" {
				mime = "image/png"
			}
			captureID, _ := result["capture_id"].(string)
			output.Images = append(output.Images, ImageContent{Base64: image, MIMEType: mime, CaptureID: captureID})
		}
		parts := make([]string, 0, 2)
		for _, key := range []string{"error", "capture_error"} {
			if text, ok := result[key].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
		output.Error = strings.Join(parts, "\n")
	}
	if callErr != nil {
		if output.Error == "" {
			output.Error = callErr.Error()
		}
		output.Success = false
		return output, callErr
	}
	return output, nil
}

func optionalTimeout(arguments map[string]any) (*float64, error) {
	raw, exists := arguments["timeout"]
	if !exists || raw == nil {
		return nil, nil
	}
	value, ok := numberValue(raw)
	if !ok || value < 0 || !isFiniteNumber(raw) {
		return nil, fmt.Errorf("timeout 必须为非负数")
	}
	return &value, nil
}

// ExecuteCommand 对齐 bridge 层 shell；默认不执行任何宿主机命令
func ExecuteCommand(ctx context.Context, command string) (CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return CommandResult{}, fmt.Errorf("computer: 命令不能为空")
	}
	if LocalModeEnabled() && !DefaultService().Enabled() {
		return runLocalShell(ctx, command)
	}
	service := DefaultService()
	if !service.Enabled() {
		return CommandResult{}, ErrNotImplemented
	}
	result, err := service.Desktop(ctx, []map[string]any{{"action": string(ActionShell), "command": command}}, nil, false, "", "computer.ExecuteCommand")
	if err != nil {
		return CommandResult{}, err
	}
	return commandResultFromSandbox(result), nil
}

func commandResultFromSandbox(result map[string]any) CommandResult {
	commandResult := CommandResult{}
	if value, ok := result["stdout"].(string); ok {
		commandResult.Stdout = value
	}
	if value, ok := result["stderr"].(string); ok {
		commandResult.Stderr = value
	}
	if value, ok := numberValue(result["exit_code"]); ok {
		commandResult.ExitCode = int(value)
	}
	if commandResult.Stdout != "" || commandResult.Stderr != "" || commandResult.ExitCode != 0 {
		return commandResult
	}
	items, _ := result["results"].([]any)
	for index := len(items) - 1; index >= 0; index-- {
		entry, _ := items[index].(map[string]any)
		inner, _ := entry["result"].(map[string]any)
		if inner == nil {
			continue
		}
		if value, ok := inner["stdout"].(string); ok {
			commandResult.Stdout = value
		}
		if value, ok := inner["stderr"].(string); ok {
			commandResult.Stderr = value
		}
		if value, ok := numberValue(inner["exit_code"]); ok {
			commandResult.ExitCode = int(value)
		}
		return commandResult
	}
	return commandResult
}
