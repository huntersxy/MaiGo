package messagecomponent

import (
	"fmt"
)

// MessageSequence 消息组件序列，顺序即消息顺序，不去重不校验
// 对应 Python 的 MessageSequence
type MessageSequence struct {
	Components []Component
}

// NewMessageSequence 构造空序列
func NewMessageSequence() *MessageSequence {
	return &MessageSequence{Components: []Component{}}
}

// Text 追加文本组件
func (s *MessageSequence) Text(text string) *MessageSequence {
	s.Components = append(s.Components, TextComponent{Text: text})
	return s
}

// Image 追加图片组件
func (s *MessageSequence) Image(binary []byte, content string) *MessageSequence {
	s.Components = append(s.Components, NewImageComponent("", content, binary))
	return s
}

// Emoji 追加表情组件
func (s *MessageSequence) Emoji(binary []byte, content string) *MessageSequence {
	s.Components = append(s.Components, NewEmojiComponent("", content, binary))
	return s
}

// Voice 追加语音组件
func (s *MessageSequence) Voice(binary []byte, content string) *MessageSequence {
	s.Components = append(s.Components, NewVoiceComponent("", content, binary))
	return s
}

// File 追加文件组件
func (s *MessageSequence) File(file FileComponent) *MessageSequence {
	s.Components = append(s.Components, file)
	return s
}

// At 追加 @ 组件
func (s *MessageSequence) At(userID string) *MessageSequence {
	s.Components = append(s.Components, AtComponent{TargetUserID: userID})
	return s
}

// Reply 追加回复组件
func (s *MessageSequence) Reply(messageID string) *MessageSequence {
	s.Components = append(s.Components, ReplyComponent{TargetMessageID: messageID})
	return s
}

// ToDict 对应 Python 的 MessageSequence.to_dict
func (s *MessageSequence) ToDict() []any {
	items := make([]any, 0, len(s.Components))
	for _, comp := range s.Components {
		items = append(items, comp.toDict())
	}
	return items
}

// FromDict 对应 Python 的 MessageSequence.from_dict
func FromDict(items []any) (*MessageSequence, error) {
	components := make([]Component, 0, len(items))
	for index, item := range items {
		comp, err := dictToComponent(item)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个消息组件解析失败: %w", index, err)
		}
		components = append(components, comp)
	}
	return &MessageSequence{Components: components}, nil
}

// dictToComponent 对应 Python 的 MessageSequence._dict_2_item
func dictToComponent(item any) (Component, error) {
	mapping, ok := item.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("消息组件必须是字典，实际为 %T", item)
	}
	itemType, _ := mapping["type"].(string)
	switch itemType {
	case "text":
		text, ok := mapping["data"].(string)
		if !ok {
			return nil, fmt.Errorf("text 组件的 data 必须是字符串")
		}
		return TextComponent{Text: text}, nil
	case "image", "emoji", "voice":
		hash, ok := mapping["hash"].(string)
		if !ok {
			return nil, fmt.Errorf("%s 组件缺少 hash", itemType)
		}
		data, _ := mapping["data"].(string)
		switch itemType {
		case "image":
			return ImageComponent{ByteComponent: ByteComponent{Hash: hash, Content: data}}, nil
		case "emoji":
			return EmojiComponent{ByteComponent: ByteComponent{Hash: hash, Content: data}}, nil
		default:
			return VoiceComponent{ByteComponent: ByteComponent{Hash: hash, Content: data}}, nil
		}
	case "file":
		payload, _ := mapping["data"].(map[string]any)
		if payload == nil {
			// Python：data 不是字典时按文件名兜底
			text, _ := mapping["data"].(string)
			return FileComponent{Name: text}, nil
		}
		return FileComponentFromPayload(payload), nil
	case "at":
		data, ok := mapping["data"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("at 组件的 data 必须是字典")
		}
		targetID, _ := data["target_user_id"].(string)
		return AtComponent{
			TargetUserID: targetID,
			Nickname:     optionalString(data["target_user_nickname"]),
			Cardname:     optionalString(data["target_user_cardname"]),
			UsesBotNick:  boolValue(data["uses_configured_bot_nickname"]),
		}, nil
	case "reply":
		targetID, _ := mapping["data"].(string)
		return ReplyComponent{
			TargetMessageID: targetID,
			TargetContent:   optionalString(mapping["target_message_content"]),
			SenderID:        optionalString(mapping["target_message_sender_id"]),
			SenderNickname:  optionalString(mapping["target_message_sender_nickname"]),
			SenderCardname:  optionalString(mapping["target_message_sender_cardname"]),
		}, nil
	case "dict":
		data, _ := mapping["data"].(map[string]any)
		if data == nil {
			return DictComponent{Type: "dict", Data: map[string]any{}}, nil
		}
		// Python：dict 里再套 type=file 时还原为文件组件
		if innerType, _ := data["type"].(string); innerType == "file" {
			payload := data
			if nested, ok := data["data"].(map[string]any); ok {
				payload = nested
			}
			return FileComponentFromPayload(payload), nil
		}
		return DictComponent{Type: "dict", Data: data}, nil
	case "forward":
		nodes, ok := mapping["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("forward 组件的 data 必须是列表")
		}
		forwards := make([]ForwardComponent, 0, len(nodes))
		for index, node := range nodes {
			nodeMap, ok := node.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("第 %d 个转发节点必须是字典", index)
			}
			rawContent, _ := nodeMap["content"].([]any)
			content := make([]Component, 0, len(rawContent))
			for _, inner := range rawContent {
				comp, err := dictToComponent(inner)
				if err != nil {
					return nil, err
				}
				content = append(content, comp)
			}
			nickname, _ := nodeMap["user_nickname"].(string)
			forwards = append(forwards, ForwardComponent{
				UserNickname: nickname,
				MessageID:    stringValue(nodeMap["message_id"]),
				Content:      content,
				UserID:       optionalString(nodeMap["user_id"]),
				UserCardname: optionalString(nodeMap["user_cardname"]),
			})
		}
		return ForwardNodeComponent{ForwardComponents: forwards}, nil
	default:
		// Python 对未知类型告警后按 DictComponent 承载，保留原始 type（如 mention_bot）
		data, _ := mapping["data"].(map[string]any)
		if data == nil {
			data = map[string]any{}
		}
		return DictComponent{Type: itemType, Data: data}, nil
	}
}

func optionalString(value any) *string {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
