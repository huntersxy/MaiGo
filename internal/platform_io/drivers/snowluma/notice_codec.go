package snowluma

// notice_codec.go：notice 事件编解码骨架（P0 stub）

import "strings"

// NoticeAllowed 按 notice 白名单判断是否注入 Host
func NoticeAllowed(settings Settings, noticeType, subType string) bool {
	if !settings.Notice.Enabled {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(noticeType))
	sub := strings.ToLower(strings.TrimSpace(subType))
	switch key {
	case "notify":
		switch sub {
		case "poke":
			return settings.Notice.EnablePoke
		default:
			return false
		}
	case "friend_recall":
		return settings.Notice.EnableFriendRecall
	case "group_recall":
		return settings.Notice.EnableGroupRecall
	case "group_ban":
		return settings.Notice.EnableGroupBan
	case "group_msg_emoji_like":
		return settings.Notice.EnableGroupMsgEmojiLike
	case "group_upload":
		return settings.Notice.EnableGroupUpload
	case "group_increase":
		return settings.Notice.EnableGroupIncrease
	case "group_decrease":
		return settings.Notice.EnableGroupDecrease
	case "group_admin":
		return settings.Notice.EnableGroupAdmin
	case "essence":
		return settings.Notice.EnableEssence
	case "group_name":
		return settings.Notice.EnableGroupName
	default:
		return false
	}
}
