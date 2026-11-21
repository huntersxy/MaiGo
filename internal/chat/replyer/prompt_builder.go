package replyer

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

var errPromptLoaderMissing = fmt.Errorf("replyer prompt 加载器未接线")

const defaultReplyPersonality = "是人类。"

// BuildPersonalityPrompt 对齐 _build_personality_prompt
func (b *PromptBuilder) BuildPersonalityPrompt() string {
	// 对齐 Python：bot_name / alias_names 原样使用（不做 TrimSpace，空名不回退「麦麦」）；
	// 「麦麦」仅出现在 Python 异常兜底路径，正常路径保持空昵称可见。
	botName := b.Config.BotName
	botAliases := ""
	if len(b.Config.BotAliases) > 0 {
		botAliases = "，也有人叫你" + strings.Join(b.Config.BotAliases, ",")
	}
	personality := strings.TrimSpace(b.Config.Personality)
	if personality == "" {
		personality = defaultReplyPersonality
	}
	return fmt.Sprintf("你的名字是%s%s。\n%s", botName, botAliases, personality)
}

// SelectReplyStyle 返回基础表达风格
func (b *PromptBuilder) SelectReplyStyle() string {
	return b.Config.ReplyStyle
}

// SelectTemporaryReplyStyle 按概率选择一次性备用风格
func (b *PromptBuilder) SelectTemporaryReplyStyle() string {
	candidates := make([]string, 0, len(b.Config.MultipleReplyStyles))
	for _, style := range b.Config.MultipleReplyStyles {
		if trimmed := strings.TrimSpace(style); trimmed != "" {
			candidates = append(candidates, trimmed)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	probability := b.Config.MultipleProbability
	if probability <= 0 {
		return ""
	}
	rng := b.Rand
	if rng == nil {
		rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	if rng.Float64() > probability {
		return ""
	}
	return candidates[rng.IntN(len(candidates))]
}

// BuildReplyReferenceMessage 有 reference 时只使用参考，否则使用 Planner 正文，并标注为当前想法
func (b *PromptBuilder) BuildReplyReferenceMessage(replyReason, replyReference string) string {
	thought := strings.TrimSpace(replyReference)
	if thought == "" {
		thought = strings.TrimSpace(replyReason)
	}
	if thought == "" {
		return ""
	}
	locale := strings.ToLower(strings.TrimSpace(b.Config.PromptLocale))
	if strings.HasPrefix(locale, "en") {
		return "Current thought:\n\"" + thought + "\""
	}
	if strings.HasPrefix(locale, "ja") {
		return "現在の考え：\n「" + thought + "」"
	}
	return "当前想法：\n“" + thought + "”"
}

// BuildRetryConstraintSentence 对齐 _build_retry_constraint_sentence
func BuildRetryConstraintSentence(reason, previousResponse string) string {
	normalizedReason := strings.Join(strings.Fields(reason), " ")
	normalizedReason = strings.TrimRight(normalizedReason, "。！？!?；;，,")
	if normalizedReason == "" {
		return ""
	}
	normalizedResponse := strings.Join(strings.Fields(previousResponse), " ")
	normalizedResponse = strings.ReplaceAll(normalizedResponse, "\"", "\\\"")
	return fmt.Sprintf("由于%s，之前生成的回复\"%s\"不符合要求，你需要重新生成回复。", normalizedReason, normalizedResponse)
}

// BuildReplyRequirements 对齐 _build_reply_requirements：额外要求 + 最近 3 条重生成约束
func (b *PromptBuilder) BuildReplyRequirements(extra string, retryConstraints []string) string {
	sections := make([]string, 0, 2)
	if normalized := strings.TrimSpace(extra); normalized != "" {
		sections = append(sections, "【额外回复要求】\n"+normalized)
	}
	if len(retryConstraints) > 0 {
		start := len(retryConstraints) - 3
		if start < 0 {
			start = 0
		}
		lines := []string{"【重生成约束】"}
		lines = append(lines, retryConstraints[start:]...)
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func requestPartsText(parts []RequestPart) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Kind == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

// BuildDocumentMessages 对齐 document_generator._build_request_messages
func (b *DocumentBuilder) BuildDocumentMessages(req ReplyRequest) ([]RequestMessage, error) {
	systemPrompt, err := b.LoadPrompt("maisaka_replyer_document", map[string]string{
		"identity":          b.BuildPersonalityPrompt(),
		"reply_style":       b.SelectReplyStyle(),
		"expression_habits": req.ExpressionHabits,
	})
	if err != nil {
		return nil, fmt.Errorf("加载 maisaka_replyer_document 失败: %w", err)
	}
	reference := b.BuildReplyReferenceMessage(req.ReplyReason, req.ReplyReference)
	replyRequirements := b.BuildReplyRequirements(req.ReplyRequirements, req.RetryConstraints)
	return []RequestMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: reference},
		{Role: "user", Content: replyRequirements},
	}, nil
}

// ResolveDocumentModel 对齐 DocumentReplyGenerator._build_generation_options：
// 调用方显式指定模型优先，否则使用构造时注入的 document_model_name
func (b *DocumentBuilder) ResolveDocumentModel(requested string) string {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(b.DocumentModel)
}

func stringValue(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return fmt.Sprint(value)
	}
}

func intValue(raw any) int {
	switch value := raw.(type) {
	case nil:
		return 0
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
