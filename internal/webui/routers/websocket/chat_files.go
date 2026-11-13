package websocket

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/core/internal_chat"
	"maibot-go/internal/files"
	"maibot-go/internal/platform_io/drivers/onebot"
)

// storeChatFiles 把 WebUI 上传的文件写入 filesvc 资产库并返回公开字段
func (s *unifiedWSSession) storeChatFiles(files []any, virtual *internalchat.VirtualIdentity) ([]any, error) {
	if len(files) == 0 {
		return []any{}, nil
	}
	service := s.deps.Files
	if service == nil || !service.Enabled() {
		return nil, fmt.Errorf("文件功能已关闭")
	}
	if s.deps.Memory == nil {
		return nil, fmt.Errorf("文件服务不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	accountID := ""
	if s.deps.Agent != nil {
		if selfID := s.deps.Agent.SelfID(); selfID > 0 {
			accountID = strconv.FormatInt(selfID, 10)
		}
	}
	platform := internalchat.Platform
	userID := strconv.FormatInt(internalchat.OwnerNumericID, 10)
	groupID := ""
	if virtual != nil && virtual.Enabled {
		platform = virtual.PlatformName()
		userID = virtual.UserID
		groupID = virtual.GroupID
	}
	sessionMgr := chatsession.NewManager(s.deps.Memory.GetDB())
	session, err := sessionMgr.GetOrCreateSession(ctx, platform, userID, groupID, accountID, "")
	if err != nil || session == nil {
		return nil, fmt.Errorf("文件会话不可用")
	}
	stored := make([]any, 0, len(files))
	for _, item := range files {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(wsAnyString(raw["name"]))
		fileRef := strings.TrimSpace(wsAnyString(raw["file_ref"]))
		var asset filesvc.Asset
		if fileRef != "" {
			asset, err = service.Store().Get(fileRef, session.SessionID, false)
		} else {
			encoded := strings.TrimSpace(wsAnyString(raw["base64"]))
			content, decodeErr := base64.StdEncoding.Strict().DecodeString(encoded)
			if decodeErr != nil {
				return nil, fmt.Errorf("附件 Base64 内容无效")
			}
			mimeType := strings.TrimSpace(wsAnyString(raw["mime_type"]))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			asset, err = service.SaveContent(session.SessionID, name, mimeType, content)
		}
		if err != nil {
			return nil, fmt.Errorf("保存附件失败: %w", err)
		}
		stored = append(stored, filesvc.PublicView(asset))
	}
	return stored, nil
}

// appendInternalChatFiles 把文件资产转成 Agent 文件段，保留原始顺序
func appendInternalChatFiles(msg *onebot.ConversationMessage, files []any) {
	for _, item := range files {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(wsAnyString(raw["name"]))
		fileRef := strings.TrimSpace(wsAnyString(raw["file_ref"]))
		if name == "" && fileRef == "" {
			continue
		}
		var size int64
		switch value := raw["size"].(type) {
		case float64:
			size = int64(value)
		case int64:
			size = value
		case int:
			size = int64(value)
		}
		msg.Files = append(msg.Files, onebot.FileInfo{
			Name:     name,
			MimeType: strings.TrimSpace(wsAnyString(raw["mime_type"])),
			Size:     size,
			FileRef:  fileRef,
		})
		msg.MessageParts = append(msg.MessageParts, onebot.MessagePart{Kind: "file", Index: len(msg.Files) - 1})
	}
}
