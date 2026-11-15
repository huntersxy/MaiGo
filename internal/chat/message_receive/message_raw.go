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
func BuildRawContent(msg *onebot.ConversationMessage, mediaRefs []media.Ref, forwards []ForwardDraft) []byte {
	if msg == nil {
		return nil
	}
	seq := messagecomponent.NewMessageSequence()
	refIndex := 0
	topForwards := topLevelForwards(forwards)
	forwardIndex := 0
	for _, part := range msg.MessageParts {
		switch part.Kind {
		case "text":
			seq.Text(part.Text)
		case "at":
			seq.Components = append(seq.Components, messagecomponent.AtComponent{
				TargetUserID: strconv.FormatInt(part.AtUserID, 10),
				Nickname:     stringOrNil(msg.AtNames[part.AtUserID]),
			})
		case "reply":
			if msg.Reply != nil && msg.Reply.MessageID != 0 {
				seq.Reply(strconv.FormatInt(msg.Reply.MessageID, 10))
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
			if forwardIndex < len(topForwards) {
				seq.Components = append(seq.Components, forwardNodeComponent(topForwards[forwardIndex]))
				forwardIndex++
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

// topLevelForwards 取出顶层转发草稿，父级为空表示消息本体上的合并转发
func topLevelForwards(forwards []ForwardDraft) []ForwardDraft {
	out := make([]ForwardDraft, 0, len(forwards))
	for _, draft := range forwards {
		if draft.Parent == "" {
			out = append(out, draft)
		}
	}
	return out
}

// forwardNodeComponent 把转发草稿还原成 MaiBot forward 组件；
// 优先使用解析阶段保留的完整组件树，旧草稿回退为纯文本以保持兼容
func forwardNodeComponent(draft ForwardDraft) messagecomponent.ForwardNodeComponent {
	if len(draft.RawComponents) > 0 {
		return messagecomponent.ForwardNodeComponent{ForwardComponents: draft.RawComponents}
	}
	component := messagecomponent.ForwardNodeComponent{}
	for _, node := range draft.Nodes {
		content := make([]messagecomponent.Component, 0, 1)
		if strings.TrimSpace(node.Content) != "" {
			content = append(content, messagecomponent.TextComponent{Text: node.Content})
		}
		component.ForwardComponents = append(component.ForwardComponents, messagecomponent.ForwardComponent{
			UserNickname: node.Sender,
			Content:      content,
		})
	}
	return component
}

func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
