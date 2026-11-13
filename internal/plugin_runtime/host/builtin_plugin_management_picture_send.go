package host

import (
	"context"
	"fmt"
	"strings"
)

func pluginStyleDisplayValue(value any) string {
	if value == nil {
		return "None"
	}
	return fmt.Sprint(value)
}

func strictPluginWebUIStyle(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed >= 0 && typed <= 2
	case int8:
		return int(typed), typed >= 0 && typed <= 2
	case int16:
		return int(typed), typed >= 0 && typed <= 2
	case int32:
		return int(typed), typed >= 0 && typed <= 2
	case int64:
		return int(typed), typed >= 0 && typed <= 2
	case uint:
		return int(typed), typed <= 2
	case uint8:
		return int(typed), typed <= 2
	case uint16:
		return int(typed), typed <= 2
	case uint32:
		return int(typed), typed <= 2
	case uint64:
		return int(typed), typed <= 2
	default:
		return 0, false
	}
}

func invokePluginCapability(ctx context.Context, host *Host, capability string, args map[string]any) (map[string]any, error) {
	if host == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	value, err := host.InvokeCapability(ctx, builtinPluginManagementID, capability, args)
	if err != nil {
		return nil, err
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("能力 %s 返回了非字典结果", capability)
	}
	return result, nil
}

// sendCommandCatalogPicture 按 Python plugin.py 的 cmd pic 流程渲染并发送图片
func (s *pluginManagementState) sendCommandCatalogPicture(ctx context.Context, host *Host, streamID string, groups []pluginManagementCommandGroup) bool {
	styleValue := any(nil)
	if configResult, err := invokePluginCapability(ctx, host, "config.get", map[string]any{"key": "webui.webui_style"}); err == nil {
		if truthyPayloadValue(configResult["success"]) {
			styleValue = configResult["value"]
		} else {
			styleValue = configResult["error"]
		}
	} else {
		styleValue = err.Error()
	}
	style, ok := strictPluginWebUIStyle(styleValue)
	if !ok {
		_ = s.sendText(ctx, host, streamID, "无法读取有效的 WebUI 界面风格: "+pluginStyleDisplayValue(styleValue))
		return false
	}
	pages, err := buildManagementCommandPages(groups, style)
	if err != nil {
		_ = s.sendText(ctx, host, streamID, "指令列表图片生成失败: "+err.Error())
		return false
	}
	images := make([]string, 0, len(pages))
	for _, page := range pages {
		result, err := invokePluginCapability(ctx, host, "render.html2png", map[string]any{
			"html":                page,
			"viewport":            map[string]any{"width": 1000, "height": 600},
			"full_page":           true,
			"device_scale_factor": 1.5,
		})
		if err != nil {
			_ = s.sendText(ctx, host, streamID, "指令列表图片生成失败: "+err.Error())
			return false
		}
		if !truthyPayloadValue(result["success"]) {
			_ = s.sendText(ctx, host, streamID, "指令列表图片生成失败: "+firstNonEmptyText(stringValue(result["error"]), "render.html2png 未返回结果"))
			return false
		}
		rendered, _ := result["result"].(map[string]any)
		image := strings.TrimSpace(stringValue(rendered["image_base64"]))
		if image == "" {
			_ = s.sendText(ctx, host, streamID, fmt.Sprintf("指令列表图片生成失败: %v", rendered))
			return false
		}
		images = append(images, image)
	}
	sent := false
	if len(images) == 1 {
		result, err := invokePluginCapability(ctx, host, "send.image", map[string]any{
			"image_base64": images[0],
			"stream_id":    streamID,
		})
		sent = err == nil && truthyPayloadValue(result["success"])
	} else {
		messages := make([]any, 0, len(images))
		for _, image := range images {
			messages = append(messages, map[string]any{
				"nickname": "插件指令列表",
				"segments": []any{map[string]any{"type": "image", "content": image}},
			})
		}
		result, err := invokePluginCapability(ctx, host, "send.forward", map[string]any{
			"messages":  messages,
			"stream_id": streamID,
		})
		sent = err == nil && truthyPayloadValue(result["success"])
	}
	if !sent {
		_ = s.sendText(ctx, host, streamID, "指令列表图片发送失败")
		return false
	}
	return true
}
