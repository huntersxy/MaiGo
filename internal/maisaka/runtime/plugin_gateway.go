package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"
)

// fallbackGatewayArrivalSeq 为插件网关入站提供单调递增的运行时序号
var fallbackGatewayArrivalSeq atomic.Uint64

// ReceiveExternalPluginMessage 接收消息网关入站的外部平台消息并进入 Agent 会话调度
// 对齐 src/plugin_runtime/host/message_gateway.py receive_external_message
func (a *Agent) ReceiveExternalPluginMessage(ctx context.Context, message map[string]any) error {
	if a == nil {
		return fmt.Errorf("Agent 未初始化")
	}
	if len(message) == 0 {
		return fmt.Errorf("外部消息为空")
	}
	platform := strings.TrimSpace(stringValueOf(message["platform"]))
	if platform == "" {
		return fmt.Errorf("外部消息缺少 platform")
	}
	accountID := strings.TrimSpace(firstNonEmptyMapString(message, "account_id", "self_id"))
	scope := strings.TrimSpace(stringValueOf(message["scope"]))
	groupID := strings.TrimSpace(firstNonEmptyMapString(message, "group_id"))
	externalTarget := strings.TrimSpace(firstNonEmptyMapString(message, "target_id"))
	if externalTarget == "" {
		externalTarget = groupID
	}
	externalUser := strings.TrimSpace(firstNonEmptyMapString(message, "user_id"))
	if externalUser == "" {
		if info, ok := message["user_info"].(map[string]any); ok {
			externalUser = strings.TrimSpace(firstNonEmptyMapString(info, "user_id"))
		}
	}
	if externalTarget == "" {
		return fmt.Errorf("外部消息缺少 target_id 或 group_id")
	}
	kind := memory.ConversationKindPrivate
	chatType := strings.ToLower(strings.TrimSpace(firstNonEmptyMapString(message, "chat_type", "message_type")))
	if groupID != "" || chatType == "group" {
		kind = memory.ConversationKindGroup
	}
	targetID, err := strconv.ParseInt(externalTarget, 10, 64)
	if err != nil || targetID <= 0 {
		targetID = onebot.SyntheticIdentityID("target", platform, accountID, scope, externalTarget)
	}
	userID, err := strconv.ParseInt(externalUser, 10, 64)
	if err != nil || userID <= 0 {
		if externalUser == "" {
			externalUser = externalTarget
		}
		userID = onebot.SyntheticIdentityID("user", platform, accountID, scope, externalUser)
	}
	messageIDText := strings.TrimSpace(firstNonEmptyMapString(message, "message_id", "msg_id"))
	messageID, err := strconv.ParseInt(messageIDText, 10, 64)
	if err != nil || messageID <= 0 {
		messageID = onebot.SyntheticIdentityID("message", platform, accountID, scope, externalTarget, messageIDText)
	}
	content := pluginGatewayContent(message)
	if content == "" {
		content = "[外部消息]"
	}
	nickname := strings.TrimSpace(firstNonEmptyMapString(message, "nickname", "user_nickname"))
	if nickname == "" {
		if info, ok := message["user_info"].(map[string]any); ok {
			nickname = strings.TrimSpace(firstNonEmptyMapString(info, "user_nickname", "nickname"))
		}
	}
	now := time.Now()
	msg := &onebot.ConversationMessage{
		Platform:          platform,
		AccountID:         accountID,
		Scope:             scope,
		MessageID:         messageID,
		ConversationKind:  kind,
		TargetID:          targetID,
		UserID:            userID,
		ExternalMessageID: messageIDText,
		ExternalTargetID:  externalTarget,
		ExternalUserID:    externalUser,
		Nickname:          nickname,
		Content:           content,
		FinalContent:      content,
		Time:              now,
		ReceivedAt:        now,
	}
	msg.ArrivalSeq = fallbackGatewayArrivalSeq.Add(1)
	a.onMessage(msg)
	_ = ctx
	return nil
}

func pluginGatewayContent(message map[string]any) string {
	raw, ok := message["message"]
	if !ok || raw == nil {
		raw = message["content"]
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	if text := strings.TrimSpace(stringValueOf(message["processed_plain_text"])); text != "" {
		return text
	}
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringValueOf(node["type"]))) {
		case "text":
			if text := strings.TrimSpace(firstNonEmptyMapString(node, "data", "content")); text != "" {
				parts = append(parts, text)
			}
		case "image", "emoji":
			parts = append(parts, "[图片]")
		case "at":
			parts = append(parts, "@"+strings.TrimSpace(firstNonEmptyMapString(node, "data", "user_id")))
		case "face":
			parts = append(parts, "[表情]")
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
