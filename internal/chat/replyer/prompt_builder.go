package replyer

import (
	"context"
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

// BuildSystemPrompt 对齐 _build_system_prompt
func (b *PromptBuilder) BuildSystemPrompt() (string, error) {
	systemPrompt, err := b.LoadPrompt("maisaka_replyer", map[string]string{
		"bot_name":                   b.Config.BotName,
		"group_chat_attention_block": b.BuildGroupChatAttentionBlock(),
		"replyer_output_instruction": b.BuildReplyerOutputInstruction(),
		"identity":                   b.BuildPersonalityPrompt(),
		"reply_style":                b.SelectReplyStyle(),
	})
	if err != nil {
		// 对齐 _build_system_prompt：模板加载失败时回退到通用人设，仍继续追加注意力漂移
		systemPrompt = "你是一个友好的 AI 助手，请根据聊天记录自然回复。"
	}
	if drift := strings.TrimSpace(b.Config.AttentionDriftBlock); drift != "" {
		systemPrompt += "\n\n" + drift
	}
	return systemPrompt, nil
}

// BuildReplyInstruction 对齐 _build_reply_instruction
func (b *PromptBuilder) BuildReplyInstruction() string {
	return "请自然地回复。不要输出多余说明、括号、@ 或额外标记，只输出实际要发言的内容。"
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

// BuildTemporaryReplyStyleMessage 对齐 _build_temporary_reply_style_message
func BuildTemporaryReplyStyleMessage(replyStyle string) string {
	normalized := strings.TrimSpace(replyStyle)
	if normalized == "" {
		return ""
	}
	return "你的说话风格可以尝试：\n" + normalized
}

// BuildRequestedReplyStyleMessage 对齐 _build_requested_reply_style_message
func (b *PromptBuilder) BuildRequestedReplyStyleMessage(replyStyle string) string {
	styleMessages := replyLengthInstructions(b.Config.PromptLocale)
	normalized := strings.TrimSpace(replyStyle)
	if normalized == "" {
		normalized = "正常回复"
	}
	return styleMessages[normalized]
}

func replyLengthInstructions(locale string) map[string]string {
	switch {
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en"):
		return map[string]string{
			"简短表达": "Reply in one or two short sentences, as you would in a casual conversation.",
			"正常回复": "Keep the reply concise and give the topic as much space as it needs.",
			"长回复":  "Use as much space as needed to make the point clear, and expand on key details.",
		}
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "ja"):
		return map[string]string{
			"简短表达": "普段の会話のように、短い一文か二文で自然に返信してください。",
			"正常回复": "話題に合った長さで、簡潔に返信してください。",
			"长回复":  "要点が伝わる長さを目安に、重要な点は掘り下げて説明してください。",
		}
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "ru"):
		return map[string]string{
			"简短表达": "Ответьте одной или двумя короткими фразами, как в обычном разговоре.",
			"正常回复": "Отвечайте кратко и соразмерно теме.",
			"长回复":  "Пишите столько, сколько нужно для ясности, и поясните важные детали.",
		}
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "fr"):
		return map[string]string{
			"简短表达": "Répondez en une ou deux phrases courtes, comme dans une conversation ordinaire.",
			"正常回复": "Restez concis et adaptez la longueur de la réponse au sujet.",
			"长回复":  "Prenez la place nécessaire pour être clair et développez les points importants.",
		}
	default:
		return map[string]string{
			"简短表达": "用一到两句口语短句回复，像平时接话一样自然。",
			"正常回复": "篇幅与当前话题相称，保持简洁。",
			"长回复":  "本次篇幅以说清问题为准，可以展开解释关键细节。",
		}
	}
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

