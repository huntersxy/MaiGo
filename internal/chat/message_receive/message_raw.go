package messagereceive

import (
	"strconv"
	"strings"

	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/media"
	"MaiGo/internal/platform_io/drivers/onebot"
)

// BuildRawContent 生成 MaiBot mai_messages.raw_content 的等价 msgpack：
// 组件顺序与平台消息一致，图片/表情只保留内容摘要和 sha256，不重复存二进制
func BuildRawContent(msg *onebot.ConversationMessage, mediaRefs []media.Ref) []byte {
	if msg == nil {
		return nil
	}
	seq := messagecomponent.NewMessageSequence()
	refIndex := 0
	for _, part := range msg.MessageParts {
		switch part.Kind {
		case "text":
			seq.Text(part.Text)
		case "at":
			target := part.ExternalAtUserID
			if target == "" {
				target = strconv.FormatInt(part.AtUserID, 10)
			}
			nickname := part.AtNickname
			if nickname == "" && part.AtCardname == "" {
				nickname = msg.AtNames[part.AtUserID]
			}
			seq.Components = append(seq.Components, messagecomponent.AtComponent{
				TargetUserID: target,
				Nickname:     stringOrNil(nickname),
				Cardname:     stringOrNil(part.AtCardname),
			})
		case "reply":
			if reply := msg.Reply; reply != nil {
				messageID := reply.ExternalMessageID
				if messageID == "" && reply.MessageID != 0 {
					messageID = strconv.FormatInt(reply.MessageID, 10)
				}
				sender := reply.ExternalSenderID
				if sender == "" && reply.SenderID != 0 {
					sender = strconv.FormatInt(reply.SenderID, 10)
				}
				if messageID != "" {
					seq.Components = append(seq.Components, messagecomponent.ReplyComponent{
						TargetMessageID: messageID, TargetContent: stringOrNil(reply.Content), SenderID: stringOrNil(sender),
						SenderNickname: stringOrNil(reply.Nickname), SenderCardname: stringOrNil(reply.GroupCard),
					})
				}
			}
		case "image":
			hash := ""
			if refIndex < len(mediaRefs) {
				hash = mediaRefs[refIndex].Hash
				refIndex++
			}
			desc := ""
			sticker := false
			if part.Index >= 0 && part.Index < len(msg.Images) {
				desc = msg.Images[part.Index].Desc
				sticker = msg.Images[part.Index].SubType == 1
			}
			byteComp := messagecomponent.ByteComponent{Hash: hash, Content: desc}
			if sticker {
				seq.Components = append(seq.Components, messagecomponent.EmojiComponent{ByteComponent: byteComp})
			} else {
				seq.Components = append(seq.Components, messagecomponent.ImageComponent{ByteComponent: byteComp})
			}
		case "record":
			voice := messagecomponent.VoiceComponent{}
			if part.Index >= 0 && part.Index < len(msg.Voices) {
				voice.Source = msg.Voices[part.Index].VoiceSource()
			}
			if strings.TrimSpace(voice.Source) == "" {
				voice.Source = strings.TrimSpace(part.Text)
			}
			seq.Components = append(seq.Components, voice)
		case "file":
			component := messagecomponent.FileComponent{}
			if part.Index >= 0 && part.Index < len(msg.Files) {
				file := msg.Files[part.Index]
				component.Name = file.Name
				component.MimeType = file.MimeType
				component.FileRef = file.FileRef
				if file.Size > 0 {
					component.Size = strconv.FormatInt(file.Size, 10)
				}
			} else if part.Index >= 0 && part.Index < len(msg.FileNames) {
				component.Name = msg.FileNames[part.Index]
			}
			seq.File(component)
		case "forward":
			if part.Forward != nil {
				seq.Components = append(seq.Components, *part.Forward)
				continue
			}
			seq.Components = append(seq.Components, messagecomponent.DictComponent{
				Data: map[string]any{"type": part.Kind},
			})
		case "dict":
			var payload any = map[string]any{"type": "dict"}
			if part.Index >= 0 && part.Index < len(msg.Dicts) && msg.Dicts[part.Index] != nil {
				payload = msg.Dicts[part.Index]
			}
			if mapping, ok := payload.(map[string]any); ok {
				seq.Components = append(seq.Components, messagecomponent.DictComponent{Data: mapping})
				continue
			}
			seq.Components = append(seq.Components, messagecomponent.DictComponent{
				Data: map[string]any{"type": "dict", "data": payload},
			})
		case "face", "video", "card":
			// 平台段在 MaiBot 组件模型里没有对应类型，用 dict 段保留类型标记
			seq.Components = append(seq.Components, messagecomponent.DictComponent{
				Data: map[string]any{"type": part.Kind},
			})
		}
	}
	raw, err := messagecomponent.EncodeDBRecord(seq)
	if err != nil {
		return nil
	}
	return raw
}

func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
