package capabilities

import (
	"context"
	"strings"
)

func makePersonGetIDHandler(port PluginPersonPort) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired("person.get_id"), nil
		}
		platform := strings.TrimSpace(coalesceString(args, "platform"))
		userID := strings.TrimSpace(coalesceString(args, "user_id"))
		if platform == "" || userID == "" {
			return capabilityFailure("缺少必要参数 platform 或 user_id"), nil
		}
		personID, err := port.GetID(ctx, platform, userID)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "person_id": personID}, nil
	}
}

func makePersonGetValueHandler(port PluginPersonPort) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired("person.get_value"), nil
		}
		personID := strings.TrimSpace(coalesceString(args, "person_id"))
		fieldName := strings.TrimSpace(coalesceString(args, "field_name"))
		if personID == "" || fieldName == "" {
			return capabilityFailure("缺少必要参数 person_id 或 field_name"), nil
		}
		value, ok, err := port.GetValue(ctx, personID, fieldName)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if !ok {
			value = args["default"]
		}
		return map[string]any{"success": true, "value": value}, nil
	}
}

func makePersonGetIDByNameHandler(port PluginPersonPort) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired("person.get_id_by_name"), nil
		}
		name := strings.TrimSpace(coalesceString(args, "person_name"))
		if name == "" {
			return capabilityFailure("缺少必要参数 person_name"), nil
		}
		personID, err := port.GetIDByName(ctx, name)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "person_id": personID}, nil
	}
}
