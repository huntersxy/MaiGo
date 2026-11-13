package learners

import "strings"

// ContextMessage 学习链路用的裁切上下文最小抽象（独立于 maisaka.ContextMessage，避免反向依赖）
type ContextMessage struct {
	MessageID  string
	SourceKind string // user / assistant / tool 等
	Text       string
	IsBotSelf  bool
	// IsNotify 平台通知以 user 来源进入规划器，但不是用户发言
	IsNotify bool
	// 以下字段对齐 MaiBot 消息来源信息；agent 未填充时按空值降级
	Platform string
	UserID   string
	Name     string
	// CardName 群名片名，优先于 Name 作为学习 prompt 的 speaker 名
	CardName  string
	GroupName string
	SessionID string
}

// OtherUserTexts 提取其他用户可见文字，对齐 jargon_context_matcher._extract_other_user_texts
func OtherUserTexts(messages []ContextMessage) []string {
	texts := make([]string, 0, len(messages))
	seen := map[string]struct{}{}
	for _, message := range messages {
		if message.SourceKind != "user" || message.IsBotSelf || message.IsNotify {
			continue
		}
		if message.MessageID != "" {
			if _, ok := seen[message.MessageID]; ok {
				continue
			}
			seen[message.MessageID] = struct{}{}
		}
		text := strings.TrimSpace(strings.Join(strings.Fields(message.Text), " "))
		if text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}
