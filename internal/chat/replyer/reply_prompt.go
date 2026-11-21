package replyer

import (
	"fmt"
	"strings"
	"time"
)

const (
	replyGroupPrompt               = "reply_group"
	replyGroupMaiReplierPrompt     = "reply_group_mai_replier"
	replyPrivatePrompt             = "reply_private"
	replyPrivateMaiReplierPrompt   = "reply_private_mai_replier"
	replyPrivateFollowupPrompt     = "reply_private_followup"
	replyPrivateFollowupMaiReplier = "reply_private_followup_mai_replier"
	shortReplyStyle                = "简短表达"
)

func (b *PromptBuilder) buildReplyRequestMessages(req ReplyRequest, maiReplier bool) ([]RequestMessage, error) {
	history := make([]ChatMessage, 0, len(req.History))
	for _, message := range req.History {
		if message.IsNotify {
			continue
		}
		switch message.SourceKind {
		case "", "user", "guided_reply":
			history = append(history, message)
		}
	}
	promptName := b.selectReplyPromptName(req, maiReplier)
	scene := "private"
	if b.isGroupChat() {
		scene = "group"
	}
	systemTemplate := "reply_" + scene + "_system"
	attention, drift := b.BuildGroupChatAttentionBlock(), strings.TrimSpace(b.Config.AttentionDriftBlock)
	if maiReplier {
		systemTemplate = "reply_mai_replier_" + scene + "_system"
		attention, drift = "", ""
	}
	systemPrompt, err := b.LoadPrompt(systemTemplate, map[string]string{
		"identity": b.BuildPersonalityPrompt(), "chat_attention_block": attention, "attention_drift_block": drift,
	})
	if err != nil {
		return nil, fmt.Errorf("加载回复系统模板 %s 失败: %w", systemTemplate, err)
	}
	systemPrompt = strings.TrimSpace(systemPrompt)
	req.ReplyRequirements = b.BuildReplyRequirements(req.ReplyRequirements, req.RetryConstraints)
	if !maiReplier {
		extraBlocks := []string{req.ReplyRequirements}
		args := req.ReplyToolArgs
		if reminder := strings.TrimSpace(stringValue(args[duplicateTargetReplyReminderArg])); reminder != "" {
			extraBlocks = append(extraBlocks, reminder)
		}
		attachment, err := b.BuildReplyAttachmentPrompt(history, req.Target, args)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(attachment) != "" {
			extraBlocks = append(extraBlocks, "【额外发送内容参考】\n"+strings.TrimSpace(attachment))
		}
		req.ReplyRequirements = joinBlocks("\n\n", extraBlocks...)
	}
	requestedStyle := strings.TrimSpace(stringValue(req.ReplyToolArgs["reply_style"]))
	if !maiReplier && requestedStyle == "" && req.ThinkLevel == 0 {
		requestedStyle = shortReplyStyle
	}
	guidance := ReplyGuidance(b.Config.PromptLocale, b.isGroupChat())[requestedStyle]
	if guidance == "" {
		guidance = ReplyGuidance(b.Config.PromptLocale, b.isGroupChat())["正常回复"]
	}
	var visualImages []retroVisualImage
	if req.EnableVisualMessage {
		dialogue, images, sent := b.buildRetroVisualRequest(history, resolveMaxImageNum(req.MaxImageNum), req.ImageQuality)
		visualImages = images
		// 目标消息与引用共用去掉视觉描述后的副本，不修改真实消息
		if req.Target != nil {
			for messageIndex, message := range history {
				if message.MessageID != "" && message.MessageID == req.Target.MessageID {
					req.Target = retroTargetWithoutVisual(req.Target, sent[messageIndex])
					break
				}
			}
		}
		req.History = history
		templateContext, err := b.buildReplyTemplateContext(history, req, maiReplier, guidance)
		if err != nil {
			return nil, err
		}
		templateContext["dialogue_prompt"] = dialogue
		userMessage, err := b.LoadPrompt(promptName, templateContext)
		if err != nil {
			return nil, fmt.Errorf("加载回复模板 %s 失败: %w", promptName, err)
		}
		userRequest := RequestMessage{Role: "user", Content: userMessage}
		if len(visualImages) > 0 {
			parts := splitRetroVisualParts(userMessage, visualImages)
			userRequest.Parts = parts
			userRequest.Content = requestPartsText(parts)
		}
		return []RequestMessage{
			{Role: "system", Content: systemPrompt},
			userRequest,
		}, nil
	}
	templateContext, err := b.buildReplyTemplateContext(history, req, maiReplier, guidance)
	if err != nil {
		return nil, err
	}
	userMessage, err := b.LoadPrompt(promptName, templateContext)
	if err != nil {
		return nil, fmt.Errorf("加载回复模板 %s 失败: %w", promptName, err)
	}
	return []RequestMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}, nil
}

