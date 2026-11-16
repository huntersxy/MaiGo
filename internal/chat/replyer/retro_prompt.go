package replyer

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	retroGroupPrompt       = "retro_replyer"
	retroGroupLightPrompt  = "retro_replyer_light"
	retroPrivatePrompt     = "retro_private_replyer"
	retroPrivateSelfPrompt = "retro_private_replyer_self"
	shortReplyStyle        = "简短表达"
)

// BuildRetroRequestMessages 对齐 RetroReplyPromptMixin._build_retro_request_messages
func (b *PromptBuilder) BuildRetroRequestMessages(ctx context.Context, req ReplyRequest, maiReplier bool) ([]RequestMessage, error) {
	_ = ctx
	history := make([]ChatMessage, 0, len(req.History))
	for _, message := range req.History {
		if message.IsNotify {
			continue
		}
		// 对齐 _is_replyer_filtered_history_message：复古模板只保留真实收发消息，
		// 排除 Planner 模型输出等内部上下文
		switch message.SourceKind {
		case "", "user", "guided_reply":
			history = append(history, message)
		}
	}
	promptName := b.SelectRetroPromptName(history, req, maiReplier)
	systemPrompt := b.BuildPersonalityPrompt()
	if !maiReplier {
		systemPrompt = joinBlocks("\n\n",
			systemPrompt,
			b.BuildGroupChatAttentionBlock(),
			strings.TrimSpace(b.Config.AttentionDriftBlock),
		)
		// 通用复古保留普通回复中的附件参考、重复回复提醒及篇幅要求
		extraBlocks := []string{req.ReplyRequirements}
		args := req.ReplyToolArgs
		if reminder := strings.TrimSpace(stringValue(args[duplicateTargetReplyReminderArg])); reminder != "" {
			extraBlocks = append(extraBlocks, reminder)
		}
		if attachment, err := b.BuildReplyAttachmentPrompt(history, req.Target, args); err == nil {
			if strings.TrimSpace(attachment) != "" {
				extraBlocks = append(extraBlocks, "【额外发送内容参考】\n"+strings.TrimSpace(attachment))
			}
		}
		extraBlocks = append(extraBlocks, BuildRequestedReplyStyleMessage(stringValue(args["reply_style"])))
		req.ReplyRequirements = joinBlocks("\n\n", extraBlocks...)
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
		templateContext := b.BuildRetroTemplateContext(history, req, maiReplier)
		templateContext["dialogue_prompt"] = dialogue
		userMessage, err := b.LoadPrompt(promptName, templateContext)
		if err != nil {
			return nil, fmt.Errorf("加载复古 replyer 模板 %s 失败: %w", promptName, err)
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
	templateContext := b.BuildRetroTemplateContext(history, req, maiReplier)
	userMessage, err := b.LoadPrompt(promptName, templateContext)
	if err != nil {
		return nil, fmt.Errorf("加载复古 replyer 模板 %s 失败: %w", promptName, err)
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

// SelectRetroPromptName 按群聊/私聊、是否补充自己发言选择复古模板
func (b *PromptBuilder) SelectRetroPromptName(history []ChatMessage, req ReplyRequest, maiReplier bool) string {
	if b.IsRetroGroupChat(req) {
		// Mai Replier 对齐训练分布：群聊恒用标准模板；通用复古保留简短回复模板
		if !maiReplier && IsRetroShortReply(req.ThinkLevel, req.ReplyToolArgs) {
			return retroGroupLightPrompt
		}
		return retroGroupPrompt
	}
	if req.Target != nil && req.Target.IsBotSelf {
		return retroPrivateSelfPrompt
	}
	return retroPrivatePrompt
}

// IsRetroGroupChat 判断当前会话是否为群聊
func (b *PromptBuilder) IsRetroGroupChat(req ReplyRequest) bool {
	if b.Config.IsGroupChat != nil {
		return *b.Config.IsGroupChat
	}
	return false
}

// IsRetroShortReply 判断是否属于简短回复（保留旧版 think_level=0 语义）
func IsRetroShortReply(thinkLevel int, replyToolArgs map[string]any) bool {
	if thinkLevel == 0 {
		return true
	}
	return strings.TrimSpace(stringValue(replyToolArgs["reply_style"])) == shortReplyStyle
}

// BuildRetroTemplateContext 按旧版块顺序准备模板占位符
func (b *PromptBuilder) BuildRetroTemplateContext(history []ChatMessage, req ReplyRequest, maiReplier bool) map[string]string {
	combinedReference := b.BuildReplyReferenceMessage(req.ReplyReason, req.ReplyReference)
	replyStyle := b.SelectTemporaryReplyStyle()
	if strings.TrimSpace(replyStyle) == "" {
		replyStyle = b.SelectReplyStyle()
	}
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	expressionHabitsBlock := ""
	keywordsReactionPrompt := ""
	if !maiReplier {
		expressionHabitsBlock = strings.TrimSpace(req.ExpressionHabits)
		if prompt, err := b.BuildKeywordReactionPrompt(history, req.Target); err == nil {
			keywordsReactionPrompt = prompt
		}
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
		"bot_name":                 b.Config.BotName,
		"sender_name":              b.BuildRetroSenderName(history, req.Target),
		"reply_style":              replyStyle,
		"expression_habits_block":  expressionHabitsBlock,
		"extra_info_block":         BuildRetroExtraInfoBlock(req.ReplyRequirements),
		"dialogue_prompt":          b.BuildRetroDialogueBlock(history),
		"time_block":               "当前时间：" + now.Format("2006-01-02 15:04:05"),
		"reply_target_block":       replyTargetBlock,
		"planner_reasoning":        combinedReference,
		"keywords_reaction_prompt": keywordsReactionPrompt,
	}
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
