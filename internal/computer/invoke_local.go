package computer

import (
	"context"
	"fmt"
)

// invokeLocalDesktop 本机模式下仅支持纯 shell 动作批次，用于开发/调试
func invokeLocalDesktop(ctx context.Context, args map[string]any) (InvokeResult, error) {
	parsed, err := parseActionBatch(args["actions"])
	if err != nil {
		return InvokeResult{Success: false, Error: err.Error()}, err
	}

	results := make([]map[string]any, 0, len(parsed))
	for i, action := range parsed {
		name, _ := action["action"].(string)
		if name != string(ActionShell) {
			return InvokeResult{
				Success: false,
				Error:   fmt.Sprintf("本机模式仅支持 shell 动作，actions[%d] 为 %q；桌面/截图仍走 Windows Sandbox", i, name),
			}, ErrNotImplemented
		}
		cmd, _ := action["command"].(string)
		if cmd == "" {
			return InvokeResult{Success: false, Error: fmt.Sprintf("actions[%d] 缺少 command", i)},
				fmt.Errorf("computer: actions[%d] 缺少 command", i)
		}
		out, err := runLocalShell(ctx, cmd)
		entry := map[string]any{
			"action":    string(ActionShell),
			"command":   cmd,
			"stdout":    out.Stdout,
			"stderr":    out.Stderr,
			"exit_code": out.ExitCode,
		}
		if err != nil {
			entry["error"] = err.Error()
			results = append(results, entry)
			return InvokeResult{
				Success: false,
				Payload: map[string]any{"results": results},
				Error:   err.Error(),
			}, err
		}
		results = append(results, entry)
	}

	return InvokeResult{
		Success: true,
		Payload: map[string]any{
			"success": true,
			"mode":    "local_host",
			"warning": "MAIBOT_COMPUTER_LOCAL=1：命令在宿主机执行，非 Windows Sandbox",
			"results": results,
		},
	}, nil
}

func parseActionBatch(raw any) ([]map[string]any, error) {
	if raw == nil {
		return nil, fmt.Errorf("computer: 缺少 actions")
	}
	switch list := raw.(type) {
	case []map[string]any:
		if len(list) == 0 {
			return nil, fmt.Errorf("computer: actions 不能为空")
		}
		return list, nil
	case []any:
		if len(list) == 0 {
			return nil, fmt.Errorf("computer: actions 不能为空")
		}
		out := make([]map[string]any, 0, len(list))
		for i, item := range list {
			action, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("computer: actions[%d] 不是对象", i)
			}
			out = append(out, action)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("computer: actions 必须为数组")
	}
}