// joinBlocks 按顺序拼接非空块
func joinBlocks(separator string, parts ...string) string {
	blocks := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			blocks = append(blocks, trimmed)
		}
	}
	return strings.Join(blocks, separator)
}

// retroTargetWithoutVisual 把已发送图片/表情包替换为文本标记，对齐 Python 的目标消息副本
func retroTargetWithoutVisual(target *ReplyTarget, sent map[int]bool) *ReplyTarget {
	if target == nil || len(sent) == 0 || len(target.Parts) == 0 {
		return target
	}
	copied := *target
	parts := make([]MessagePart, 0, len(target.Parts))
	for partIndex, part := range target.Parts {
		if sent[partIndex] {
			switch part.Kind {
			case PartImage:
				parts = append(parts, MessagePart{Kind: PartText, Text: "[图片]"})
				continue
			case PartEmoji:
				parts = append(parts, MessagePart{Kind: PartText, Text: "[表情包]"})
				continue
			}
		}
		parts = append(parts, part)
	}
	copied.Parts = parts
	copied.Content = buildTextFromParts(parts)
	return &copied
}

func (b *PromptBuilder) selectReplyPromptName(req ReplyRequest, maiReplier bool) string {
	if b.isGroupChat() {
		if maiReplier {
			return replyGroupMaiReplierPrompt
		}
		return replyGroupPrompt
	}
	selfReply := req.Target != nil && req.Target.IsBotSelf
	if maiReplier {
		if selfReply {
			return replyPrivateFollowupMaiReplier
		}
		return replyPrivateMaiReplierPrompt
	}
	if selfReply {
		return replyPrivateFollowupPrompt
	}
	return replyPrivatePrompt
}

func (b *PromptBuilder) isGroupChat() bool {
	return b.Config.IsGroupChat != nil && *b.Config.IsGroupChat
}

func (b *PromptBuilder) buildReplyTemplateContext(history []ChatMessage, req ReplyRequest, maiReplier bool, guidance string) (map[string]string, error) {
	thought := strings.TrimSpace(firstNonEmpty(stringValue(req.ReplyToolArgs["chat_anlyze"]), req.ReplyReason))
	analysis := ""
	if thought != "" {
		var err error
		analysis, err = b.LoadPrompt("reply_conversation_analysis", map[string]string{"chat_anlyze": thought})
		if err != nil {
			return nil, err
		}
	}
	intent, _ := req.ReplyToolArgs["expression_intent"].(map[string]any)
	combinedReference := joinBlocks("\n\n", analysis, expressionIntentGuidance(b.Config.PromptLocale, intent))
	replyStyle := b.SelectTemporaryReplyStyle()
	if strings.TrimSpace(replyStyle) == "" {
		replyStyle = b.SelectReplyStyle()
	}
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	expressionHabitsBlock := ""
	if !maiReplier {
		expressionHabitsBlock = strings.TrimSpace(req.ExpressionHabits)
	}
	replyTargetBlock := b.BuildRetroReplyTargetBlock(req.Target, history)
	if replyTargetBlock != "" {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.Config.PromptLocale)), "en") {
			replyTargetBlock += "."
		} else {
			replyTargetBlock += "。"
		}
	}
	return map[string]string{
		"bot_name":                b.Config.BotName,
		"sender_name":             b.BuildRetroSenderName(history, req.Target),
		"reply_style":             replyStyle,
		"expression_habits_block": expressionHabitsBlock,
		"extra_info_block":        BuildRetroExtraInfoBlock(req.ReplyRequirements),
		"dialogue_prompt":         b.BuildRetroDialogueBlock(history),
		"time_block":              "当前时间：" + now.Format("2006-01-02 15:04:05"),
		"reply_target_block":      replyTargetBlock,
		"planner_reasoning":       combinedReference,
		"reply_guidance":          guidance,
	}, nil
}

// BuildRetroExtraInfoBlock 按旧版措辞包裹回复要求
func BuildRetroExtraInfoBlock(replyRequirements string) string {
	normalized := strings.TrimSpace(replyRequirements)
	if normalized == "" {
		return ""
	}
	return "以下是你在回复时需要参考的信息，现在请你阅读以下内容，进行决策\n" +
		normalized + "\n" +
		"以上是你在回复时需要参考的信息，现在请你阅读以下内容，进行决策"
}

