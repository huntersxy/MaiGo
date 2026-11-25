package runtime

import (
	"context"
	"strconv"
	"strings"
	"time"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/maisaka"
	maisakactx "MaiGo/internal/maisaka/context"
)

// replyTargetMemory 返回会话的回复目标记忆，按需初始化并挂上历史反查
func (s *Session) replyTargetMemory() *chatsession.ReplyTargetMemory {
	if s == nil {
		return nil
	}
	s.replyTargetsMu.Lock()
	defer s.replyTargetsMu.Unlock()
	if s.replyTargets == nil {
		s.replyTargets = chatsession.NewReplyTargetMemory()
		s.replyTargets.SetHistoryLookup(s.recentReplyToTargetFromHistory)
	}
	return s.replyTargets
}

// appendGuidedReplyToHistory 把一条已成功发送的自身发言写入 Maisaka 历史
//
// 对齐 Python send_service._sync_sent_message_to_maisaka_history +
// maisaka/runtime.py::append_sent_message_to_chat_history(source_kind="guided_reply")：
// 使用 user 角色与 source_kind="guided_reply"，同时保留 Planner 前缀与干净的 visible_text / visible_body。
func (s *Session) appendGuidedReplyToHistory(messageID int64, externalMessageID string, replyTo int64, content string, at time.Time, speakerName string, selfID int64) {
	if s == nil {
		return
	}
	text := strings.TrimSpace(content)
	if text == "" {
		return
	}
	runtime := s.MaisakaRuntime()
	if runtime == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	messageIDText := strings.TrimSpace(externalMessageID)
	if messageIDText == "" && messageID != 0 {
		messageIDText = strconv.FormatInt(messageID, 10)
	}
	quoteIDs := make([]string, 0, 1)
	if replyTo != 0 {
		quoteIDs = append(quoteIDs, strconv.FormatInt(replyTo, 10))
	}
	speaker := strings.TrimSpace(speakerName)
	plannerPrefix := maisakactx.BuildPlannerPrefix(maisakactx.PlannerPrefixInput{
		Timestamp:        at,
		UserName:         speaker,
		MessageID:        messageIDText,
		ChatID:           runtime.SessionID(),
		ChatName:         runtime.SessionName(),
		QuoteIDs:         quoteIDs,
		IncludeMessageID: messageIDText != "",
		IncludeChatID:    runtime.FocusModeActive(),
		IsSelfMessage:    true,
	})
	visibleText := maisakactx.FormatSpeakerContent(speaker, text, &at, messageIDText)
	metadata := map[string]any{
		"source_kind":       maisaka.SourceGuidedReply,
		"sender_name":       speaker,
		"nickname":          speaker,
		"visible_text":      visibleText,
		"visible_body":      text,
		"message_id":        messageIDText,
		"timestamp_unix_ms": at.UnixMilli(),
	}
	if selfID > 0 {
		metadata["sender_id"] = strconv.FormatInt(selfID, 10)
	}
	if replyTo != 0 {
		replyID := strconv.FormatInt(replyTo, 10)
		metadata["reply_to"] = replyID
		metadata["quote_ids"] = []string{replyID}
		// 引用预览在发送时固化，对齐 Python ReplyComponent 携带目标正文与发送者
		if source, ok := runtime.FindSourceMessageByID(replyID); ok {
			if quoteBody := strings.TrimSpace(maisaka.BuildMonitorMessageContent(source)); quoteBody != "" {
				metadata["quote_content"] = quoteBody
			}
			if sender := monitorSpeakerName(source); sender != "" {
				metadata["quote_sender"] = sender
			}
			if cardname := source.MetadataString("sender_cardname"); cardname != "" {
				metadata["quote_sender_cardname"] = cardname
			}
			if nickname := firstNonEmptyString(source.MetadataString("sender_name"), source.MetadataString("nickname")); nickname != "" {
				metadata["quote_sender_nickname"] = nickname
			}
		}
	}
	userIDText := ""
	if selfID > 0 {
		userIDText = strconv.FormatInt(selfID, 10)
	}
	message := maisaka.ContextMessage{
		Role:           maisaka.RoleUser,
		Content:        plannerPrefix + text,
		VisibleText:    visibleText,
		Source:         maisaka.SourceGuidedReply,
		Timestamp:      at,
		MessageID:      messageIDText,
		UserID:         userIDText,
		SessionID:      runtime.SessionID(),
		CountInContext: true,
		Metadata:       metadata,
	}
	runtime.AppendHistory(message)
	s.persistGuidedReplyIfStandalone(message)
	if s.maisakaFocusGroup != nil {
		s.maisakaFocusGroup.RecordSentMessageForSession(runtime.SessionID(), message)
	}
	runtime.EmitMonitorMessageSent(message, speaker, maisaka.SourceGuidedReply)
}

