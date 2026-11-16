package replyer

import (
	"fmt"
	"regexp"
	"strings"

	"MaiGo/internal/maisaka"
)

func normalizeReplyContent(content string, limit int) string {
	normalized := strings.Join(strings.Fields(content), " ")
	if limit <= 0 {
		return normalized
	}
	runes := []rune(normalized)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return normalized
}

func renderTargetAt(part MessagePart) string {
	name := firstNonEmpty(part.TargetUserCardname, part.TargetUserNickname, part.TargetUserID)
	return strings.TrimSpace("@" + name)
}

// BuildTargetMessageContent 对齐 _build_target_message_content
func (b *PromptBuilder) BuildTargetMessageContent(target *ReplyTarget) string {
	if target == nil {
		return ""
	}
	parts := make([]string, 0, len(target.Parts))
	for _, part := range target.Parts {
		switch part.Kind {
		case PartText:
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		case PartReply:
			continue
		case PartAt:
			if rendered := renderTargetAt(part); rendered != "" {
				parts = append(parts, rendered)
			}
		case PartImage:
			parts = append(parts, firstNonEmpty(strings.TrimSpace(part.Description), "[图片，识别中.....]"))
		case PartEmoji:
			parts = append(parts, firstNonEmpty(strings.TrimSpace(part.Description), "[表情包]"))
		case PartVoice:
			parts = append(parts, firstNonEmpty(strings.TrimSpace(part.Description), "[语音消息]"))
		}
	}
	rendered := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			rendered = append(rendered, trimmed)
		}
	}
	if normalized := strings.TrimSpace(strings.Join(rendered, " ")); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(target.Content)
}

// BuildTargetMessageBlock 对齐 _build_target_message_block
func (b *PromptBuilder) BuildTargetMessageBlock(target *ReplyTarget) string {
	if target == nil {
		return ""
	}
	senderName := firstNonEmpty(strings.TrimSpace(target.SenderName), target.SenderID)
	botName := strings.TrimSpace(b.Config.BotName)
	if botName == "" {
		botName = senderName
	}
	targetMessageID := maisaka.ToDisplayMessageID(target.MessageID)
	if targetMessageID == "" {
		targetMessageID = "未知"
	}
	targetContent := normalizeReplyContent(b.BuildTargetMessageContent(target), 300)
	if targetContent == "" {
		targetContent = "[无可见文本内容]"
	}
	if !target.IsBotSelf {
		return ""
	}
	return strings.Join([]string{
		fmt.Sprintf("你想要补充说明你自己（%s） 发送的 msg_id为 %s 的消息，你可以在这条目标消息的基础上补充发言，不要把你自己的发言当成别人的发言。", botName, targetMessageID),
		"- 你之前的发言内容：" + targetContent,
	}, "\n")
}

func normalizeAttachmentImages(raw []AttachmentImage) []AttachmentImage {
	return raw
}

func joinChineseItems(items []string) string {
	normalized := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	switch len(normalized) {
	case 0:
		return ""
	case 1:
		return normalized[0]
	case 2:
		return strings.Join(normalized, " 和 ")
	default:
		return strings.Join(normalized[:len(normalized)-1], "、") + " 和 " + normalized[len(normalized)-1]
	}
}

func findMessageByID(history []ChatMessage, target *ReplyTarget, messageID string) *ChatMessage {
	normalized := strings.TrimSpace(messageID)
	if normalized == "" {
		return nil
	}
	// 对齐 Python：命中 reply_message/target 时返回该目标（可取发送者名），不是 nil
	if target != nil && strings.TrimSpace(target.MessageID) == normalized {
		return &ChatMessage{
			MessageID:  target.MessageID,
			SenderName: target.SenderName,
			SenderID:   target.SenderID,
			Content:    target.Content,
		}
	}
	for index := len(history) - 1; index >= 0; index-- {
		if strings.TrimSpace(history[index].MessageID) == normalized {
			return &history[index]
		}
	}
	return nil
}

func (b *PromptBuilder) formatAttachmentImageTarget(image AttachmentImage, history []ChatMessage, target *ReplyTarget) string {
	if strings.TrimSpace(image.MediaIndex) != "" {
		return fmt.Sprintf("工具返回媒体 %s 中的第 %d 张图片", strings.TrimSpace(image.MediaIndex), image.Index+1)
	}
	message := findMessageByID(history, target, image.MessageID)
	displayID := maisaka.ToDisplayMessageID(image.MessageID)
	if message == nil {
		return fmt.Sprintf("msg_id=%s 的第 %d 张图片", displayID, image.Index+1)
	}
	senderName := firstNonEmpty(strings.TrimSpace(message.SenderName), message.SenderID)
	return fmt.Sprintf("%s 的消息 msg_id=%s 中的第 %d 张图片", senderName, displayID, image.Index+1)
}

