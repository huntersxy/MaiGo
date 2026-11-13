package messagecomponent

import (
	"sort"

	"github.com/vmihailenco/msgpack/v5"
)

// Python 的 dict 保持插入顺序，msgpack 直接把该顺序写进字节流。
// Go 的 map 遍历是随机的，直接 Marshal 会得到非确定字节，
// 既不利于对拍，也让同样的消息在库里出现不同编码。
// 这里按 Python _item_2_dict / to_payload 的键顺序逐字段写出，
// 保证 Go 侧写出的 raw_content 与 Python 侧逐字节一致

// encodeSequence 按 Python 顺序写出组件数组
func encodeSequence(enc *msgpack.Encoder, seq *MessageSequence) error {
	if err := enc.EncodeArrayLen(len(seq.Components)); err != nil {
		return err
	}
	for _, comp := range seq.Components {
		if err := encodeComponent(enc, comp); err != nil {
			return err
		}
	}
	return nil
}

func encodeComponent(enc *msgpack.Encoder, comp Component) error {
	switch typed := comp.(type) {
	case TextComponent:
		return encodeText(enc, typed)
	case ImageComponent:
		return encodeBytesComponent(enc, "image", typed.Content, typed.Hash)
	case EmojiComponent:
		return encodeBytesComponent(enc, "emoji", ensureBinaryContent(typed.Content, "[表情包]"), typed.Hash)
	case VoiceComponent:
		return encodeBytesComponent(enc, "voice", ensureBinaryContent(typed.Content, "[语音消息]"), typed.Hash)
	case FileComponent:
		return encodeFile(enc, typed)
	case AtComponent:
		return encodeAt(enc, typed)
	case ReplyComponent:
		return encodeReply(enc, typed)
	case ForwardNodeComponent:
		return encodeForward(enc, typed)
	case *TextComponent:
		return encodeText(enc, *typed)
	case *ImageComponent:
		return encodeBytesComponent(enc, "image", typed.Content, typed.Hash)
	case *EmojiComponent:
		return encodeBytesComponent(enc, "emoji", ensureBinaryContent(typed.Content, "[表情包]"), typed.Hash)
	case *VoiceComponent:
		return encodeBytesComponent(enc, "voice", ensureBinaryContent(typed.Content, "[语音消息]"), typed.Hash)
	case *FileComponent:
		return encodeFile(enc, *typed)
	case *AtComponent:
		return encodeAt(enc, *typed)
	case *ReplyComponent:
		return encodeReply(enc, *typed)
	case *ForwardNodeComponent:
		return encodeForward(enc, *typed)
	case DictComponent:
		return encodeDict(enc, typed.Data)
	case *DictComponent:
		return encodeDict(enc, typed.Data)
	default:
		// 未知组件按 Python 兜底写成 dict 组件
		return encodeDict(enc, map[string]any{})
	}
}

func encodeText(enc *msgpack.Encoder, comp TextComponent) error {
	if err := enc.EncodeMapLen(2); err != nil {
		return err
	}
	if err := enc.EncodeString("type"); err != nil {
		return err
	}
	if err := enc.EncodeString("text"); err != nil {
		return err
	}
	if err := enc.EncodeString("data"); err != nil {
		return err
	}
	return enc.EncodeString(comp.Text)
}

// encodeBytesComponent 覆盖 image/emoji/voice：type、data、hash 三键
func encodeBytesComponent(enc *msgpack.Encoder, kind string, content string, hash string) error {
	if err := enc.EncodeMapLen(3); err != nil {
		return err
	}
	if err := enc.EncodeString("type"); err != nil {
		return err
	}
	if err := enc.EncodeString(kind); err != nil {
		return err
	}
	if err := enc.EncodeString("data"); err != nil {
		return err
	}
	if err := enc.EncodeString(content); err != nil {
		return err
	}
	if err := enc.EncodeString("hash"); err != nil {
		return err
	}
	return enc.EncodeString(hash)
}

func encodeFile(enc *msgpack.Encoder, comp FileComponent) error {
	if err := enc.EncodeMapLen(2); err != nil {
		return err
	}
	if err := enc.EncodeString("type"); err != nil {
		return err
	}
	if err := enc.EncodeString("file"); err != nil {
		return err
	}
	if err := enc.EncodeString("data"); err != nil {
		return err
	}
	// 与 FileComponent.to_payload 的写入顺序一致
	fields := make([]struct {
		key   string
		value any
	}, 0, 8)
	if comp.FileRef != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"file_ref", comp.FileRef})
	}
	if comp.Busid != 0 {
		fields = append(fields, struct {
			key   string
			value any
		}{"busid", comp.Busid})
	}
	if comp.Name != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"name", comp.Name})
	}
	if comp.Size != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"size", comp.Size})
	}
	if comp.URL != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"url", comp.URL})
	}
	if comp.FileID != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"file_id", comp.FileID})
	}
	if comp.MimeType != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"mime_type", comp.MimeType})
	}
	if comp.Base64Data != "" {
		fields = append(fields, struct {
			key   string
			value any
		}{"base64", comp.Base64Data})
	}
	if err := enc.EncodeMapLen(len(fields)); err != nil {
		return err
	}
	for _, field := range fields {
		if err := enc.EncodeString(field.key); err != nil {
			return err
		}
		if err := encodeAny(enc, field.value); err != nil {
			return err
		}
	}
	return nil
}