// BuildRetroSenderName 优先使用本次回复目标发送者，否则取最近一条用户发言的发送者
func (b *PromptBuilder) BuildRetroSenderName(history []ChatMessage, target *ReplyTarget) string {
	if target != nil {
		if senderName := firstNonEmpty(target.SenderName, target.SenderID); senderName != "" {
			return senderName
		}
	}
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.SourceKind != "user" {
			continue
		}
		if senderName := strings.TrimSpace(message.SenderName); senderName != "" {
			return senderName
		}
	}
	return ""
}

// BuildRetroReplyTargetBlock 按旧版措辞描述目标消息
func (b *PromptBuilder) BuildRetroReplyTargetBlock(target *ReplyTarget, history []ChatMessage) string {
	if target == nil {
		return ""
	}
	senderName := firstNonEmpty(target.SenderName, target.SenderID)
	references := b.BuildRetroReferences(target.Quotes, history)
	targetContent := normalizeReplyContent(b.BuildTargetMessageContent(target), retroTargetContentLimit)
	if targetContent == "" && len(references) == 0 {
		return ""
	}
	referenceText := strings.Join(references, " ")
	if target.IsBotSelf {
		if len(references) > 0 {
			return fmt.Sprintf("你现在想补充说明你刚刚自己的发言内容：你%s 说：%s", referenceText, targetContent)
		}
		return fmt.Sprintf("你现在想补充说明你刚刚自己的发言内容：%s", targetContent)
	}
	if len(references) > 0 {
		return fmt.Sprintf("现在%s%s 说：%s 引起了你的注意", senderName, referenceText, targetContent)
	}
	return fmt.Sprintf("现在%s说的：%s 引起了你的注意", senderName, targetContent)
}

// BuildRetroDialogueBlock 把聊天历史渲染成旧版可读文本块
func (b *PromptBuilder) BuildRetroDialogueBlock(history []ChatMessage) string {
	lines := make([]string, 0, len(history))
	historyByID := make(map[string]ChatMessage, len(history))
	for _, message := range history {
		if message.MessageID != "" {
			historyByID[message.MessageID] = message
		}
	}
	for _, message := range history {
		if message.IsNotify {
			continue
		}
		if line := b.RenderRetroHistoryLine(message, historyByID); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// RenderRetroHistoryLine 渲染单条历史正文与引用关系
func (b *PromptBuilder) RenderRetroHistoryLine(message ChatMessage, historyByID map[string]ChatMessage) string {
	content := strings.TrimSpace(firstNonEmpty(message.Content, buildTextFromParts(message.Parts)))
	references := b.BuildRetroReferences(message.Quotes, mapValues(historyByID))
	if content == "" && len(references) == 0 {
		return ""
	}
	speaker := strings.TrimSpace(message.SenderName)
	botName := b.Config.BotName
	if message.SourceKind == "guided_reply" {
		speaker = botName + "(你)"
	} else if botName != "" && speaker == botName {
		speaker = "你"
	}
	timestamp := "00:00:00"
	if !message.Timestamp.IsZero() {
		timestamp = message.Timestamp.Format("15:04:05")
	}
	referenceText := strings.Join(references, " ")
	if len(references) > 0 {
		return fmt.Sprintf("[%s] %s%s 说：%s", timestamp, speaker, referenceText, content)
	}
	if speaker != "" {
		return fmt.Sprintf("[%s] %s说：%s", timestamp, speaker, content)
	}
	return fmt.Sprintf("[%s]%s", timestamp, content)
}

// BuildRetroReferences 渲染引用消息；优先使用上下文中最新原消息
func (b *PromptBuilder) BuildRetroReferences(quotes []QuoteReference, history []ChatMessage) []string {
	historyByID := make(map[string]ChatMessage, len(history))
	for _, message := range history {
		if message.MessageID != "" {
			historyByID[message.MessageID] = message
		}
	}
	references := make([]string, 0, len(quotes))
	for _, quote := range quotes {
		target, ok := historyByID[quote.MessageID]
		sender := quote.Sender
		content := quote.Content
		if ok {
			sender = firstNonEmpty(target.SenderName, target.SenderID, quote.Sender)
			content = firstNonEmpty(target.Content, buildTextFromParts(target.Parts), quote.Content)
		}
		targetName := firstNonEmpty(sender, quote.MessageID)
		content = strings.TrimSpace(content)
		if content != "" {
			references = append(references, fmt.Sprintf("[回复了%s的消息: %s]", targetName, content))
		} else {
			references = append(references, fmt.Sprintf("[回复了%s的消息]", targetName))
		}
	}
	return references
}

func mapValues(historyByID map[string]ChatMessage) []ChatMessage {
	values := make([]ChatMessage, 0, len(historyByID))
	for _, message := range historyByID {
		values = append(values, message)
	}
	return values
}