// appendSentImagesToHistory 把已发送的图片作为独立 guided_reply 条目写入历史
func (s *Session) appendSentImagesToHistory(messageID int64, images []maisaka.ImageComponent, at time.Time, speakerName string) {
	s.appendSentMediaToHistory(messageID, images, nil, maisaka.ImagePlaceholderText(images), "image", at, speakerName)
}

// appendSentEmojiToHistory 把已发送的表情包作为独立 guided_reply 条目写入历史
func (s *Session) appendSentEmojiToHistory(messageID int64, emoji maisaka.ImageComponent, at time.Time, speakerName string) {
	placeholder := strings.TrimSpace(emoji.Description)
	if placeholder == "" {
		placeholder = "[表情包]"
	}
	s.appendSentMediaToHistory(messageID, nil, []maisaka.ImageComponent{emoji}, placeholder, "emoji", at, speakerName)
}

func (s *Session) appendSentMediaToHistory(messageID int64, images []maisaka.ImageComponent, emojis []maisaka.ImageComponent, placeholder, kind string, at time.Time, speakerName string) {
	if s == nil || (len(images) == 0 && len(emojis) == 0) {
		return
	}
	runtime := s.MaisakaRuntime()
	if runtime == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	messageIDText := ""
	if messageID != 0 {
		messageIDText = strconv.FormatInt(messageID, 10)
	}
	speaker := strings.TrimSpace(speakerName)
	plannerPrefix := maisakactx.BuildPlannerPrefix(maisakactx.PlannerPrefixInput{
		Timestamp:        at,
		UserName:         speaker,
		MessageID:        messageIDText,
		ChatID:           runtime.SessionID(),
		ChatName:         runtime.SessionName(),
		QuoteIDs:         nil,
		IncludeMessageID: messageIDText != "",
		IncludeChatID:    runtime.FocusModeActive(),
		IsSelfMessage:    true,
	})
	visibleText := maisakactx.FormatSpeakerContent(speaker, placeholder, &at, messageIDText)
	message := maisaka.ContextMessage{
		Role:           maisaka.RoleUser,
		Content:        plannerPrefix + placeholder,
		VisibleText:    visibleText,
		Source:         maisaka.SourceGuidedReply,
		Timestamp:      at,
		MessageID:      messageIDText,
		SessionID:      runtime.SessionID(),
		CountInContext: true,
		Images:         images,
		Emojis:         emojis,
		Metadata: map[string]any{
			"kind":              kind,
			"source_kind":       maisaka.SourceGuidedReply,
			"sender_name":       speaker,
			"nickname":          speaker,
			"visible_text":      visibleText,
			"visible_body":      placeholder,
			"message_id":        messageIDText,
			"timestamp_unix_ms": at.UnixMilli(),
		},
	}
	runtime.AppendHistory(message)
	s.persistGuidedReplyIfStandalone(message)
	if s.maisakaFocusGroup != nil {
		s.maisakaFocusGroup.RecordSentMessageForSession(runtime.SessionID(), message)
	}
	runtime.EmitMonitorMessageSent(message, speaker, maisaka.SourceGuidedReply)
	if len(images) > 0 {
		runtime.ScheduleSentImageRecognition(images)
	}
	if len(emojis) > 0 {
		runtime.ScheduleSentImageRecognition(emojis)
	}
}

