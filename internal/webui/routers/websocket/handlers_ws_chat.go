package websocket

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/core/internal_chat"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
)

// chatSessionState 单个前端聊天会话
type chatSessionState struct {
	clientSessionID string
	userName        string
	virtual         *internalchat.VirtualIdentity
	client          *internalchat.Client
}

// handleChatCall 处理 domain=chat 的统一 WebSocket 调用
func (s *unifiedWSSession) handleChatCall(requestID any, message map[string]any) error {
	method := strings.TrimSpace(wsAnyString(message["method"]))
	switch method {
	case "session.open":
		return s.openChatSession(requestID, message)
	case "session.close":
		return s.closeChatSession(requestID, message)
	case "message.send":
		return s.sendChatMessage(requestID, message)
	case "session.update_nickname":
		return s.updateChatNickname(requestID, message)
	default:
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "unsupported_method",
			"message": "不支持的聊天方法: " + method,
		})
	}
}

// openChatSession 打开逻辑聊天会话并下发会话信息、历史与欢迎语
func (s *unifiedWSSession) openChatSession(requestID any, message map[string]any) error {
	clientSessionID := strings.TrimSpace(wsAnyString(message["session"]))
	if clientSessionID == "" {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "missing_session",
			"message": "聊天会话打开请求缺少 session",
		})
	}
	if s.deps.ChatHub == nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "chat_unavailable",
			"message": "内部聊天未初始化",
		})
	}
	data, _ := message["data"].(map[string]any)
	initialName := internalchat.DefaultUserName
	if data != nil {
		if value := strings.TrimSpace(wsAnyString(data["user_name"])); value != "" {
			initialName = value
		}
	}
	virtual := s.resolveVirtualIdentity(data)
	userName := initialName
	if virtual == nil || !virtual.Enabled {
		loaded, err := internalchat.LoadUserName(s.deps.InstanceDir, initialName)
		if err != nil {
			return s.sendResponse(requestID, false, nil, map[string]any{
				"code":    "profile_unavailable",
				"message": err.Error(),
			})
		}
		userName = loaded
	}
	clientInfo := chatClientInfo(data)
	channelKey := chatChannelKey(virtual)
	if s.chatSessions != nil {
		if existing := s.chatSessions[clientSessionID]; existing != nil && existing.client != nil {
			s.deps.ChatHub.Close(existing.client.SessionID)
		}
	}
	sender := func(event string, payload map[string]any) error {
		return s.sendChatEvent(event, clientSessionID, payload)
	}
	client, err := s.deps.ChatHub.Open(clientSessionID, userName, channelKey, sender)
	if err != nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "chat_unavailable",
			"message": err.Error(),
		})
	}
	if virtual != nil && virtual.Enabled {
		s.deps.ChatHub.SetClientVirtualIdentity(client.SessionID, virtual)
	}
	if s.chatSessions == nil {
		s.chatSessions = make(map[string]*chatSessionState)
	}
	s.chatSessions[clientSessionID] = &chatSessionState{
		clientSessionID: clientSessionID,
		userName:        userName,
		virtual:         virtual,
		client:          client,
	}
	if virtual != nil && virtual.Enabled {
		s.deps.ChatHub.RegisterConversationChannel(virtual.ConversationKind(), virtual.TargetID(), channelKey)
	}
	if err := s.sendResponse(requestID, true, map[string]any{
		"session":     clientSessionID,
		"session_id":  client.SessionID,
		"client_mode": chatClientMode(clientInfo),
		"client":      clientInfo,
	}, nil); err != nil {
		return err
	}
	if err := s.sendChatEvent("session_info", clientSessionID, s.chatSessionInfo(client.SessionID, userName, clientInfo, virtual)); err != nil {
		return err
	}
	var historyGroup any
	if virtual != nil && virtual.Enabled {
		historyGroup = virtual.GroupID
	}
	if err := s.sendChatEvent("history", clientSessionID, map[string]any{
		"type":     "history",
		"messages": s.internalChatHistory(s.chatSessions[clientSessionID]),
		"group_id": historyGroup,
	}); err != nil {
		return err
	}
	restore := data != nil && truthyChatValue(data["restore"])
	if !restore {
		return s.sendChatEvent("system", clientSessionID, map[string]any{
			"type":      "system",
			"content":   s.chatWelcomeMessage(virtual),
			"timestamp": chatTimestamp(),
		})
	}
	return nil
}