// BuildFinalUserMessage 对齐 _build_final_user_message
func (b *PromptBuilder) BuildFinalUserMessage(ctx context.Context, history []ChatMessage, target *ReplyTarget, replyRequirements, keywordsReactionPrompt string, replyToolArgs map[string]any) (string, error) {
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	sections := []string{"当前时间：" + now.Format("2006-01-02 15:04:05")}
	if block := b.BuildTargetMessageBlock(target); block != "" {
		sections = append(sections, block)
	}
	if reminder := strings.TrimSpace(stringValue(replyToolArgs[duplicateTargetReplyReminderArg])); reminder != "" {
		sections = append(sections, reminder)
	}
	if requirements := strings.TrimSpace(replyRequirements); requirements != "" {
		sections = append(sections, requirements)
	}
	if keywords := strings.TrimSpace(keywordsReactionPrompt); keywords != "" {
		sections = append(sections, keywords)
	}
	attachmentPrompt, err := b.BuildReplyAttachmentPrompt(history, target, replyToolArgs)
	if err != nil {
		return "", err
	}
	if trimmed := strings.TrimSpace(attachmentPrompt); trimmed != "" {
		sections = append(sections, "【额外发送内容参考】\n"+trimmed)
	}
	sections = append(sections, b.BuildReplyInstruction())
	return strings.Join(sections, "\n\n"), nil
}

// BuildHistoryMessages 对齐 _build_history_messages；默认不启用图片视觉编码
func (b *PromptBuilder) BuildHistoryMessages(history []ChatMessage) []RequestMessage {
	return b.BuildHistoryMessagesWithVisual(history, false, 0)
}

// BuildHistoryMessagesWithVisual 对齐 _build_history_messages：
// enableVisualMessage 为 true 时图片/表情包转为多模态内容块，纯图片消息不再被丢弃
func (b *PromptBuilder) BuildHistoryMessagesWithVisual(history []ChatMessage, enableVisualMessage bool, maxImageSide int) []RequestMessage {
	items := make([]RequestMessage, 0, len(history))
	for _, message := range history {
		if message.IsNotify {
			continue
		}
		if message.SourceKind == "guided_reply" {
			if content := normalizeReplyContent(message.Content, 500); content != "" {
				items = append(items, RequestMessage{Role: "assistant", Content: content})
			}
			continue
		}
		switch message.Role {
		case "assistant":
			content := firstNonEmpty(message.VisibleReply, message.Content)
			if trimmed := strings.TrimSpace(content); trimmed != "" {
				items = append(items, textRequestMessage("assistant", trimmed))
			}
		case "user":
			parts := buildUserRequestParts(message, enableVisualMessage, maxImageSide)
			if len(parts) == 0 {
				continue
			}
			content := requestPartsText(parts)
			if strings.TrimSpace(content) == "" && !hasRequestImage(parts) {
				continue
			}
			items = append(items, RequestMessage{Role: "user", Content: content, Parts: parts})
		}
	}
	return items
}

// buildUserRequestParts 按组件顺序生成文本/图片内容块
func buildUserRequestParts(message ChatMessage, enableVisualMessage bool, maxImageSide int) []RequestPart {
	parts := make([]RequestPart, 0, len(message.Parts)+1)
	appendText := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		parts = append(parts, RequestPart{Kind: "text", Text: text})
	}
	appendImage := func(image RequestImage) {
		copied := image
		parts = append(parts, RequestPart{Kind: "image", Image: &copied})
	}
	if len(message.Parts) == 0 {
		appendText(message.Content)
		if enableVisualMessage {
			for _, image := range message.Images {
				if encoded, ok := transcodeImage(image.Base64, maxImageSide); ok {
					appendImage(encoded)
				}
			}
		}
		return parts
	}
	for _, part := range message.Parts {
		switch part.Kind {
		case PartText:
			appendText(part.Text)
		case PartAt:
			appendText(renderTargetAt(part))
		case PartReply:
			continue
		case PartImage:
			if enableVisualMessage {
				if encoded, ok := transcodeImage(part.ImageBase64, maxImageSide); ok {
					appendImage(encoded)
					continue
				}
			}
			appendText(firstNonEmpty(strings.TrimSpace(part.Description), "[图片，识别中.....]"))
		case PartEmoji:
			if enableVisualMessage {
				if encoded, ok := transcodeImage(part.ImageBase64, emojiMaxSide); ok {
					appendText("[消息类型]表情包")
					appendImage(encoded)
					continue
				}
			}
			appendText(firstNonEmpty(strings.TrimSpace(part.Description), "[表情包]"))
		case PartVoice:
			appendText(firstNonEmpty(strings.TrimSpace(part.Description), "[语音消息]"))
		}
	}
	return parts
}

