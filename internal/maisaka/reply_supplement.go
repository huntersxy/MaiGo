package maisaka

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/common/llmjson"
)

// 回复补充编排节点类型，对齐 Python reply_supplement 的 discriminated union
const (
	// SupplementNodeReply 正文占位符，必须且只能出现一次
	SupplementNodeReply = "reply"
	// SupplementNodeAt @ 某个用户
	SupplementNodeAt = "at"
	// SupplementNodePicture 引用上下文中已有图片
	SupplementNodePicture = "pic"
	// SupplementNodeEmoji 引用表情候选编号
	SupplementNodeEmoji = "emoji"
)

// SupplementPart 编排中的一个节点
type SupplementPart struct {
	Type   string
	MsgID  string
	Source string
	Index  int
}

// ReplySupplementPlan 一份发送编排；reply 只能出现一次，不能携带或改写正文
type ReplySupplementPlan struct {
	MsgID string
	Parts []SupplementPart
}

// ParseReplySupplementPlan 严格解析模型输出，多余字段与结构错误直接报错
func ParseReplySupplementPlan(raw string) (ReplySupplementPlan, error) {
	payload := map[string]any{}
	if err := llmjson.Unmarshal(raw, &payload); err != nil {
		return ReplySupplementPlan{}, fmt.Errorf("回复补充编排不是合法 JSON：%w", err)
	}
	for key := range payload {
		if key != "msg_id" && key != "parts" {
			return ReplySupplementPlan{}, fmt.Errorf("回复补充编排包含未知字段：%s", key)
		}
	}
	plan := ReplySupplementPlan{}
	if value, ok := payload["msg_id"]; ok && value != nil {
		msgID, ok := value.(string)
		if !ok || strings.TrimSpace(msgID) == "" {
			return ReplySupplementPlan{}, errors.New("回复补充编排的 msg_id 必须是非空字符串")
		}
		plan.MsgID = msgID
	}
	rawParts, ok := payload["parts"].([]any)
	if !ok {
		return ReplySupplementPlan{}, errors.New("回复补充编排缺少 parts 列表")
	}
	if len(rawParts) < 1 || len(rawParts) > 16 {
		return ReplySupplementPlan{}, errors.New("回复补充编排的 parts 数量必须在 1 到 16 之间")
	}
	replyCount := 0
	for _, rawPart := range rawParts {
		partPayload, ok := rawPart.(map[string]any)
		if !ok {
			return ReplySupplementPlan{}, errors.New("回复补充编排的节点必须是对象")
		}
		part, err := parseSupplementPart(partPayload)
		if err != nil {
			return ReplySupplementPlan{}, err
		}
		if part.Type == SupplementNodeReply {
			replyCount++
		}
		plan.Parts = append(plan.Parts, part)
	}
	if replyCount != 1 {
		return ReplySupplementPlan{}, errors.New("回复补充编排必须且只能包含一个 reply 占位符")
	}
	return plan, nil
}

func parseSupplementPart(payload map[string]any) (SupplementPart, error) {
	rawType, ok := payload["type"].(string)
	if !ok {
		return SupplementPart{}, errors.New("回复补充编排节点缺少 type")
	}
	part := SupplementPart{Type: rawType}
	switch rawType {
	case SupplementNodeReply:
		if err := rejectUnknownKeys(payload, "type"); err != nil {
			return SupplementPart{}, err
		}
	case SupplementNodeAt:
		if err := rejectUnknownKeys(payload, "type", "msg_id"); err != nil {
			return SupplementPart{}, err
		}
		msgID, ok := payload["msg_id"].(string)
		if !ok || strings.TrimSpace(msgID) == "" {
			return SupplementPart{}, errors.New("回复补充编排的 @ 节点缺少 msg_id")
		}
		part.MsgID = msgID
	case SupplementNodePicture:
		if err := rejectUnknownKeys(payload, "type", "source", "index"); err != nil {
			return SupplementPart{}, err
		}
		source, ok := payload["source"].(string)
		if !ok || strings.TrimSpace(source) == "" {
			return SupplementPart{}, errors.New("回复补充编排的图片节点缺少 source")
		}
		index, ok := payload["index"].(float64)
		if !ok || index < 0 {
			return SupplementPart{}, errors.New("回复补充编排的图片序号必须是非负整数")
		}
		part.Source = source
		part.Index = int(index)
	case SupplementNodeEmoji:
		if err := rejectUnknownKeys(payload, "type", "index"); err != nil {
			return SupplementPart{}, err
		}
		index, ok := payload["index"].(float64)
		if !ok || index < 1 {
			return SupplementPart{}, errors.New("回复补充编排的表情编号必须是从 1 开始的整数")
		}
		part.Index = int(index)
	default:
		return SupplementPart{}, fmt.Errorf("回复补充编排包含未知节点类型：%s", rawType)
	}
	return part, nil
}

func rejectUnknownKeys(payload map[string]any, allowed ...string) error {
	for key := range payload {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("回复补充编排节点包含未知字段：%s", key)
		}
	}
	return nil
}