// closeChatSession 关闭逻辑聊天会话
func (s *unifiedWSSession) closeChatSession(requestID any, message map[string]any) error {
	clientSessionID := strings.TrimSpace(wsAnyString(message["session"]))
	state := s.chatSessions[clientSessionID]
	if state == nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "session_not_found",
			"message": "找不到聊天会话: " + clientSessionID,
		})
	}
	if s.deps.ChatHub != nil {
		s.deps.ChatHub.Close(state.client.SessionID)
	}
	delete(s.chatSessions, clientSessionID)
	return s.sendResponse(requestID, true, map[string]any{"session": clientSessionID}, nil)
}

// sendChatMessage 接收前端消息：先应答再异步投递 Agent
func (s *unifiedWSSession) sendChatMessage(requestID any, message map[string]any) error {
	clientSessionID := strings.TrimSpace(wsAnyString(message["session"]))
	state := s.chatSessions[clientSessionID]
	if state == nil {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "session_not_found",
			"message": "找不到聊天会话: " + clientSessionID,
		})
	}
	data, _ := message["data"].(map[string]any)
	content := strings.TrimSpace(wsAnyString(data["content"]))
	attachments := chatAttachments{
		images: normalizeChatImages(data["images"]),
		emojis: normalizeChatImages(data["emojis"]),
		voices: normalizeChatVoices(data["voices"]),
	}
	files, err := normalizeChatFiles(data["files"])
	if err != nil {
		return s.sendResponse(requestID, true, map[string]any{
			"accepted": false,
			"session":  clientSessionID,
			"error":    err.Error(),
		}, nil)
	}
	attachments.files = files
	if len(attachments.files) > 0 {
		stored, storeErr := s.storeChatFiles(attachments.files, state.virtual)
		if storeErr != nil {
			return s.sendResponse(requestID, true, map[string]any{
				"accepted": false,
				"session":  clientSessionID,
				"error":    storeErr.Error(),
			}, nil)
		}
		attachments.files = stored
	}
	if content == "" && attachments.empty() {
		return s.sendResponse(requestID, true, map[string]any{
			"accepted": false,
			"session":  clientSessionID,
		}, nil)
	}
	if err := s.sendResponse(requestID, true, map[string]any{
		"accepted": true,
		"session":  clientSessionID,
	}, nil); err != nil {
		return err
	}
	display := buildChatProcessedPlainText(content, attachments)
	go s.processChatMessage(state, content, display, attachments)
	return nil
}

// chatAttachments 一次聊天消息携带的附件，字段对齐 WebUI handle_chat_message
type chatAttachments struct {
	images []any
	emojis []any
	files  []any
	voices []any
}

func (a chatAttachments) empty() bool {
	return len(a.images) == 0 && len(a.emojis) == 0 && len(a.files) == 0 && len(a.voices) == 0
}

// processChatMessage 广播用户消息并交给 Agent 处理
func (s *unifiedWSSession) processChatMessage(state *chatSessionState, content, display string, attachments chatAttachments) {
	hub := s.deps.ChatHub
	if hub == nil || state == nil {
		return
	}
	messageID := internalchat.NextNumericMessageID()
	senderName := hub.UserName(state.client.SessionID)
	if senderName == "" {
		senderName = state.userName
	}
	senderUserID := internalchat.OwnerUserID
	channelKey := internalchat.OwnerChannelKey()
	var groupID any
	virtual := state.virtual
	if virtual != nil && virtual.Enabled {
		senderName = virtual.SenderName(senderName)
		senderUserID = virtual.SenderUserID(internalchat.OwnerUserID)
		channelKey = virtual.ChannelKey()
		groupID = virtual.GroupID
	}
	hub.BroadcastUserMessage(internalchat.UserMessage{
		Content:      display,
		RawContent:   content,
		MessageID:    strconv.FormatInt(messageID, 10),
		Images:       attachments.images,
		Emojis:       attachments.emojis,
		Files:        attachments.files,
		Voices:       attachments.voices,
		GroupID:      groupID,
		SenderName:   senderName,
		SenderUserID: senderUserID,
		Origin:       state.client.SessionID,
		ChannelKey:   channelKey,
		VirtualMode:  virtual != nil && virtual.Enabled,
	})
	hub.BroadcastTypingTo(channelKey, true)
	defer hub.BroadcastTypingTo(channelKey, false)
	if s.deps.Agent == nil {
		return
	}
	msg := &onebot.ConversationMessage{
		Platform:         internalchat.Platform,
		AccountID:        internalchat.SelfUserID,
		ConversationKind: memory.ConversationKindPrivate,
		MessageID:        messageID,
		TargetID:         internalchat.OwnerNumericID,
		UserID:           internalchat.OwnerNumericID,
		// chat_sessions.user_id 对齐 Python INTERNAL_CHAT_USER_ID="owner"；
		// 数值身份只用于 mai_messages 的 BIGINT 列
		ExternalUserID: internalchat.OwnerUserID,
		ExternalTargetID: internalchat.OwnerUserID,
		Nickname:       senderName,
		Content:        content,
		Time:           time.Now(),
		ReceivedAt:     time.Now(),
	}
	if virtual != nil && virtual.Enabled {
		msg.Platform = virtual.PlatformName()
		msg.ConversationKind = virtual.ConversationKind()
		msg.TargetID = virtual.TargetID()
		msg.UserID = virtual.NumericUserID()
		msg.Nickname = virtual.UserNickname
		msg.GroupCard = virtual.UserNickname
		msg.GroupKey = virtual.GroupID
		msg.GroupName = virtual.GroupName
	}
	if content != "" {
		msg.MessageParts = append(msg.MessageParts, onebot.MessagePart{Kind: "text", Text: content})
	}
	appendInternalChatImages(msg, attachments.images, 0)
	appendInternalChatImages(msg, attachments.emojis, 1)
	appendInternalChatFiles(msg, attachments.files)
	s.deps.Agent.HandleInternalChatMessage(msg)
	zap.L().Debug("内部聊天消息已投递", zap.String("session", state.clientSessionID))
}

