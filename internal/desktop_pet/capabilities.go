package desktoppet

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"maibot-go/internal/plugin_runtime"
)

// 桌宠控制能力，对齐 capabilities/registry.py 的 control 注册表
var controlCapabilityActions = []string{"status", "show", "hide", "say", "set_thinking", "refresh_buttons"}

// 桌宠动画能力，对齐 ANIMATION_ACTIONS
var animationCapabilityActions = []string{"get_skin", "list_animations", "play_animation", "play_event", "stop_animation", "animation_status"}

// RegisterCapabilities 把 desktop_pet.* 能力注册到插件 Host，对齐 register_capability_impls
func RegisterCapabilities(host *pluginruntime.Host, svc *Service) ([]string, error) {
	if host == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	if svc == nil {
		return nil, fmt.Errorf("桌宠服务未初始化")
	}
	names := make([]string, 0, len(controlCapabilityActions)+len(animationCapabilityActions))
	for _, action := range controlCapabilityActions {
		action := action
		name := "desktop_pet." + action
		err := host.RegisterCapability(name, func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
			return runControlCapability(ctx, svc, action, args)
		})
		if err != nil {
			return names, err
		}
		names = append(names, name)
	}
	for _, action := range animationCapabilityActions {
		action := action
		name := "desktop_pet." + action
		err := host.RegisterCapability(name, func(ctx context.Context, owner string, _ string, args map[string]any) (any, error) {
			return runAnimationCapability(ctx, svc, owner, action, args)
		})
		if err != nil {
			return names, err
		}
		names = append(names, name)
	}
	return names, nil
}

// runControlCapability 对齐 capabilities/desktop_pet.py control：先按请求模型校验再下发
func runControlCapability(ctx context.Context, svc *Service, action string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	text := ""
	active := true
	switch action {
	case "say":
		if err := rejectUnknownArgs(args, "text"); err != nil {
			return nil, err
		}
		value, err := requiredTextArg(args, "text", 2000)
		if err != nil {
			return nil, err
		}
		text = value
	case "set_thinking":
		if err := rejectUnknownArgs(args, "active"); err != nil {
			return nil, err
		}
		value, err := strictBoolArg(args, "active")
		if err != nil {
			return nil, err
		}
		active = value
	case "status", "show", "hide", "refresh_buttons":
		if err := rejectUnknownArgs(args); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("未知桌宠能力: desktop_pet.%s", action)
	}
	status, err := svc.Control(ctx, action, text, active)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "result": status}, nil
}

// runAnimationCapability 对齐 capabilities/desktop_pet.py animation：显式补默认值后下发
func runAnimationCapability(ctx context.Context, svc *Service, owner, action string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	payload := map[string]any{}
	switch action {
	case "get_skin", "list_animations":
		if err := rejectUnknownArgs(args); err != nil {
			return nil, err
		}
	case "play_animation":
		if err := rejectUnknownArgs(args, "name", "repeat"); err != nil {
			return nil, err
		}
		name, err := requiredTextArg(args, "name", 64)
		if err != nil {
			return nil, err
		}
		repeat, err := integerArg(args, "repeat", 1, 1, 30)
		if err != nil {
			return nil, err
		}
		payload["name"] = name
		payload["repeat"] = repeat
	case "play_event":
		if err := rejectUnknownArgs(args, "event"); err != nil {
			return nil, err
		}
		event, err := requiredTextArg(args, "event", 16)
		if err != nil {
			return nil, err
		}
		switch event {
		case "click", "happy", "error", "greeting":
		default:
			return nil, fmt.Errorf("event 只能是 click、happy、error 或 greeting")
		}
		payload["event"] = event
	case "stop_animation", "animation_status":
		if err := rejectUnknownArgs(args, "playback_id"); err != nil {
			return nil, err
		}
		playbackID, err := requiredTextArg(args, "playback_id", 64)
		if err != nil {
			return nil, err
		}
		payload["playback_id"] = playbackID
	default:
		return nil, fmt.Errorf("未知动画能力: desktop_pet.%s", action)
	}
	result, err := svc.Animation(ctx, owner, action, payload)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "result": result}, nil
}

// requiredTextArg 取必填文本参数，去首尾空白后按字符数校验长度
func requiredTextArg(args map[string]any, key string, maxLen int) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("缺少必要参数 %s", key)
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("%s 不能为空", key)
	}
	if utf8.RuneCountInString(text) > maxLen {
		return "", fmt.Errorf("%s 长度不能超过 %d", key, maxLen)
	}
	return text, nil
}

// strictBoolArg 取必填布尔参数，对齐 StrictBool 不接受其他类型
func strictBoolArg(args map[string]any, key string) (bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return false, fmt.Errorf("缺少必要参数 %s", key)
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s 必须是布尔值", key)
	}
	return value, nil
}

// integerArg 取整数参数，缺省用 def，越界或非整数报错
func integerArg(args map[string]any, key string, def, minValue, maxValue int) (int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return def, nil
	}
	var value int
	switch n := raw.(type) {
	case int:
		value = n
	case int64:
		value = int(n)
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("%s 必须是整数", key)
		}
		value = int(n)
	default:
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	if value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s 应在 %d 至 %d 之间", key, minValue, maxValue)
	}
	return value, nil
}

// rejectUnknownArgs 对齐 extra="forbid"，多余参数直接报错
func rejectUnknownArgs(args map[string]any, allowed ...string) error {
	if len(args) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	for key := range args {
		if _, ok := allow[key]; !ok {
			return fmt.Errorf("不支持参数 %s", key)
		}
	}
	return nil
}
