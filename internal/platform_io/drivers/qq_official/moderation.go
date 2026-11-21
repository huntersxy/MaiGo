package qq_official

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	MuteToolName   = "set_mute"
	KickToolName   = "kick_member"
	RecallToolName = "recall_message"
)

type ModerationTarget struct {
	MessageID   string
	GroupID     string
	TargetID    string
	UserID      string
	UserName    string
	GroupOpenID string
	UserOpenID  string
}

func (a *Adapter) Moderate(ctx context.Context, operation string, target ModerationTarget, duration int, reason string) (map[string]any, error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	client, mapping := a.client, a.mapping
	ready := a.runtime.Ready()
	a.mu.Unlock()
	if !ready || client == nil {
		return nil, fmt.Errorf("QQ 官方网关未连接")
	}
	group := strings.TrimSpace(target.GroupID)
	groupOpenID := strings.TrimSpace(target.GroupOpenID)
	if group != "" {
		groupOpenID = firstNonEmpty(groupOpenID, mapping.reverse("group")[group], group)
		group = firstNonEmpty(mapping.Group[groupOpenID], group)
	}
	users, _ := a.groupUserMapping(mapping, groupOpenID)
	user := strings.TrimSpace(target.UserID)
	userOpenID := firstNonEmpty(strings.TrimSpace(target.UserOpenID), users.reverse("user")[user], user)
	user = firstNonEmpty(users.User[userOpenID], user)
	if isDecimal(userOpenID) {
		userOpenID = ""
	}
	groupPath := "/v2/groups/" + url.PathEscape(groupOpenID)
	display := firstNonEmpty(target.UserName, user)
	switch operation {
	case MuteToolName:
		settings := a.settings.Mute
		if !settings.Enabled {
			return nil, fmt.Errorf("禁言设置工具已停用")
		}
		if duration < 0 {
			return nil, fmt.Errorf("禁言时长必须为非负整数（秒），0 表示解除禁言")
		}
		if groupOpenID == "" || isDecimal(groupOpenID) {
			return nil, fmt.Errorf("无法解析群 %s 的 OpenID", group)
		}
		if !allowedModerationGroup(settings.AllowedGroups, group, groupOpenID) {
			return nil, fmt.Errorf("群 %s 不在禁言白名单内", group)
		}
		op, expiry := "del", ""
		content := fmt.Sprintf("已解除 %s 的禁言。", display)
		if duration > 0 {
			if moderationContains(settings.AdminUsers, user, userOpenID) {
				return nil, fmt.Errorf("用户 %s 在禁言保护名单中，无法被禁言", display)
			}
			duration = max(settings.MinDuration, min(duration, settings.MaxDuration))
			op, expiry = "add", time.Now().UTC().Add(time.Duration(duration)*time.Second).Format("2006-01-02T15:04:05.999999Z07:00")
			content = fmt.Sprintf("已禁言 %s，时长 %d 秒。", display, duration)
		}
		if userOpenID == "" {
			return nil, fmt.Errorf("无法解析 %s 的 OpenID，需其消息经本适配器接收或先 /assign_id 绑定", display)
		}
		body := map[string]any{"members": []map[string]any{{"op": op, "member_openid": userOpenID, "mute_expire_at": expiry}}}
		if _, err := client.APIPost(ctx, groupPath+"/restrict_chat_setting", body); err != nil {
			return nil, err
		}
		reason = strings.TrimSpace(reason)
		zap.L().Info("QQ 官方设置群成员禁言", zap.String("group", group), zap.String("user", user), zap.Int("duration", duration), zap.String("reason", reason))
		return map[string]any{"success": true, "content": content, "group_id": group, "user_id": user, "duration": duration, "reason": reason}, nil
	case KickToolName:
		settings := a.settings.Kick
		if !settings.Enabled {
			return nil, fmt.Errorf("踢人工具已停用")
		}
		if groupOpenID == "" || isDecimal(groupOpenID) || userOpenID == "" {
			return nil, fmt.Errorf("无法解析目标群或成员的官方 OpenID")
		}
		if !allowedModerationGroup(settings.AllowedGroups, group, groupOpenID) {
			return nil, fmt.Errorf("群 %s 不在踢人白名单内", group)
		}
		if moderationContains(settings.AdminUsers, user, userOpenID, target.UserID) {
			return nil, fmt.Errorf("用户 %s 在踢人保护名单中，无法被移除", user)
		}
		if userOpenID == a.runtime.AccountID() || user == a.runtime.RouteAccountID() {
			return nil, fmt.Errorf("不能通过踢人工具移除机器人自身")
		}
		body := map[string]any{"member_openids": []string{userOpenID}, "add_to_member_blacklist": false}
		result, err := client.APIPost(ctx, groupPath+"/batch_remove_members", body)
		if err != nil {
			return nil, err
		}
		if result["remove_members_result"] != "success" {
			return nil, fmt.Errorf("QQ 移除群成员未成功: %v", result)
		}
		zap.L().Info("QQ 官方踢人", zap.String("group", group), zap.String("user", user), zap.String("reason", reason))
		return map[string]any{"success": true, "content": fmt.Sprintf("已将 %s 移出群 %s。", user, group), "group_id": group, "user_id": user, "reason": reason}, nil
	case RecallToolName:
		if !a.settings.Recall.Enabled {
			return nil, fmt.Errorf("撤回工具已停用")
		}
		id := strings.TrimSpace(target.MessageID)
		if id == "" {
			return nil, fmt.Errorf("缺少目标消息 ID")
		}
		path, scope, openID := groupPath, "群 "+group, groupOpenID
		if group == "" {
			peer := strings.TrimSpace(target.TargetID)
			openID = firstNonEmpty(strings.TrimSpace(target.UserOpenID), mapping.reverse("user")[peer], peer)
			path, scope = "/v2/users/"+url.PathEscape(openID), "单聊"
		} else if !allowedModerationGroup(a.settings.Recall.AllowedGroups, group, groupOpenID) {
			return nil, fmt.Errorf("群 %s 不在撤回白名单内", group)
		}
		if openID == "" || isDecimal(openID) {
			return nil, fmt.Errorf("无法解析消息 %s 所在会话的 OpenID", id)
		}
		result, err := client.apiRequest(ctx, http.MethodDelete, path+"/messages/"+url.PathEscape(id), nil, 0)
		if err != nil {
			code := firstNonEmpty(asString(result["code"]), asString(result["err_code"]))
			if code == "40062003" {
				return nil, fmt.Errorf("无法撤回此消息，可能是因为没有开启权限，或用户是群主或管理员")
			}
			return nil, err
		}
		zap.L().Info("QQ 官方撤回", zap.String("scope", scope), zap.String("message_id", id))
		return map[string]any{"success": true, "content": fmt.Sprintf("已撤回消息 %s（%s）。", id, scope), "msg_id": id, "group_id": group}, nil
	default:
		return nil, fmt.Errorf("未知 QQ 群管理操作 %s", operation)
	}
}

func allowedModerationGroup(allowed []string, group, openID string) bool {
	return !slices.ContainsFunc(allowed, func(value string) bool { return strings.TrimSpace(value) != "" }) || moderationContains(allowed, group, openID)
}

func moderationContains(values []string, targets ...string) bool {
	return slices.ContainsFunc(values, func(value string) bool {
		trimmed := strings.TrimSpace(value)
		return trimmed != "" && slices.Contains(targets, trimmed)
	})
}