func textRequestMessage(role, content string) RequestMessage {
	return RequestMessage{
		Role:    role,
		Content: content,
		Parts:   []RequestPart{{Kind: "text", Text: content}},
	}
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

func hasRequestImage(parts []RequestPart) bool {
	for _, part := range parts {
		if part.Kind == "image" && part.Image != nil {
			return true
		}
	}
	return false
}

// limitLatestRequestImages 对齐 limit_latest_images_in_messages：
// 只保留最后 limit 张图片，其余在原位置替换为文本 "[图片]"
func limitLatestRequestImages(items []RequestMessage, limit int) []RequestMessage {
	if limit < 0 {
		limit = 0
	}
	total := 0
	for _, item := range items {
		for _, part := range item.Parts {
			if part.Kind == "image" && part.Image != nil {
				total++
			}
		}
	}
	if total <= limit {
		return items
	}
	drop := total - limit
	for index := range items {
		parts := items[index].Parts
		for partIndex := range parts {
			if drop == 0 {
				break
			}
			if parts[partIndex].Kind != "image" || parts[partIndex].Image == nil {
				continue
			}
			parts[partIndex] = RequestPart{Kind: "text", Text: "[图片]"}
			drop--
		}
		items[index].Parts = parts
		items[index].Content = requestPartsText(parts)
	}
	return items
}

// BuildRequestMessages 对齐 _build_request_messages 的普通模板分支
func (b *PromptBuilder) BuildRequestMessages(ctx context.Context, req ReplyRequest) ([]RequestMessage, error) {
	// 关键词反应由内置插件通过 maisaka.replyer.before_request 追加到回复要求
	keywordsReactionPrompt := ""
	systemPrompt, err := b.BuildSystemPrompt()
	if err != nil {
		return nil, err
	}
	replyRequirements := b.BuildReplyRequirements(req.ReplyRequirements, req.RetryConstraints)
	finalUserMessage, err := b.BuildFinalUserMessage(ctx, req.History, req.Target, replyRequirements, keywordsReactionPrompt, req.ReplyToolArgs)
	if err != nil {
		return nil, err
	}
	temporaryStyleMessage := BuildTemporaryReplyStyleMessage(b.SelectTemporaryReplyStyle())
	replyReferenceMessage := b.BuildReplyReferenceMessage(req.ReplyReason, req.ReplyReference)
	requestedStyleMessage := b.BuildRequestedReplyStyleMessage(req.ReplyStyle)

	items := make([]RequestMessage, 0, len(req.History)+6)
	items = append(items, RequestMessage{Role: "system", Content: systemPrompt})
	maxImageSide := resolveImageMaxSide(req.ImageQuality, req.MaxImageSide)
	items = append(items, b.BuildHistoryMessagesWithVisual(req.History, req.EnableVisualMessage, maxImageSide)...)
	if trimmed := strings.TrimSpace(req.ExpressionHabits); trimmed != "" {
		items = append(items, RequestMessage{Role: "user", Content: trimmed})
	}
	if temporaryStyleMessage != "" {
		items = append(items, RequestMessage{Role: "user", Content: temporaryStyleMessage})
	}
	if replyReferenceMessage != "" {
		items = append(items, RequestMessage{Role: "user", Content: replyReferenceMessage})
	}
	items = append(items, RequestMessage{Role: "user", Content: finalUserMessage})
	if requestedStyleMessage != "" {
		items = append(items, RequestMessage{Role: "user", Content: requestedStyleMessage})
	}
	if req.EnableVisualMessage {
		items = limitLatestRequestImages(items, resolveMaxImageNum(req.MaxImageNum))
	}
	return items, nil
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