// BuildReplyAttachmentPrompt 对齐 _build_reply_attachment_prompt
func (b *PromptBuilder) BuildReplyAttachmentPrompt(history []ChatMessage, target *ReplyTarget, replyToolArgs map[string]any) (string, error) {
	if len(replyToolArgs) == 0 {
		return "", nil
	}
	lines := make([]string, 0, 2)
	images := normalizeAttachmentImages(attachmentImagesFromArgs(replyToolArgs["attach_pic"]))
	if len(images) > 0 {
		targets := make([]string, 0, len(images))
		for _, image := range images {
			targets = append(targets, b.formatAttachmentImageTarget(image, history, target))
		}
		lines = append(lines, "当前文字回复后还会另外发送图片"+joinChineseItems(targets)+"。")
	}
	if emoji := strings.TrimSpace(stringValue(replyToolArgs["attach_emoji"])); emoji != "" {
		lines = append(lines, fmt.Sprintf("当前文字回复后还会单独发送已选中的第 %s 号表情包，无需在正文中输出序号。", emoji))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return b.LoadPrompt("reply_attachments", map[string]string{"attachments": strings.Join(lines, "\n")})
}

func attachmentImagesFromArgs(raw any) []AttachmentImage {
	switch value := raw.(type) {
	case nil:
		return nil
	case []AttachmentImage:
		return value
	case AttachmentImage:
		return []AttachmentImage{value}
	case []any:
		out := make([]AttachmentImage, 0, len(value))
		for _, item := range value {
			if image, ok := attachmentImageFromAny(item); ok {
				out = append(out, image)
			}
		}
		return out
	default:
		if image, ok := attachmentImageFromAny(value); ok {
			return []AttachmentImage{image}
		}
		return nil
	}
}

func attachmentImageFromAny(raw any) (AttachmentImage, bool) {
	switch value := raw.(type) {
	case AttachmentImage:
		return value, true
	case map[string]any:
		image := AttachmentImage{
			MediaIndex: strings.TrimSpace(stringValue(value["media_index"])),
			MessageID:  strings.TrimSpace(firstNonEmpty(stringValue(value["msg_id"]), stringValue(value["message_id"]), stringValue(value["source"]))),
			Index:      intValue(firstNonNil(value["index"], value["image_index"])),
		}
		return image, true
	case string:
		return AttachmentImage{MessageID: strings.TrimSpace(value)}, true
	default:
		return AttachmentImage{}, false
	}
}

func (b *PromptBuilder) chatPrompt() string {
	return strings.TrimSpace(b.Config.ChatPrompt)
}

// BuildGroupChatAttentionBlock 对齐 _build_group_chat_attention_block
func (b *PromptBuilder) BuildGroupChatAttentionBlock() string {
	lines := make([]string, 0, 2)
	if b.Config.IsGroupChat != nil {
		if *b.Config.IsGroupChat {
			if prompt := strings.TrimSpace(b.Config.GroupChatPrompt); prompt != "" {
				lines = append(lines, "通用注意事项：\n"+prompt)
			}
		} else if prompt := strings.TrimSpace(b.Config.PrivateChatPrompt); prompt != "" {
			lines = append(lines, "通用注意事项：\n"+prompt)
		}
	}
	if prompt := b.chatPrompt(); prompt != "" {
		lines = append(lines, "当前聊天额外注意事项：\n"+prompt)
	}
	if len(lines) == 0 {
		return ""
	}
	return "在该聊天中的注意事项：\n" + strings.Join(lines, "\n\n") + "\n"
}

// BuildReplyerOutputInstruction 对齐 _build_replyer_output_instruction
func (b *PromptBuilder) BuildReplyerOutputInstruction() string {
	locale := strings.ToLower(strings.TrimSpace(b.Config.PromptLocale))
	if locale == "" {
		locale = "zh-cn"
	}
	switch {
	case strings.HasPrefix(locale, "en"):
		return "Please do not output any extra content (including unnecessary prefixes or suffixes, colons, brackets, stickers, plain at, or @). Only output the message content itself."
	case strings.HasPrefix(locale, "ja"):
		return "余計な内容（不要な前置きや後置き、コロン、括弧、スタンプ、通常の at や @ など）は出力せず、発言内容だけを出力してください。"
	case strings.HasPrefix(locale, "ru"):
		return "Не добавляйте лишнего (вступлений, окончаний, двоеточий, скобок, стикеров, at или @); выводите только текст сообщения."
	case strings.HasPrefix(locale, "fr"):
		return "N’ajoutez rien de superflu (préambules, conclusions, deux-points, parenthèses, autocollants, at ou @) ; produisez uniquement le message."
	default:
		return "请注意不要输出多余内容(包括不必要的前后缀，冒号，括号，表情包，@等 )，只输出发言内容就好。"
	}
}

var reactionGroupPattern = regexp.MustCompile("\\[([A-Za-z_][A-Za-z0-9_]*)\\]")

func replaceRegexCaptureGroups(reaction string, match []string, names []string) string {
	if len(match) == 0 {
		return reaction
	}
	return reactionGroupPattern.ReplaceAllStringFunc(reaction, func(group string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(group, "["), "]")
		for index, candidate := range names {
			if candidate == name && index < len(match) {
				return match[index]
			}
		}
		// Python 只替换实际存在的命名捕获组，未知占位符原样保留
		return group
	})
}

func buildTextFromParts(parts []MessagePart) string {
	rendered := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case PartText:
			rendered = append(rendered, part.Text)
		case PartAt:
			rendered = append(rendered, renderTargetAt(part))
		case PartImage, PartEmoji, PartVoice:
			if part.Description != "" {
				rendered = append(rendered, part.Description)
			}
		}
	}
	normalized := make([]string, 0, len(rendered))
	for _, part := range rendered {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	return strings.Join(normalized, " ")
}
