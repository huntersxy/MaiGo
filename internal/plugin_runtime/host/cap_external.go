package host

import (
	"context"
	"strconv"
	"strings"

	"MaiGo/internal/plugin_runtime/protocol"

	"github.com/bytedance/sonic"
)

func makeFrequencyHandler(port PluginFrequencyPort, capability string) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		chatID := strings.TrimSpace(coalesceString(args, "chat_id"))
		if chatID == "" {
			return capabilityFailure("缺少必要参数 chat_id"), nil
		}
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		switch capability {
		case "frequency.get_current_talk_value":
			value, err := port.CurrentTalkValue(ctx, chatID)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return map[string]any{"success": true, "value": value}, nil
		case "frequency.set_adjust":
			raw, exists := args["value"]
			if !exists || raw == nil {
				return capabilityFailure("缺少必要参数 chat_id 或 value"), nil
			}
			value, err := coerceFloat64(raw)
			if err != nil {
				return capabilityFailure("value 必须是数字"), nil
			}
			if err := port.SetAdjust(ctx, chatID, value); err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return map[string]any{"success": true}, nil
		case "frequency.get_adjust":
			value, err := port.GetAdjust(ctx, chatID)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return map[string]any{"success": true, "value": value}, nil
		default:
			return nil, &CapabilityError{Code: "E_METHOD_NOT_ALLOWED", Message: "未注册的能力: " + capability}
		}
	}
}

func makeToolDefinitionsHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		if host == nil {
			return capabilityFailure("Host 未初始化"), nil
		}
		tools := []map[string]any{}
		for _, info := range host.Components().ToolsForLLM(false, "") {
			definition := map[string]any{
				"name":        info.Name,
				"description": info.Description,
				"parameters":  info.Parameters,
			}
			tools = append(tools, map[string]any{"name": info.Name, "definition": definition})
		}
		return map[string]any{"success": true, "tools": tools}, nil
	}
}

func makeKnowledgeSearchHandler(port PluginKnowledgePort) CapabilityHandler {
	return func(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		result, err := port.Search(ctx, pluginID, args)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return result, nil
	}
}

func makeMarketHandler(port PluginMarketPort, capability string) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		var (
			result any
			err    error
		)
		switch capability {
		case "market.search":
			result, err = port.Search(ctx, args)
		case "market.get_detail":
			result, err = port.GetDetail(ctx, args)
		case "market.get_readme":
			result, err = port.GetReadme(ctx, args)
		default:
			return nil, &CapabilityError{Code: "E_METHOD_NOT_ALLOWED", Message: "未注册的能力: " + capability}
		}
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return result, nil
	}
}

func makeWebHandler(port PluginWebPort, capability string) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		var (
			result map[string]any
			err    error
		)
		if capability == "web.search" {
			result, err = port.Search(ctx, pluginID, args)
		} else {
			result, err = port.Fetch(ctx, pluginID, args)
		}
		if err != nil {
			if coder, ok := err.(interface{ ErrorCode() string }); ok && strings.TrimSpace(coder.ErrorCode()) != "" {
				return nil, &protocol.RPCError{Code: protocol.ErrorCode(coder.ErrorCode()), Message: err.Error()}
			}
			return capabilityFailure("%s", err.Error()), nil
		}
		if pluginWebResultTooLarge(result) {
			return capabilityFailure("联网结果超过 500000 字符的传输限制，请缩小查询范围"), nil
		}
		return result, nil
	}
}

// pluginWebMaxResultChars 对齐 Python web capability 的传输上限
const pluginWebMaxResultChars = 500000

// pluginWebResultTooLarge 按最终 capability 结果的 JSON 字符数判断是否超限
func pluginWebResultTooLarge(result map[string]any) bool {
	encoded, err := sonic.MarshalString(map[string]any{"success": true, "result": result})
	if err != nil {
		return false
	}
	return len([]rune(encoded)) > pluginWebMaxResultChars
}

func makeStatisticsHandler(port PluginStatisticsPort) CapabilityHandler {
	return func(ctx context.Context, _, capability string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		result, err := port.Invoke(ctx, capability, args)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return result, nil
	}
}

func makeRenderHandler(port PluginRenderPort) CapabilityHandler {
	return func(ctx context.Context, _, capability string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		width, height := renderViewport(args)
		request := PluginRenderRequest{
			HTML:              coalesceString(args, "html"),
			Selector:          firstNonEmptyText(coalesceString(args, "selector"), "body"),
			ViewportWidth:     width,
			ViewportHeight:    height,
			DeviceScaleFactor: renderDeviceScaleFactor(args),
			FullPage:          renderBool(args, "full_page", false),
			OmitBackground:    renderBool(args, "omit_background", false),
			WaitUntil:         firstNonEmptyText(coalesceString(args, "wait_until"), "load"),
			WaitForSelector:   coalesceString(args, "wait_for_selector"),
			WaitForTimeoutMS:  max(int(coalesceFloat(args, "wait_for_timeout_ms", 0)), 0),
			TimeoutMS:         renderTimeoutMS(args),
			AllowNetwork:      renderBool(args, "allow_network", false),
		}
		result, err := port.RenderHTMLToPNG(ctx, request)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "result": result}, nil
	}
}

// renderViewport 对齐 Python capability 的嵌套 viewport 参数
func renderViewport(args map[string]any) (int, int) {
	width, height := 900, 500
	if viewport, ok := args["viewport"].(map[string]any); ok {
		if value, ok := renderNumericArg(viewport, "width"); ok {
			width = max(int(value), 1)
		}
		if value, ok := renderNumericArg(viewport, "height"); ok {
			height = max(int(value), 1)
		}
	}
	return width, height
}

func renderBool(args map[string]any, key string, defaultValue bool) bool {
	raw, ok := args[key]
	if !ok || raw == nil {
		return defaultValue
	}
	if text, ok := raw.(string); ok {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "0", "false", "no", "off":
			return false
		case "1", "true", "yes", "on":
			return true
		}
	}
	return truthyPayloadValue(raw)
}

func renderDeviceScaleFactor(args map[string]any) float64 {
	value, ok := renderNumericArg(args, "device_scale_factor")
	if !ok {
		return 2.0
	}
	if value < 1 {
		return 1
	}
	return value
}

// renderNumericArg 读取数值参数；缺失、null 或无法解析时返回 ok=false
func renderNumericArg(args map[string]any, key string) (float64, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func renderTimeoutMS(args map[string]any) int {
	if raw, ok := args["render_timeout_ms"]; ok && raw != nil {
		return max(int(coalesceFloat(args, "render_timeout_ms", 0)), 0)
	}
	if raw, ok := args["timeout_ms"]; ok && raw != nil {
		return max(int(coalesceFloat(args, "timeout_ms", 0)), 0)
	}
	return 0
}

func makeMaisakaHandler(host *Host, port PluginMaisakaPort) CapabilityHandler {
	return func(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
		if host != nil && host.MaisakaPort() != nil {
			port = host.MaisakaPort()
		}
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		result, err := port.Invoke(ctx, capability, pluginID, args)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return result, nil
	}
}

func makeWebUIClaimUploadHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		if deps.Uploads == nil {
			return pluginPortNotWired("webui.claim_upload"), nil
		}
		if deps.PluginDataDir == nil {
			return pluginPortNotWired("webui.claim_upload"), nil
		}
		token := strings.TrimSpace(coalesceString(args, "upload_id"))
		dataDir, err := deps.PluginDataDir(pluginID)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		upload, err := deps.Uploads.Claim(pluginID, token, dataDir)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "upload": upload}, nil
	}
}
