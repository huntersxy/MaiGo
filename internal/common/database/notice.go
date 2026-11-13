package memory

import "strings"

// NoticeExternalMessageIDPredicate 返回排除平台通知行的 SQL 条件
//
// 对齐 Python src/common/message_repository.py:99 的 message_id != "notice"；
// Go 的 NapCat 通知 id 由 internal/onebot/client_notice.go 生成，形如 napcat-notice-<hash>，
// 因此历史/记忆读取路径需要同时排除遗留值 notice 与该前缀
func NoticeExternalMessageIDPredicate(column string) string {
	column = strings.TrimSpace(column)
	if column == "" {
		column = "external_message_id"
	}
	return "COALESCE(" + column + ", '') <> 'notice' AND COALESCE(" + column + ", '') NOT LIKE 'napcat-notice-%'"
}

// IsNoticeExternalMessageID 判断消息 id 是否为平台通知（notice 或 napcat-notice-*）
func IsNoticeExternalMessageID(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "notice" || strings.HasPrefix(trimmed, "napcat-notice-")
}