// appendInternalChatImages 把 WebUI 内联 base64 转成 Agent 可缓存的 data URL 图片段
func appendInternalChatImages(msg *onebot.ConversationMessage, images []any, subType int) {
	for _, item := range images {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mimeType := strings.TrimSpace(wsAnyString(raw["mime_type"]))
		encoded := strings.TrimSpace(wsAnyString(raw["base64"]))
		if mimeType == "" || encoded == "" {
			continue
		}
		msg.Images = append(msg.Images, onebot.ImageInfo{
			URL:     "data:" + mimeType + ";base64," + encoded,
			SubType: subType,
		})
		msg.MessageParts = append(msg.MessageParts, onebot.MessagePart{Kind: "image", Index: len(msg.Images) - 1})
	}
}

// closeAllChatSessions 断开连接时清理会话
func (s *unifiedWSSession) closeAllChatSessions() {
	for id, state := range s.chatSessions {
		if s.deps.ChatHub != nil {
			s.deps.ChatHub.Close(state.client.SessionID)
		}
		delete(s.chatSessions, id)
	}
}

// sendChatEvent 发送 domain=chat 事件并带上逻辑会话 ID
func (s *unifiedWSSession) sendChatEvent(event, session string, data map[string]any) error {
	payload := map[string]any{
		"op":     "event",
		"domain": "chat",
		"event":  event,
		"data":   data,
	}
	if session != "" {
		payload["session"] = session
	}
	return s.writeJSON(payload)
}

// chatClientInfo 提取客户端形态声明，缺省为 web
func chatClientInfo(data map[string]any) map[string]any {
	if data != nil {
		if raw, ok := data["client"].(map[string]any); ok && raw != nil {
			out := make(map[string]any, len(raw)+1)
			for key, value := range raw {
				out[key] = value
			}
			if strings.TrimSpace(wsAnyString(out["type"])) == "" {
				out["type"] = "web"
			}
			return out
		}
	}
	return map[string]any{"type": "web"}
}

func chatClientMode(clientInfo map[string]any) string {
	if clientInfo == nil {
		return "web"
	}
	if mode := strings.TrimSpace(wsAnyString(clientInfo["type"])); mode != "" {
		return mode
	}
	return "web"
}

// normalizeChatVoices 校验语音附件，对齐 normalize_chat_voices：最多 4 条
func normalizeChatVoices(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	voices := make([]any, 0, len(items))
	for _, item := range items {
		if len(voices) >= 4 {
			break
		}
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mimeType := strings.TrimSpace(wsAnyString(raw["mime_type"]))
		if mimeType == "" {
			mimeType = strings.TrimSpace(wsAnyString(raw["mimeType"]))
		}
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		if !strings.HasPrefix(mimeType, "audio/") {
			continue
		}
		encoded := strings.TrimSpace(wsAnyString(raw["base64"]))
		dataURL := strings.TrimSpace(wsAnyString(raw["data_url"]))
		if dataURL == "" {
			dataURL = strings.TrimSpace(wsAnyString(raw["dataUrl"]))
		}
		if encoded == "" && strings.HasPrefix(dataURL, "data:audio/") {
			if index := strings.Index(dataURL, ","); index >= 0 {
				encoded = strings.TrimSpace(dataURL[index+1:])
			}
		}
		if encoded == "" {
			continue
		}
		if _, err := base64.StdEncoding.Strict().DecodeString(encoded); err != nil {
			continue
		}
		voices = append(voices, map[string]any{
			"name":      strings.TrimSpace(wsAnyString(raw["name"])),
			"mime_type": mimeType,
			"base64":    encoded,
		})
	}
	return voices
}

