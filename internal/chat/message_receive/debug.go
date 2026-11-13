package messagereceive

import (
	"fmt"
	"strings"

	"maibot-go/internal/common/data_models/messagecomponent"
)

// String 对齐 SessionMessage.__str__
func (m *SessionMessage) String() string { return m.DebugString() }

// DebugString 对齐 SessionMessage.to_debug_string
func (m *SessionMessage) DebugString() string {
	if m == nil {
		return "SessionMessage(<nil>)"
	}
	chatType := "private"
	var groupID, groupName *string
	if m.MessageInfo.GroupInfo != nil {
		chatType = "group"
		groupID = &m.MessageInfo.GroupInfo.GroupID
		groupName = &m.MessageInfo.GroupInfo.GroupName
	}
	components := make([]string, 0)
	if m.RawMessage != nil {
		for _, component := range m.RawMessage.Components {
			components = append(components, summarizeComponentDebug(component))
		}
	}
	rawComponents := "empty"
	if len(components) > 0 {
		rawComponents = strings.Join(components, ", ")
	}
	nickname := m.Nickname()
	return fmt.Sprintf(
		"SessionMessage(message_id=%s, platform=%s, chat_type=%s, group_id=%s, group_name=%s, "+
			"user_id=%s, user_nickname=%s, user_cardname=%s, reply_to=%s, processed_plain_text=%s, raw_components=[%s])",
		pyReprString(m.MessageID),
		pyReprString(m.Platform),
		pyReprString(chatType),
		pyReprPtr(groupID),
		pyReprPtr(groupName),
		pyReprString(m.UserID()),
		pyReprString(nickname),
		pyReprPtr(m.MessageInfo.UserInfo.UserCardname),
		pyReprPtr(m.ReplyTo),
		truncateDebugText(m.PlainText(), 120),
		rawComponents,
	)
}

// summarizeComponentDebug 对齐 _summarize_component 的摘要格式
func summarizeComponentDebug(component messagecomponent.Component) string {
	if component == nil {
		return "None"
	}
	switch item := component.(type) {
	case messagecomponent.TextComponent:
		return "Text(text=" + truncateDebugText(item.Text, 80) + ")"
	case *messagecomponent.TextComponent:
		if item == nil {
			return "None"
		}
		return "Text(text=" + truncateDebugText(item.Text, 80) + ")"
	case messagecomponent.ImageComponent:
		return "Image(content=" + truncateDebugText(item.Content, 60) + ")"
	case *messagecomponent.ImageComponent:
		if item == nil {
			return "None"
		}
		return "Image(content=" + truncateDebugText(item.Content, 60) + ")"
	case messagecomponent.EmojiComponent:
		return "Emoji(content=" + truncateDebugText(item.Content, 60) + ")"
	case *messagecomponent.EmojiComponent:
		if item == nil {
			return "None"
		}
		return "Emoji(content=" + truncateDebugText(item.Content, 60) + ")"
	case messagecomponent.VoiceComponent:
		return "Voice(content=" + truncateDebugText(item.Content, 60) + ")"
	case *messagecomponent.VoiceComponent:
		if item == nil {
			return "None"
		}
		return "Voice(content=" + truncateDebugText(item.Content, 60) + ")"
	case messagecomponent.FileComponent:
		return "File(name=" + pyReprString(item.Name) + ", size=" + pyReprString(item.Size) + ")"
	case *messagecomponent.FileComponent:
		if item == nil {
			return "None"
		}
		return "File(name=" + pyReprString(item.Name) + ", size=" + pyReprString(item.Size) + ")"
	case messagecomponent.AtComponent:
		return "At(target=" + pyReprString(atTargetName(item)) + ")"
	case *messagecomponent.AtComponent:
		if item == nil {
			return "None"
		}
		return "At(target=" + pyReprString(atTargetName(*item)) + ")"
	case messagecomponent.ReplyComponent:
		return replyComponentDebug(item)
	case *messagecomponent.ReplyComponent:
		if item == nil {
			return "None"
		}
		return replyComponentDebug(*item)
	case messagecomponent.ForwardNodeComponent:
		return fmt.Sprintf("ForwardNode(count=%d)", len(item.ForwardComponents))
	case *messagecomponent.ForwardNodeComponent:
		if item == nil {
			return "None"
		}
		return fmt.Sprintf("ForwardNode(count=%d)", len(item.ForwardComponents))
	default:
		return component.FormatName() + "Component"
	}
}

func atTargetName(item messagecomponent.AtComponent) string {
	if item.Cardname != nil && strings.TrimSpace(*item.Cardname) != "" {
		return *item.Cardname
	}
	if item.Nickname != nil && strings.TrimSpace(*item.Nickname) != "" {
		return *item.Nickname
	}
	return item.TargetUserID
}

func replyComponentDebug(item messagecomponent.ReplyComponent) string {
	sender := ""
	switch {
	case item.SenderCardname != nil && strings.TrimSpace(*item.SenderCardname) != "":
		sender = *item.SenderCardname
	case item.SenderNickname != nil && strings.TrimSpace(*item.SenderNickname) != "":
		sender = *item.SenderNickname
	case item.SenderID != nil:
		sender = *item.SenderID
	}
	return "Reply(target_message_id=" + pyReprString(item.TargetMessageID) +
		", target_sender=" + pyReprString(sender) +
		", target_content=" + truncateDebugTextPtr(item.TargetContent, 80) + ")"
}

// truncateDebugText 对齐 _truncate_text：nil→None，换行转义后截断并加省略号，再取 repr
func truncateDebugText(text string, maxLength int) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r", "\\r"), "\n", "\\n")
	runes := []rune(normalized)
	if len(runes) <= maxLength {
		return pyReprString(normalized)
	}
	return pyReprString(string(runes[:maxLength]) + "...")
}

func truncateDebugTextPtr(text *string, maxLength int) string {
	if text == nil {
		return "None"
	}
	return truncateDebugText(*text, maxLength)
}

func pyReprPtr(value *string) string {
	if value == nil {
		return "None"
	}
	return pyReprString(*value)
}

// pyReprString 对齐 Python repr(str)：默认单引号，含单引号且不含双引号时改用双引号
func pyReprString(value string) string {
	quote := byte('\'')
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteByte(quote)
	for _, char := range value {
		switch char {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if char == rune(quote) {
				builder.WriteByte('\\')
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteByte(quote)
	return builder.String()
}
