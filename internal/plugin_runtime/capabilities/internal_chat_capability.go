package capabilities

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"maibot-go/internal/core/internal_chat"
)

// PluginChatRelay 插件界面代主人投递内部聊天消息
type PluginChatRelay interface {
	RelayPluginChatMessage(ctx context.Context, pluginID, content string, images []map[string]any) (string, error)
}

// makeInternalChatIdentityHandler 返回内部聊天平台与主人用户 ID
func makeInternalChatIdentityHandler() CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		return map[string]any{
			"success": true,
			"result": map[string]any{
				"platform": internalchat.Platform,
				"user_id":  internalchat.OwnerUserID,
			},
		}, nil
	}
}

// makeInternalChatSendHandler 以主人身份投递一句话，对齐 internal_chat.send
func makeInternalChatSendHandler(relay PluginChatRelay) CapabilityHandler {
	return func(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
		if args == nil {
			args = map[string]any{}
		}
		if err := rejectInternalChatArgs(args); err != nil {
			return nil, err
		}
		raw, ok := args["text"]
		if !ok || raw == nil {
			return nil, fmt.Errorf("缺少必要参数 text")
		}
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("text 必须是字符串")
		}
		text = strings.TrimSpace(text)
		length := utf8.RuneCountInString(text)
		if length < 1 || length > 2000 {
			return nil, fmt.Errorf("%s 的 text 长度必须为 1～2000 个字符", capability)
		}
		if relay == nil {
			return nil, fmt.Errorf("内部聊天服务未就绪")
		}
		messageID, err := relay.RelayPluginChatMessage(ctx, pluginID, text, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"success": true,
			"result":  map[string]any{"message_id": messageID},
		}, nil
	}
}

func rejectInternalChatArgs(args map[string]any) error {
	for key := range args {
		if key != "text" {
			return fmt.Errorf("不支持参数 %s", key)
		}
	}
	return nil
}