func encodeAt(enc *msgpack.Encoder, comp AtComponent) error {
	if err := enc.EncodeMapLen(2); err != nil {
		return err
	}
	if err := enc.EncodeString("type"); err != nil {
		return err
	}
	if err := enc.EncodeString("at"); err != nil {
		return err
	}
	if err := enc.EncodeString("data"); err != nil {
		return err
	}
	if err := enc.EncodeMapLen(4); err != nil {
		return err
	}
	if err := enc.EncodeString("target_user_id"); err != nil {
		return err
	}
	if err := enc.EncodeString(comp.TargetUserID); err != nil {
		return err
	}
	if err := enc.EncodeString("target_user_nickname"); err != nil {
		return err
	}
	if err := encodeOptionalString(enc, comp.Nickname); err != nil {
		return err
	}
	if err := enc.EncodeString("target_user_cardname"); err != nil {
		return err
	}
	if err := encodeOptionalString(enc, comp.Cardname); err != nil {
		return err
	}
	if err := enc.EncodeString("uses_configured_bot_nickname"); err != nil {
		return err
	}
	return enc.EncodeBool(comp.UsesBotNick)
}

func encodeReply(enc *msgpack.Encoder, comp ReplyComponent) error {
	if err := enc.EncodeMapLen(6); err != nil {
		return err
	}
	steps := []struct {
		key    string
		encode func() error
	}{
		{"type", func() error { return enc.EncodeString("reply") }},
		{"data", func() error { return enc.EncodeString(comp.TargetMessageID) }},
		{"target_message_content", func() error { return encodeOptionalString(enc, comp.TargetContent) }},
		{"target_message_sender_id", func() error { return encodeOptionalString(enc, comp.SenderID) }},
		{"target_message_sender_nickname", func() error { return encodeOptionalString(enc, comp.SenderNickname) }},
		{"target_message_sender_cardname", func() error { return encodeOptionalString(enc, comp.SenderCardname) }},
	}
	for _, step := range steps {
		if err := enc.EncodeString(step.key); err != nil {
			return err
		}
		if err := step.encode(); err != nil {
			return err
		}
	}
	return nil
}

func encodeForward(enc *msgpack.Encoder, comp ForwardNodeComponent) error {
	if err := enc.EncodeMapLen(2); err != nil {
		return err
	}
	if err := enc.EncodeString("type"); err != nil {
		return err
	}
	if err := enc.EncodeString("forward"); err != nil {
		return err
	}
	if err := enc.EncodeString("data"); err != nil {
		return err
	}
	if err := enc.EncodeArrayLen(len(comp.ForwardComponents)); err != nil {
		return err
	}
	for _, node := range comp.ForwardComponents {
		if err := enc.EncodeMapLen(5); err != nil {
			return err
		}
		if err := enc.EncodeString("user_id"); err != nil {
			return err
		}
		if err := encodeOptionalString(enc, node.UserID); err != nil {
			return err
		}
		if err := enc.EncodeString("user_nickname"); err != nil {
			return err
		}
		if err := enc.EncodeString(node.UserNickname); err != nil {
			return err
		}
		if err := enc.EncodeString("user_cardname"); err != nil {
			return err
		}
		if err := encodeOptionalString(enc, node.UserCardname); err != nil {
			return err
		}
		if err := enc.EncodeString("message_id"); err != nil {
			return err
		}
		if err := enc.EncodeString(node.MessageID); err != nil {
			return err
		}
		if err := enc.EncodeString("content"); err != nil {
			return err
		}
		if err := enc.EncodeArrayLen(len(node.Content)); err != nil {
			return err
		}
		for _, inner := range node.Content {
			if err := encodeComponent(enc, inner); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeDict(enc *msgpack.Encoder, data map[string]any) error {
	if err := enc.EncodeMapLen(2); err != nil {
		return err
	}
	if err := enc.EncodeString("type"); err != nil {
		return err
	}
	if err := enc.EncodeString("dict"); err != nil {
		return err
	}
	if err := enc.EncodeString("data"); err != nil {
		return err
	}
	return encodeAny(enc, data)
}

func encodeOptionalString(enc *msgpack.Encoder, value *string) error {
	if value == nil {
		return enc.EncodeNil()
	}
	return enc.EncodeString(*value)
}

// encodeAny 只为 DictComponent 的任意负载服务：键排序保证确定性
func encodeAny(enc *msgpack.Encoder, value any) error {
	switch typed := value.(type) {
	case nil:
		return enc.EncodeNil()
	case string:
		return enc.EncodeString(typed)
	case bool:
		return enc.EncodeBool(typed)
	case int:
		return enc.EncodeInt(int64(typed))
	case int64:
		return enc.EncodeInt(typed)
	case float64:
		return enc.EncodeFloat64(typed)
	case []any:
		if err := enc.EncodeArrayLen(len(typed)); err != nil {
			return err
		}
		for _, item := range typed {
			if err := encodeAny(enc, item); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if err := enc.EncodeMapLen(len(keys)); err != nil {
			return err
		}
		for _, key := range keys {
			if err := enc.EncodeString(key); err != nil {
				return err
			}
			if err := encodeAny(enc, typed[key]); err != nil {
				return err
			}
		}
		return nil
	default:
		return enc.Encode(typed)
	}
}
