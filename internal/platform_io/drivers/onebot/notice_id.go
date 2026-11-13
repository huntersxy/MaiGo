package onebot

import "strings"

// NoticeIDFromMessage 读取 OneBot 通知事件的稳定去重键，普通消息返回空
func NoticeIDFromMessage(msg *ConversationMessage) string {
	if msg == nil {
		return ""
	}
	if msg.AdditionalConfig != nil {
		if value, _ := msg.AdditionalConfig["napcat_notice_id"].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if msg.IsNotify && strings.HasPrefix(msg.ExternalMessageID, "napcat-notice-") {
		return strings.TrimSpace(msg.ExternalMessageID)
	}
	return ""
}
