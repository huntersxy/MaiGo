package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	"maibot-go/internal/core/internal_chat"
	"maibot-go/internal/files"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
	"maibot-go/internal/platform_io"
)

func (a *App) fileChatHooks() filesvc.ChatHooks {
	return filesvc.ChatHooks{
		List:                 a.listFileChats,
		ResolveUploadSession: a.resolveFileUploadSession,
		Send:                 a.sendFileToChat,
	}
}

func (a *App) listFileChats(ctx context.Context) ([]filesvc.FileChat, error) {
	if a.memMgr == nil {
		return nil, fmt.Errorf("聊天存储未就绪")
	}
	var rows []chatsession.Record
	if err := a.memMgr.GetDB().WithContext(ctx).
		Order("last_active_timestamp DESC NULLS LAST, session_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]filesvc.FileChat, 0, len(rows))
	for i := range rows {
		name := chatsession.DisplayName(&rows[i])
		if name == "" {
			if rows[i].Platform == internalchat.Platform {
				name = "本地聊天室"
			} else {
				name = "未命名聊天"
			}
		}
		items = append(items, filesvc.FileChat{SessionID: rows[i].SessionID, Name: name})
	}
	return items, nil
}

func (a *App) resolveFileUploadSession(ctx context.Context, chatSessionID string) (string, error) {
	chatSessionID = strings.TrimSpace(chatSessionID)
	if chatSessionID == "" || a.chatHub == nil || !a.chatHub.HasClient(chatSessionID) {
		return "", fmt.Errorf("聊天连接已失效，请重新连接")
	}
	if a.memMgr == nil {
		return "", fmt.Errorf("文件服务不可用")
	}
	manager := chatsession.NewManager(a.memMgr.GetDB())
	platform := internalchat.Platform
	userID := strconv.FormatInt(internalchat.OwnerNumericID, 10)
	groupID := ""
	if virtual := a.chatHub.VirtualIdentityForSession(chatSessionID); virtual != nil {
		platform = virtual.PlatformName()
		userID = virtual.UserID
		groupID = virtual.GroupID
	}
	session, err := manager.GetOrCreateSession(ctx, platform, userID, groupID, a.fileAccountID(), "")
	if err != nil {
		return "", fmt.Errorf("文件会话不可用")
	}
	if session == nil {
		return "", fmt.Errorf("文件会话不可用")
	}
	return session.SessionID, nil
}

// SendFileToChat 供插件 files.send 与 WebUI 文件页复用同一发送链路
func (a *App) SendFileToChat(ctx context.Context, fileRef, sessionID string) (map[string]any, error) {
	return a.sendFileToChat(ctx, fileRef, sessionID)
}

func (a *App) sendFileToChat(ctx context.Context, fileRef, sessionID string) (map[string]any, error) {
	if a.files == nil {
		return nil, fmt.Errorf("文件服务不可用")
	}
	asset, err := a.files.Store().Get(fileRef, "", true)
	if err != nil {
		return nil, err
	}
	if asset.State != "ready" {
		return nil, filesvc.ErrNotReady
	}
	if a.memMgr == nil {
		return nil, fmt.Errorf("文件服务不可用")
	}
	sessionManager := chatsession.NewManager(a.memMgr.GetDB())
	target, err := sessionManager.GetExistingBySessionID(ctx, sessionID)
	if err != nil || target == nil {
		return nil, fmt.Errorf("聊天会话不存在")
	}
	platform := strings.TrimSpace(target.Platform)
	kind := memory.ConversationKindPrivate
	targetID := int64(0)
	groupKey := ""
	groupName := ""
	channelKey := ""
	internal := platform == internalchat.Platform
	if target.IsGroup() {
		groupKey = strings.TrimSpace(*target.GroupID)
		kind = memory.ConversationKindGroup
		if internalchat.IsVirtualGroupID(groupKey) {
			internal = true
			targetID = internalchat.VirtualTargetID(groupKey)
			if target.GroupName != nil {
				groupName = strings.TrimSpace(*target.GroupName)
			}
			channelKey = internalchat.GroupChannel(groupKey)
		} else {
			if !strings.EqualFold(platform, platformio.PlatformQQ) {
				return nil, fmt.Errorf("文件发送尚未支持该平台")
			}
			targetID, err = strconv.ParseInt(groupKey, 10, 64)
			if err != nil || targetID <= 0 {
				return nil, fmt.Errorf("群聊目标无效")
			}
		}
	} else if internal {
		targetID = internalchat.OwnerNumericID
		channelKey = internalchat.OwnerChannelKey()
	} else {
		if !strings.EqualFold(platform, platformio.PlatformQQ) {
			return nil, fmt.Errorf("文件发送尚未支持该平台")
		}
		if target.UserID == nil {
			return nil, fmt.Errorf("私聊目标无效")
		}
		targetID, err = strconv.ParseInt(strings.TrimSpace(*target.UserID), 10, 64)
		if err != nil || targetID <= 0 {
			return nil, fmt.Errorf("私聊目标无效")
		}
	}
	if err := a.files.Store().Grant(fileRef, sessionID); err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(asset.Name)
	if displayName == "" {
		displayName = "文件"
	}
	if internal {
		messageID, err := a.chatHub.SendBotFileToChannel(channelKey, filesvc.PublicView(asset))
		if err != nil {
			return nil, err
		}
		if a.maiAgent != nil {
			file := onebot.FileInfo{Name: displayName, MimeType: asset.MimeType, Size: asset.Size, FileRef: asset.FileRef}
			if err := a.maiAgent.PersistFileMessage(messageID, file, platform, kind, targetID, groupKey, groupName); err != nil {
				zap.L().Warn("内部聊天文件消息落库失败", zap.String("file_ref", file.FileRef), zap.Error(err))
			}
		}
		return sentFileResult(fileRef, messageID), nil
	}
	if a.maiAgent == nil {
		return nil, fmt.Errorf("OneBot 客户端未就绪")
	}
	fileURL, err := a.files.DownloadURL(fileRef, sessionID, false, "")
	if err != nil {
		return nil, err
	}
	messageID, _, err := a.maiAgent.SendFileToConversation(ctx, kind, targetID, fileURL, displayName)
	if err != nil {
		return nil, fmt.Errorf("文件发送失败: %w", err)
	}
	file := onebot.FileInfo{Name: displayName, MimeType: asset.MimeType, Size: asset.Size, FileRef: asset.FileRef}
	if err := a.maiAgent.PersistFileMessage(messageID, file, platform, kind, targetID, groupKey, groupName); err != nil {
		zap.L().Warn("QQ 文件消息落库失败", zap.String("file_ref", file.FileRef), zap.Error(err))
	}
	return sentFileResult(fileRef, messageID), nil
}

func sentFileResult(fileRef string, messageID int64) map[string]any {
	return map[string]any{
		"file_ref":            fileRef,
		"status":              "sent",
		"message_id":          strconv.FormatInt(messageID, 10),
		"retry_automatically": false,
	}
}

func (a *App) fileAccountID() string {
	if a.maiAgent == nil {
		return ""
	}
	if selfID := a.maiAgent.SelfID(); selfID > 0 {
		return strconv.FormatInt(selfID, 10)
	}
	return ""
}
