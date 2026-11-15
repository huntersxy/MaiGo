package debounce

import (
	"strings"

	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// ShapeFromComponents 对齐 get_message_shape：按组件 format_name 判 kind，长度只计 TextComponent
func ShapeFromComponents(components []messagecomponent.Component) (kind int, length int) {
	names := make(map[string]struct{}, len(components))
	var textBuilder strings.Builder
	for _, component := range components {
		if component == nil {
			continue
		}
		names[component.FormatName()] = struct{}{}
		if text, ok := component.(messagecomponent.TextComponent); ok {
			textBuilder.WriteString(text.Text)
		} else if text, ok := component.(*messagecomponent.TextComponent); ok && text != nil {
			textBuilder.WriteString(text.Text)
		}
	}
	length = len([]rune(strings.TrimSpace(textBuilder.String())))
	switch {
	case hasName(names, "image"):
		kind = 1
	case hasName(names, "emoji"):
		kind = 2
	case hasAnyName(names, "voice", "forward", "forward_node", "file", "video"):
		kind = 3
	default:
		kind = 0
	}
	return kind, length
}

func hasName(names map[string]struct{}, name string) bool {
	_, ok := names[name]
	return ok
}

func hasAnyName(names map[string]struct{}, items ...string) bool {
	for _, item := range items {
		if hasName(names, item) {
			return true
		}
	}
	return false
}

// ShapeFromMessage 从 OneBot 消息推断裸图/纯表情（不把识图描述当文字）
func ShapeFromMessage(msg *onebot.ConversationMessage) (isBareImage, isPureEmoji bool) {
	if msg == nil {
		return false, false
	}
	text := strings.TrimSpace(msg.Content)
	textLen := len(text)
	hasImage := len(msg.Images) > 0
	hasEmoji := len(msg.Faces) > 0
	for _, part := range msg.MessageParts {
		switch part.Kind {
		case "image":
			hasImage = true
		case "face":
			hasEmoji = true
		}
	}
	if hasImage && textLen == 0 {
		return true, false
	}
	if hasEmoji && !hasImage && textLen == 0 {
		return false, true
	}
	return false, false
}
