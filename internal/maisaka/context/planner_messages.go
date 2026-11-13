package context

import (
	"fmt"
	"html"
	"strings"
	"time"

	maisaka "maibot-go/internal/maisaka"
)

// PlannerPrefixInput 规划器消息前缀构造参数
type PlannerPrefixInput struct {
	Timestamp        time.Time
	UserName         string
	GroupCard        string
	MessageID        string
	ChatID           string
	ChatName         string
	QuoteIDs         []string
	IncludeMessageID bool
	IncludeChatID    bool
	IsSelfMessage    bool
}

// BuildPlannerPrefix 构造 Maisaka 规划器使用的统一消息前缀
func BuildPlannerPrefix(input PlannerPrefixInput) string {
	attributes := make([]string, 0, 6)
	if input.IncludeMessageID {
		attributes = append(attributes, fmt.Sprintf("msg_id=\"%s\"", escapeAttribute(ToDisplayMessageID(input.MessageID))))
	}
	if input.IncludeChatID {
		chatID := strings.TrimSpace(input.ChatID)
		if chatID != "" {
			attributes = append(attributes, fmt.Sprintf("session_id=\"%s\"", escapeAttribute(chatID)))
			if input.ChatName != "" {
				attributes = append(attributes, fmt.Sprintf("chat_name=\"%s\"", escapeAttribute(input.ChatName)))
			}
		}
	}
	if quote := FormatQuoteIDs(input.QuoteIDs); quote != "" {
		attributes = append(attributes, fmt.Sprintf("quote=\"%s\"", escapeAttribute(quote)))
	}
	attributes = append(attributes,
		fmt.Sprintf("time=\"%s\"", escapeAttribute(input.Timestamp.Format("15:04:05"))),
		fmt.Sprintf("user=\"%s\"", escapeAttribute(input.UserName)),
	)
	if card := strings.TrimSpace(input.GroupCard); card != "" {
		attributes = append(attributes, fmt.Sprintf("group_card=\"%s\"", escapeAttribute(card)))
	}
	if input.IsSelfMessage {
		attributes = append(attributes, "is_self_message=\"true\"")
	}
	return "<message " + strings.Join(attributes, " ") + ">\n"
}

// FormatQuoteIDs 将引用消息 ID 列表格式化为 XML 属性值
func FormatQuoteIDs(quoteIDs []string) string {
	normalized := make([]string, 0, len(quoteIDs))
	seen := map[string]struct{}{}
	for _, rawQuoteID := range quoteIDs {
		quoteID := ToDisplayMessageID(rawQuoteID)
		if quoteID == "" {
			continue
		}
		if _, dup := seen[quoteID]; dup {
			continue
		}
		seen[quoteID] = struct{}{}
		normalized = append(normalized, quoteID)
	}
	return strings.Join(normalized, ",")
}

// ExtractQuoteIDs 从历史消息里提取引用目标 ID
func ExtractQuoteIDs(messages []maisaka.ContextMessage) []string {
	quoteIDs := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, message := range messages {
		if message.Metadata == nil {
			continue
		}
		for _, quoteID := range metadataQuoteIDs(message.Metadata["quote_ids"]) {
			trimmed := strings.TrimSpace(quoteID)
			if trimmed == "" {
				continue
			}
			if _, dup := seen[trimmed]; dup {
				continue
			}
			seen[trimmed] = struct{}{}
			quoteIDs = append(quoteIDs, trimmed)
		}
	}
	return quoteIDs
}

// metadataQuoteIDs 解析历史元数据里的引用编号，兼容字符串、字符串列表与数值列表
func metadataQuoteIDs(raw any) []string {
	switch value := raw.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				out = append(out, typed)
			case fmt.Stringer:
				out = append(out, typed.String())
			}
		}
		return out
	case string:
		parts := strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '，'
		})
		return parts
	default:
		return nil
	}
}

// BuildPlannerUserPrefix 根据历史消息构造规划器前缀
func BuildPlannerUserPrefix(message maisaka.ContextMessage, includeMessageID, includeChatID, isSelfMessage bool) string {
	userName := strings.TrimSpace(message.UserID)
	if nickname := metadataText(message.Metadata, "user_nickname"); nickname != "" {
		userName = nickname
	}
	if userName == "" {
		userName = "unknown"
	}
	timestamp := message.Timestamp
	if timestamp.IsZero() {
		if message.ReceivedAt != nil {
			timestamp = *message.ReceivedAt
		} else {
			timestamp = time.Now()
		}
	}
	return BuildPlannerPrefix(PlannerPrefixInput{
		Timestamp:        timestamp,
		UserName:         userName,
		GroupCard:        metadataText(message.Metadata, "user_cardname"),
		MessageID:        message.MessageID,
		ChatID:           message.SessionID,
		ChatName:         metadataText(message.Metadata, "session_name"),
		QuoteIDs:         ExtractQuoteIDs([]maisaka.ContextMessage{message}),
		IncludeMessageID: includeMessageID && !message.MetadataBool("is_notify") && message.MessageID != "",
		IncludeChatID:    includeChatID,
		IsSelfMessage:    isSelfMessage,
	})
}

func escapeAttribute(value string) string {
	return html.EscapeString(strings.TrimSpace(value))
}

func metadataText(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}
