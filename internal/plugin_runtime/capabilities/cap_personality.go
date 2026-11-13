package capabilities

import (
	"context"
	"fmt"
	"strings"
)

func personalityConfigPort(source BotConfigSource) PersonalityConfigPort {
	port, _ := source.(PersonalityConfigPort)
	return port
}

func makePersonalityListHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(_ context.Context, _, capability string, args map[string]any) (any, error) {
		if len(args) > 0 {
			return nil, fmt.Errorf("%s 不接受参数", capability)
		}
		port := personalityConfigPort(deps.Instance)
		if port == nil {
			return capabilityFailure("人设配置源未接线：实例 TOML 不可用"), nil
		}
		profiles := port.PersonalityProfiles()
		if profiles == nil {
			profiles = []map[string]any{}
		}
		return map[string]any{"success": true, "result": map[string]any{"profiles": profiles}}, nil
	}
}

func makePersonalityGetCurrentHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(_ context.Context, _, capability string, args map[string]any) (any, error) {
		if len(args) > 0 {
			return nil, fmt.Errorf("%s 不接受参数", capability)
		}
		port := personalityConfigPort(deps.Instance)
		if port == nil {
			return capabilityFailure("人设配置源未接线：实例 TOML 不可用"), nil
		}
		current := port.CurrentPersonalityProfile()
		if current == nil {
			return capabilityFailure("实例配置尚未初始化"), nil
		}
		return map[string]any{"success": true, "result": current}, nil
	}
}

func makePersonalitySwitchHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(_ context.Context, _, _ string, args map[string]any) (any, error) {
		name := strings.TrimSpace(coalesceString(args, "name"))
		if name == "" {
			return nil, fmt.Errorf("%s", "personality.switch 需要非空 name")
		}
		port := personalityConfigPort(deps.Instance)
		if port == nil {
			return capabilityFailure("人设切换端口未接线：实例 TOML 配置源不可用"), nil
		}
		result, err := port.SwitchPersonalityProfile(name)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "result": result}, nil
	}
}