func (s *Session) persistGuidedReplyIfStandalone(message maisaka.ContextMessage) {
	if s == nil || s.memory == nil {
		return
	}
	runtime := s.MaisakaRuntime()
	if runtime != nil && runtime.IsTranscriptRecording() {
		return
	}
	schemaMsg := maisakaGuidedReplySchemaMessage(message)
	if schemaMsg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entry, err := s.AppendGuidedReply(ctx, maisakaTranscriptRunID, schemaMsg)
	if err != nil || entry == nil {
		return
	}
	seqs, err := s.memory.MarkVisibleAndPersistReceipts(ctx, s.kind, s.targetID, []uint{entry.ID}, nil)
	if err == nil {
		s.ApplyVisibleSeqs(seqs)
	}
}

// previousReplyToTarget 返回本会话最近一次回复同一目标消息的正文
func (s *Session) previousReplyToTarget(msgID string) string {
	memory := s.replyTargetMemory()
	if memory == nil {
		return ""
	}
	return memory.Previous(msgID)
}

// recordReplyToTarget 记录成功发送的回复目标与正文
func (s *Session) recordReplyToTarget(msgID string, content string) {
	memory := s.replyTargetMemory()
	if memory == nil {
		return
	}
	memory.Record(msgID, content)
}

// recentReplyToTargetFromHistory 在 Maisaka 历史中反查最近一次引用该目标的自身发言
//
// 对齐 Python _extract_guided_reply_text：优先从 visible_text 去掉说话人前缀取正文，
// 取不到时退回 Content（并去掉 Planner 前缀），返回压缩空白后的单行文本。
func (s *Session) recentReplyToTargetFromHistory(msgID string) string {
	runtime := s.MaisakaRuntime()
	if runtime == nil {
		return ""
	}
	history := runtime.History()
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Source != maisaka.SourceGuidedReply {
			continue
		}
		if !contextMessageQuotesTarget(message, msgID) {
			continue
		}
		if text := guidedReplyBodyText(message); text != "" {
			return text
		}
	}
	return ""
}

// guidedReplyBodyText 提取已发送回复的可读正文，去掉 Planner 前缀与说话人标记
func guidedReplyBodyText(message maisaka.ContextMessage) string {
	if visible := strings.TrimSpace(message.VisibleText); visible != "" {
		if _, body := maisakactx.ParseSpeakerContent(visible); strings.TrimSpace(body) != "" {
			return strings.Join(strings.Fields(body), " ")
		}
	}
	content := strings.TrimSpace(message.Content)
	if strings.HasPrefix(strings.ToLower(content), "<message") {
		if end := strings.Index(content, ">"); end >= 0 {
			content = strings.TrimSpace(content[end+1:])
		}
	}
	return strings.Join(strings.Fields(content), " ")
}

// contextMessageQuotesTarget 判断历史消息是否引用了目标消息编号
//
// 引用关系由 reply 段的 target_message_id 承载，入站转换时写入 Metadata["reply_to"]；
// 也兼容 Metadata["quote_ids"] 里的多引用列表。
func contextMessageQuotesTarget(message maisaka.ContextMessage, msgID string) bool {
	if replyTo := strings.TrimSpace(message.MetadataString("reply_to")); replyTo == msgID {
		return true
	}
	if message.Metadata == nil {
		return false
	}
	switch quotes := message.Metadata["quote_ids"].(type) {
	case []string:
		for _, quote := range quotes {
			if strings.TrimSpace(quote) == msgID {
				return true
			}
		}
	case []any:
		for _, raw := range quotes {
			if text, ok := raw.(string); ok && strings.TrimSpace(text) == msgID {
				return true
			}
		}
	}
	return false
}
