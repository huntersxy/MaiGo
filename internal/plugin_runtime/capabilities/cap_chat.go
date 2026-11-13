package capabilities

import (
	"context"
	"strings"
)

func pluginChatPlatform(args map[string]any) string {
	platform := strings.TrimSpace(coalesceString(args, "platform"))
	if platform == "" {
		return "qq"
	}
	return platform
}

func makeChatListStreamsHandler(port PluginSessionPort, capability string) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return capabilityFailure("%s", pluginPortErrorText(capability)), nil
		}
		filter := PluginSessionFilter{Platform: pluginChatPlatform(args), AllPlatforms: pluginChatPlatform(args) == "all_platforms"}
		switch capability {
		case "chat.get_group_streams":
			value := true
			filter.IsGroup = &value
		case "chat.get_private_streams":
			value := false
			filter.IsGroup = &value
		}
		streams, err := port.ListStreams(ctx, filter)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if streams == nil {
			streams = []map[string]any{}
		}
		return map[string]any{"success": true, "streams": streams}, nil
	}
}

func makeChatOpenSessionHandler(port PluginSessionPort) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return capabilityFailure("%s", pluginPortErrorText("chat.open_session")), nil
		}
		platform := strings.TrimSpace(coalesceString(args, "platform"))
		if platform == "" {
			platform = "qq"
		}
		chatType := strings.ToLower(strings.TrimSpace(coalesceString(args, "chat_type", "type")))
		if chatType != "group" && chatType != "private" {
			if strings.TrimSpace(coalesceString(args, "group_id")) != "" {
				chatType = "group"
			} else {
				chatType = "private"
			}
		}
		req := PluginOpenSession{
			ChatType:  chatType,
			Platform:  platform,
			UserID:    strings.TrimSpace(coalesceString(args, "user_id")),
			GroupID:   strings.TrimSpace(coalesceString(args, "group_id")),
			AccountID: strings.TrimSpace(coalesceString(args, "account_id")),
			Scope:     strings.TrimSpace(coalesceString(args, "scope")),
		}
		if chatType == "group" && req.GroupID == "" {
			return capabilityFailure("群聊会话缺少必要参数 group_id"), nil
		}
		if chatType == "private" && req.UserID == "" {
			return capabilityFailure("私聊会话缺少必要参数 user_id"), nil
		}
		result, err := port.OpenSession(ctx, req)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return result, nil
	}
}

func makeChatStreamLookupHandler(port PluginSessionPort, capability string) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return capabilityFailure("%s", pluginPortErrorText(capability)), nil
		}
		platform := pluginChatPlatform(args)
		accountID := strings.TrimSpace(coalesceString(args, "account_id"))
		scope := strings.TrimSpace(coalesceString(args, "scope"))
		var (
			stream map[string]any
			err    error
		)
		if capability == "chat.get_stream_by_group_id" {
			groupID := strings.TrimSpace(coalesceString(args, "group_id"))
			if groupID == "" {
				return capabilityFailure("缺少必要参数 group_id"), nil
			}
			stream, err = port.StreamByGroupID(ctx, platform, groupID, accountID, scope)
		} else {
			userID := strings.TrimSpace(coalesceString(args, "user_id"))
			if userID == "" {
				return capabilityFailure("缺少必要参数 user_id"), nil
			}
			stream, err = port.StreamByUserID(ctx, platform, userID, accountID, scope)
		}
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "stream": stream}, nil
	}
}

func makeChatAvatarHandler(port PluginSessionPort) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		if port == nil {
			return capabilityFailure("%s", pluginPortErrorText("chat.get_avatar")), nil
		}
		platform := strings.TrimSpace(coalesceString(args, "platform"))
		targetID := strings.TrimSpace(coalesceString(args, "target_id"))
		if platform == "" || targetID == "" {
			return capabilityFailure("缺少必要参数 platform 或 target_id"), nil
		}
		result, err := port.Avatar(ctx, PluginAvatarRequest{
			Platform:     platform,
			TargetID:     targetID,
			TargetType:   firstNonEmptyText(coalesceString(args, "target_type"), "user"),
			AccountID:    coalesceString(args, "account_id"),
			Scope:        coalesceString(args, "scope"),
			ForceRefresh: truthyPayloadValue(args["force_refresh"]),
		})
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		return map[string]any{"success": true, "avatar": result}, nil
	}
}