// SupplementResourceOptions 采集资源时的平台开关
type SupplementResourceOptions struct {
	IsGroup               bool
	EnableReplyAt         bool
	UseNewSendLogic       bool
	GroupQuoteFrequency   string
	PrivateQuoteFrequency string
}

// SupplementResources 只允许引用本轮 Planner 已看到的资源，不受生成期间新消息影响
type SupplementResources struct {
	Messages   map[string]ContextMessage
	Pictures   map[string][]ImageComponent
	Emojis     map[int]string
	AllowAt    bool
	AllowEmoji bool
	AllowQuote bool
}

// CaptureSupplementResources 从 Planner 上下文历史中采集可引用资源
//
// 对齐 Python SupplementResources.capture：
//   - messages 只收真实收发消息（SessionBackedMessage），按 session_id 精确过滤并排除 notify；
//   - pictures 收任意带 message_id 且含图片的内容消息，不受 session_id 限制；
//   - emojis 收表情候选快照的序号映射。
func CaptureSupplementResources(history []ContextMessage, sessionID string, options SupplementResourceOptions) SupplementResources {
	resources := SupplementResources{
		Messages: map[string]ContextMessage{},
		Pictures: map[string][]ImageComponent{},
		Emojis:   map[int]string{},
	}
	for _, message := range history {
		// SessionBackedMessage 语义：真实收发的 IM 消息（user / guided_reply），排除工具结果、参考与内部上下文
		if isSessionBackedHistoryMessage(message) && message.MessageID != "" && !message.MetadataBool("is_notify") {
			if message.SessionID == "" || message.SessionID == sessionID {
				resources.Messages[message.MessageID] = message
			}
		}
		// 图片来源对齐 ContentContextMessage：任意带编号且含图片的内容消息，不受 session_id 限制
		if message.MessageID != "" && len(message.Images) > 0 && !message.MetadataBool("is_notify") {
			resources.Pictures[message.MessageID] = message.Images
		}
		if rawHashes, ok := message.Metadata["emoji_hashes"]; ok {
			switch hashes := rawHashes.(type) {
			case map[int]string:
				for index, hash := range hashes {
					resources.Emojis[index] = hash
				}
			case map[string]any:
				for rawIndex, item := range hashes {
					var index int
					if _, err := fmt.Sscanf(rawIndex, "%d", &index); err != nil {
						continue
					}
					if hash, ok := item.(string); ok {
						resources.Emojis[index] = hash
					}
				}
			}
		}
	}
	quoteFrequency := options.PrivateQuoteFrequency
	if options.IsGroup {
		quoteFrequency = options.GroupQuoteFrequency
	}
	resources.AllowAt = options.IsGroup && options.EnableReplyAt
	resources.AllowEmoji = options.UseNewSendLogic
	resources.AllowQuote = quoteFrequency != "" && quoteFrequency != "never"
	return resources
}

// isSessionBackedHistoryMessage 判断历史条目是否代表真实收发的 IM 消息
//
// 对齐 Python SessionBackedMessage：source 为 user / guided_reply 且有消息编号。
// 工具结果、参考消息与内部生成上下文都不算可引用消息。
func isSessionBackedHistoryMessage(message ContextMessage) bool {
	switch message.Source {
	case "", SourceUser, SourceGuidedReply:
		return message.MessageID != ""
	default:
		return false
	}
}

// Inventory 编排器可见的资源清单
func (r SupplementResources) Inventory() map[string]any {
	messageIDs := make([]string, 0, len(r.Messages))
	for key := range r.Messages {
		messageIDs = append(messageIDs, ToDisplayMessageID(key))
	}
	sort.Strings(messageIDs)
	pictureKeys := make([]string, 0, len(r.Pictures))
	for key := range r.Pictures {
		pictureKeys = append(pictureKeys, key)
	}
	sort.Strings(pictureKeys)
	pictures := make([]map[string]any, 0, len(pictureKeys))
	for _, key := range pictureKeys {
		source := key
		if !strings.HasPrefix(key, "tool_result:") {
			source = ToDisplayMessageID(key)
		}
		indices := make([]int, len(r.Pictures[key]))
		for index := range r.Pictures[key] {
			indices[index] = index
		}
		pictures = append(pictures, map[string]any{"source": source, "indices": indices})
	}
	emojiIndices := make([]int, 0, len(r.Emojis))
	if r.AllowEmoji {
		for index := range r.Emojis {
			emojiIndices = append(emojiIndices, index)
		}
		sort.Ints(emojiIndices)
	}
	return map[string]any{
		"message_ids":   messageIDs,
		"pictures":      pictures,
		"emoji_indices": emojiIndices,
		"allow_at":      r.AllowAt,
		"allow_quote":   r.AllowQuote,
	}
}

// ResolveSupplementID 把模型给出的编号换回资源键，支持展示别名
func ResolveSupplementID(value string, candidates []string) (string, error) {
	matches := make([]string, 0, 1)
	for _, key := range candidates {
		if value == key || value == ToDisplayMessageID(key) {
			matches = append(matches, key)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("回复补充器引用的资源不存在或编号有歧义：%s", value)
	}
	return matches[0], nil
}
