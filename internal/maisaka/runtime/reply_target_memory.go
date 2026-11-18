package runtime

import (
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
// 历史条目需要 Planner 前缀（msg_id/quote/time/user/is_self_message）与带说话人的
// visible_text 两个字段；只有写入历史后，_find_recent_reply_to_target 才能反查到重复回复的目标正文。
func (s *Session) appendGuidedReplyToHistory(messageID int64, replyTo int64, content string, at time.Time, speakerName string) {
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
	messageIDText := ""
	if messageID != 0 {
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
	metadata := map[string]any{}
	if replyTo != 0 {
		metadata["reply_to"] = strconv.FormatInt(replyTo, 10)
		// 引用预览在发送时固化，对齐 Python ReplyComponent 携带目标正文与发送者
		if source, ok := runtime.FindSourceMessageByID(strconv.FormatInt(replyTo, 10)); ok {
			if content := strings.TrimSpace(maisaka.BuildMonitorMessageContent(source)); content != "" {
				metadata["quote_content"] = content
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
	message := maisaka.ContextMessage{
		Role:           maisaka.RoleAssistant,
		Content:        plannerPrefix + text,
		VisibleText:    maisakactx.FormatSpeakerContent(speaker, text, &at, messageIDText),
		Source:         maisaka.SourceGuidedReply,
		Timestamp:      at,
		MessageID:      messageIDText,
		SessionID:      runtime.SessionID(),
		CountInContext: true,
		Metadata:       metadata,
	}
	runtime.AppendHistory(message)
	if s.maisakaFocusGroup != nil {
		s.maisakaFocusGroup.RecordSentMessageForSession(runtime.SessionID(), message)
	}
	runtime.EmitMonitorMessageSent(message, speaker, maisaka.SourceGuidedReply)
}

// appendSentImagesToHistory 把已发送的图片作为独立 guided_reply 条目写入历史
//
// 对齐 Python reply 工具的 attach_pic：图片作为独立 PostProcessedReplyMessage 逐条发送，
// 每条都经 send_service 同步进 Maisaka 历史并调度后台识图，供后续轮次引用。
func (s *Session) appendSentImagesToHistory(messageID int64, images []maisaka.ImageComponent, at time.Time, speakerName string) {
	if s == nil || len(images) == 0 {
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
	placeholder := maisaka.ImagePlaceholderText(images)
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
	message := maisaka.ContextMessage{
		Role:           maisaka.RoleAssistant,
		Content:        plannerPrefix + placeholder,
		VisibleText:    maisakactx.FormatSpeakerContent(speaker, placeholder, &at, messageIDText),
		Source:         maisaka.SourceGuidedReply,
		Timestamp:      at,
		MessageID:      messageIDText,
		SessionID:      runtime.SessionID(),
		CountInContext: true,
		Images:         images,
		Metadata:       map[string]any{"kind": "image"},
	}
	runtime.AppendHistory(message)
	if s.maisakaFocusGroup != nil {
		s.maisakaFocusGroup.RecordSentMessageForSession(runtime.SessionID(), message)
	}
	runtime.EmitMonitorMessageSent(message, speaker, maisaka.SourceGuidedReply)
	runtime.ScheduleSentImageRecognition(images)
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