// normalizeChatFiles 校验文件附件，对齐 normalize_chat_files：最多 6 个且必须带文件名与内容
func normalizeChatFiles(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("附件必须是列表")
	}
	if len(items) > 6 {
		return nil, fmt.Errorf("每条消息最多包含 6 个附件")
	}
	files := make([]any, 0, len(items))
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fileRef := strings.TrimSpace(wsAnyString(raw["file_ref"])); fileRef != "" {
			files = append(files, map[string]any{
				"file_ref": fileRef,
				"name":     wsAnyString(raw["name"]),
			})
			continue
		}
		name := strings.TrimSpace(wsAnyString(raw["name"]))
		if name == "" {
			name = strings.TrimSpace(wsAnyString(raw["file_name"]))
		}
		if name == "" {
			name = strings.TrimSpace(wsAnyString(raw["filename"]))
		}
		encoded := strings.TrimSpace(wsAnyString(raw["base64"]))
		if name == "" || encoded == "" {
			return nil, fmt.Errorf("附件缺少文件名或内容")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("附件 Base64 内容无效")
		}
		mimeType := strings.TrimSpace(wsAnyString(raw["mime_type"]))
		if mimeType == "" {
			mimeType = strings.TrimSpace(wsAnyString(raw["mimeType"]))
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		size := len(decoded)
		if rawSize, ok := raw["size"].(float64); ok && rawSize > 0 {
			size = int(rawSize)
		}
		files = append(files, map[string]any{
			"name":      name,
			"mime_type": mimeType,
			"size":      size,
			"base64":    encoded,
		})
	}
	return files, nil
}

// buildChatProcessedPlainText 拼接展示文本，对齐 build_processed_plain_text
func buildChatProcessedPlainText(content string, attachments chatAttachments) string {
	parts := make([]string, 0, 5+len(attachments.files))
	if content != "" {
		parts = append(parts, content)
	}
	switch len(attachments.images) {
	case 0:
	case 1:
		parts = append(parts, "[图片]")
	default:
		parts = append(parts, fmt.Sprintf("[图片 x%d]", len(attachments.images)))
	}
	switch len(attachments.emojis) {
	case 0:
	case 1:
		parts = append(parts, "[表情]")
	default:
		parts = append(parts, fmt.Sprintf("[表情 x%d]", len(attachments.emojis)))
	}
	switch len(attachments.voices) {
	case 0:
	case 1:
		parts = append(parts, "[语音]")
	default:
		parts = append(parts, fmt.Sprintf("[语音 x%d]", len(attachments.voices)))
	}
	for _, file := range attachments.files {
		raw, _ := file.(map[string]any)
		name := strings.TrimSpace(wsAnyString(raw["name"]))
		parts = append(parts, strings.TrimSpace("[文件] "+name))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// normalizeChatImages 校验图片附件，对齐 normalize_chat_images：最多 8 张、base64 严格解码
func normalizeChatImages(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	images := make([]any, 0, len(items))
	for _, item := range items {
		if len(images) >= 8 {
			break
		}
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mimeType := strings.TrimSpace(wsAnyString(raw["mime_type"]))
		if mimeType == "" {
			mimeType = strings.TrimSpace(wsAnyString(raw["mimeType"]))
		}
		if mimeType == "" {
			mimeType = "image/png"
		}
		if !strings.HasPrefix(mimeType, "image/") {
			continue
		}
		encoded := strings.TrimSpace(wsAnyString(raw["base64"]))
		dataURL := strings.TrimSpace(wsAnyString(raw["data_url"]))
		if dataURL == "" {
			dataURL = strings.TrimSpace(wsAnyString(raw["dataUrl"]))
		}
		if encoded == "" && strings.HasPrefix(dataURL, "data:image/") {
			if index := strings.Index(dataURL, ","); index >= 0 {
				encoded = strings.TrimSpace(dataURL[index+1:])
			}
		}
		if encoded == "" {
			continue
		}
		if _, err := base64.StdEncoding.Strict().DecodeString(encoded); err != nil {
			continue
		}
		images = append(images, map[string]any{
			"name":      strings.TrimSpace(wsAnyString(raw["name"])),
			"mime_type": mimeType,
			"base64":    encoded,
		})
	}
	return images
}

func truthyChatValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != "" && strings.TrimSpace(v) != "false"
	default:
		return false
	}
}

func chatTimestamp() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
