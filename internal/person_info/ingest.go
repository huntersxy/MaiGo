package personinfo

import (
	"context"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"
)

// RegisterFromMessage 在消息持久化后调用，对齐 heartflow Person.register_person
func (r *Registry) RegisterFromMessage(ctx context.Context, msg *onebot.ConversationMessage) error {
	if msg == nil {
		return nil
	}
	platform := strings.TrimSpace(msg.Platform)
	if platform == "" {
		platform = PlatformQQ
	}
	// 非 QQ 平台保留原始字符串身份；QQ 仍使用数值账号
	userID := strings.TrimSpace(msg.ExternalUserID)
	if userID == "" && msg.UserID > 0 {
		userID = strconv.FormatInt(msg.UserID, 10)
	}
	if userID == "" {
		return nil
	}
	nickname := strings.TrimSpace(msg.Nickname)
	groupKey := strings.TrimSpace(msg.ExternalTargetID)
	if groupKey == "" && msg.GroupKey != "" {
		groupKey = strings.TrimSpace(msg.GroupKey)
	}
	in := RegisterInput{
		Platform: platform,
		UserID:   userID,
		Nickname: nickname,
		SeenAt:   msg.Time,
	}
	if msg.ConversationKind == memory.ConversationKindGroup {
		in.GroupNickName = strings.TrimSpace(msg.GroupCard)
		if platform == PlatformQQ && msg.TargetID > 0 {
			in.GroupID = msg.TargetID
		}
		in.GroupKey = groupKey
	}
	profile, err := r.RegisterPerson(ctx, in)
	if err != nil {
		return err
	}
	groupID := in.GroupKey
	if groupID == "" && in.GroupID > 0 {
		groupID = strconv.FormatInt(in.GroupID, 10)
	}
	r.ObserveIdentityAsync(ctx, IdentityObservation{
		Platform: in.Platform,
		UserID:   in.UserID,
		Aliases:  []string{in.Nickname, in.GroupNickName},
		GroupID:  groupID,
	})
	_ = profile
	return nil
}

func stringsTrim(s string) string {
	return strings.TrimSpace(s)
}

func timeNow() time.Time {
	return time.Now()
}
