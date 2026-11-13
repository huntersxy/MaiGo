package internalchat

import (
	"bytes"
	"encoding/base64"
	"strings"

	"maibot-go/internal/common/data_models/messagecomponent"
)

// BinaryLoader 按段类型与 hash 读取图片/表情缓存字节
// mime 为空表示没有可用数据，序列化时回退段类型默认 MIME
type BinaryLoader func(segmentType string, hash string) (data []byte, mimeType string)

// SerializeSequence 把消息组件序列转成 WebUI 富文本段
// 对齐 src/webui/routers/chat/serializers.py；没有二进制时退化展示组件正文
func SerializeSequence(seq *messagecomponent.MessageSequence) []map[string]any {
	return SerializeSequenceWith(seq, nil)
}

// SerializeSequenceWith 在 SerializeSequence 基础上注入图片缓存读取器
// 落库只保留 hash，DB 还原出的图片/表情必须靠 loader 回读二进制
func SerializeSequenceWith(seq *messagecomponent.MessageSequence, loader BinaryLoader) []map[string]any {
	if seq == nil {
		return []map[string]any{}
	}
	segments := make([]map[string]any, 0, len(seq.Components))
	for _, component := range seq.Components {
		if segment := serializeComponent(component, loader); segment != nil {
			segments = append(segments, segment)
		}
	}
	return segments
}

func serializeComponent(component messagecomponent.Component, loader BinaryLoader) map[string]any {
	switch item := component.(type) {
	case messagecomponent.TextComponent:
		return map[string]any{"type": "text", "data": item.Text}
	case messagecomponent.ImageComponent:
		return serializeBinaryComponent("image", "image/png", item.Binary, item.Hash, loader, "image", item.Content)
	case messagecomponent.EmojiComponent:
		return serializeBinaryComponent("emoji", "image/gif", item.Binary, item.Hash, loader, "emoji", item.Content)
	case messagecomponent.VoiceComponent:
		// Python 侧 voice 传 binary_hash=""、image_type_name=""，即从不回查图片缓存
		return serializeBinaryComponent("voice", "audio/wav", item.Binary, "", nil, "", item.Content)
	case messagecomponent.FileComponent:
		return map[string]any{"type": "file", "data": item.ToPayload()}
	case messagecomponent.AtComponent:
		// Python 的 Optional[str] 字段在 JSON 里是字符串或 null，这里同样解引用
		return map[string]any{
			"type": "at",
			"data": map[string]any{
				"target_user_id":       item.TargetUserID,
				"target_user_nickname": optionalValue(item.Nickname),
				"target_user_cardname": optionalValue(item.Cardname),
			},
		}
	case messagecomponent.ReplyComponent:
		return map[string]any{
			"type": "reply",
			"data": map[string]any{
				"target_message_id":              item.TargetMessageID,
				"target_message_content":         optionalValue(item.TargetContent),
				"target_message_sender_id":       optionalValue(item.SenderID),
				"target_message_sender_nickname": optionalValue(item.SenderNickname),
				"target_message_sender_cardname": optionalValue(item.SenderCardname),
			},
		}
	case messagecomponent.ForwardNodeComponent:
		nodes := make([]map[string]any, 0, len(item.ForwardComponents))
		for _, node := range item.ForwardComponents {
			nodeContent := make([]map[string]any, 0, len(node.Content))
			for _, inner := range node.Content {
				if segment := serializeComponent(inner, loader); segment != nil {
					nodeContent = append(nodeContent, segment)
				}
			}
			nodes = append(nodes, map[string]any{
				"message_id":    node.MessageID,
				"user_id":       node.UserID,
				"user_nickname": node.UserNickname,
				"user_cardname": node.UserCardname,
				"content":       nodeContent,
			})
		}
		return map[string]any{"type": "forward", "data": nodes}
	case messagecomponent.DictComponent:
		return serializeDictComponent(item.Data)
	default:
		return map[string]any{"type": "unknown", "data": component.FormatName()}
	}
}

// serializeBinaryComponent 对齐 serializers._serialize_binary_component
func serializeBinaryComponent(segmentType, defaultMIME string, binary []byte, hash string, loader BinaryLoader, lookupType, fallback string) map[string]any {
	data, mimeType := loadCachedBinaryData(binary, hash, loader, lookupType, defaultMIME)
	if len(data) > 0 {
		return map[string]any{
			"type": segmentType,
			"data": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
		}
	}
	if fallback != "" {
		return map[string]any{"type": "text", "data": fallback}
	}
	return map[string]any{"type": "unknown", "original_type": segmentType, "data": ""}
}

// loadCachedBinaryData 对齐 serializers._load_cached_binary_data：
// 组件自带二进制优先，否则按 hash 回查图片缓存；任何缺失都退化为无字节
func loadCachedBinaryData(binary []byte, hash string, loader BinaryLoader, lookupType, defaultMIME string) ([]byte, string) {
	if len(binary) > 0 {
		return binary, detectBinaryMIME(binary, defaultMIME)
	}
	normalizedHash := strings.TrimSpace(hash)
	if normalizedHash == "" || lookupType == "" || loader == nil {
		return nil, defaultMIME
	}
	data, mimeType := loader(lookupType, normalizedHash)
	if len(data) == 0 {
		return nil, defaultMIME
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = defaultMIME
	}
	return data, mimeType
}

// detectBinaryMIME 对齐 serializers._detect_binary_mime_type
func detectBinaryMIME(binary []byte, defaultMIME string) string {
	if bytes.HasPrefix(binary, []byte{0xff, 0xd8, 0xff}) {
		return "image/jpeg"
	}
	if bytes.HasPrefix(binary, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		return "image/png"
	}
	if bytes.HasPrefix(binary, []byte("GIF87a")) || bytes.HasPrefix(binary, []byte("GIF89a")) {
		return "image/gif"
	}
	if len(binary) >= 12 && string(binary[:4]) == "RIFF" && string(binary[8:12]) == "WEBP" {
		return "image/webp"
	}
	return defaultMIME
}

// optionalValue 把可选字符串解引用成 Python 的 str|None 形状
func optionalValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// serializeDictComponent 对齐 serializers._serialize_dict_component
func serializeDictComponent(data map[string]any) map[string]any {
	rawType, _ := data["type"].(string)
	rawType = strings.TrimSpace(rawType)
	if rawType == "" {
		rawType = "dict"
	}
	payload, ok := data["data"]
	if !ok {
		payload = data
	}
	switch rawType {
	case "text", "image", "emoji", "voice", "video", "file", "music", "face":
		return map[string]any{"type": rawType, "data": payload}
	case "reply":
		return map[string]any{"type": "reply", "data": payload}
	case "forward":
		if _, ok := payload.([]map[string]any); ok {
			return map[string]any{"type": "forward", "data": payload}
		}
		if _, ok := payload.([]any); ok {
			return map[string]any{"type": "forward", "data": payload}
		}
	}
	return map[string]any{"type": "unknown", "original_type": rawType, "data": payload}
}
