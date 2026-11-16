package maisaka

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// SupplementComponent 展开后的一条消息组件
type SupplementComponent struct {
	// Kind text / at / image / emoji
	Kind       string
	Text       string
	UserID     string
	Nickname   string
	CardName   string
	MessageID  string
	Image      *ImageComponent
	ImageIndex int
	// EmojiReference 语义描述或上下文引用，交给运行时解析
	EmojiReference map[string]any
	// EmojiError 计划阶段就能确定的引用错误；运行时记日志并跳过
	EmojiError string
}

// NewTextComponent 构造文本组件
func NewTextComponent(text string) SupplementComponent {
	return SupplementComponent{Kind: "text", Text: text}
}

// ReplySupplement 解析完成的编排结果
type ReplySupplement struct {
	Plan       ReplySupplementPlan
	Quote      *ContextMessage
	Components []*SupplementComponent
	Metadata   map[string]any
}

// ReplyMessageBody 一条待发送正文，Compose 会按编排展开
type ReplyMessageBody struct {
	Components []SupplementComponent
}

// Compose 按顺序展开正文；相邻 @ 拼入正文，图片和表情作为独立消息发送
func (s ReplySupplement) Compose(body []ReplyMessageBody) ([]ReplyMessageBody, error) {
	if len(body) == 0 {
		return nil, errors.New("回复补充器不能替代空正文发送消息")
	}
	result := make([]ReplyMessageBody, 0, len(body)+len(s.Components))
	pending := make([]SupplementComponent, 0, 2)
	for _, component := range s.Components {
		if component == nil {
			merged := make([]SupplementComponent, 0, len(pending)+len(body[0].Components))
			merged = append(merged, pending...)
			merged = append(merged, body[0].Components...)
			body[0].Components = merged
			pending = pending[:0]
			result = append(result, body...)
			continue
		}
		if component.Kind == "at" {
			if len(result) > 0 && hasTextComponent(result[len(result)-1]) {
				last := &result[len(result)-1]
				last.Components = append(last.Components, NewTextComponent(" "), *component)
			} else {
				pending = append(pending, *component, NewTextComponent(" "))
			}
			continue
		}
		messageComponents := make([]SupplementComponent, 0, len(pending)+1)
		messageComponents = append(messageComponents, pending...)
		messageComponents = append(messageComponents, *component)
		pending = pending[:0]
		result = append(result, ReplyMessageBody{Components: messageComponents})
	}
	if len(pending) > 0 {
		result = append(result, ReplyMessageBody{Components: pending})
	}
	return result, nil
}

func hasTextComponent(message ReplyMessageBody) bool {
	for _, component := range message.Components {
		if component.Kind == "text" {
			return true
		}
	}
	return false
}

// ResolveSupplement 严格解析快照中的资源，全部成功后才能进入发送链路
func ResolveSupplement(plan ReplySupplementPlan, resources SupplementResources, metadata map[string]any) (ReplySupplement, error) {
	supplement := ReplySupplement{Plan: plan, Metadata: metadata}
	if plan.MsgID != "" {
		if !resources.AllowQuote {
			return ReplySupplement{}, errors.New("当前会话已关闭引用，回复补充器不能设置 msg_id")
		}
		key, err := ResolveSupplementID(plan.MsgID, sortedMessageKeys(resources.Messages))
		if err != nil {
			return ReplySupplement{}, err
		}
		quoted := resources.Messages[key]
		supplement.Quote = &quoted
	}
	components := make([]*SupplementComponent, 0, len(plan.Parts))
	for _, part := range plan.Parts {
		switch part.Type {
		case SupplementNodeReply:
			components = append(components, nil)
		case SupplementNodeAt:
			if !resources.AllowAt {
				return ReplySupplement{}, errors.New("回复时 @ 用户已关闭")
			}
			key, err := ResolveSupplementID(part.MsgID, sortedMessageKeys(resources.Messages))
			if err != nil {
				return ReplySupplement{}, err
			}
			message := resources.Messages[key]
			components = append(components, &SupplementComponent{
				Kind: "at", UserID: message.UserID, MessageID: key,
				Nickname: metadataString(message.Metadata, "user_nickname"),
				CardName: metadataString(message.Metadata, "user_cardname"),
			})
		case SupplementNodePicture:
			key, err := ResolveSupplementID(part.Source, sortedMessageKeys(resources.Pictures))
			if err != nil {
				return ReplySupplement{}, err
			}
			images := resources.Pictures[key]
			if part.Index >= len(images) {
				return ReplySupplement{}, fmt.Errorf("回复补充器图片序号超出范围：%s:%d", part.Source, part.Index)
			}
			image := images[part.Index]
			components = append(components, &SupplementComponent{Kind: "image", MessageID: key, ImageIndex: part.Index, Image: &image})
		case SupplementNodeEmoji:
			component := &SupplementComponent{Kind: "emoji"}
			if part.HasDescription || part.HasEmotion || part.HasContent {
				reference := map[string]any{}
				if part.HasDescription {
					reference["emotion"] = part.Description
				}
				if part.HasEmotion {
					reference["emotion"] = part.Emotion
				}
				if part.HasContent {
					reference["content"] = part.Content
				}
				component.EmojiReference = reference
			} else {
				source, err := ResolveSupplementID(part.Source, sortedMessageKeys(resources.ContextEmojis))
				if err != nil {
					component.EmojiError = err.Error()
				} else {
					index := 0
					if part.HasIndex {
						index = part.Index
					}
					if index < 0 || index >= len(resources.ContextEmojis[source]) {
						component.EmojiError = fmt.Sprintf("上下文表情包序号无效，该消息共有 %d 张表情包", len(resources.ContextEmojis[source]))
					} else {
						component.EmojiReference = map[string]any{"msg_id": source, "index": index}
					}
				}
			}
			components = append(components, component)
		default:
			return ReplySupplement{}, fmt.Errorf("回复补充器不支持的节点类型：%s", part.Type)
		}
	}
	supplement.Components = components
	return supplement, nil
}

func sortedMessageKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}

// SupplementGeneration 补充编排的模型返回
type SupplementGeneration struct {
	Raw             string
	ToolCalls       []ToolCall
	NativeToolCalls []NativeToolCallSummary
	Usage           ChatUsage
	Metadata        map[string]any
}

// SupplementGenerator 复用 Planner 请求快照生成补充编排
type SupplementGenerator interface {
	GenerateSupplement(snapshot PlannerRequestSnapshot, instruction string, retryInstruction string, schema map[string]any) (SupplementGeneration, error)
}

// BuildReplySupplementSchema 生成编排 JSON Schema；不允许 @ 时移除 AtNode
func BuildReplySupplementSchema(allowAt bool) map[string]any {
	defs := map[string]any{
		"ReplyNode": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"type": map[string]any{"const": "reply"}},
			"required":   []any{"type"},
		},
		"PictureNode": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"type":   map[string]any{"const": "pic"},
				"source": map[string]any{"type": "string", "minLength": 1},
				"index":  map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []any{"type", "source", "index"},
		},
		"EmojiNode": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"type":        map[string]any{"const": "emoji"},
				"index":       map[string]any{"type": "integer", "minimum": 0},
				"source":      map[string]any{"type": "string", "minLength": 1},
				"description": map[string]any{"type": "string", "minLength": 1},
				"emotion":     map[string]any{"type": "string", "minLength": 1},
				"content":     map[string]any{"type": "string", "minLength": 1},
			},
			"required": []any{"type"},
		},
	}
	oneOf := []any{
		map[string]any{"$ref": "#/$defs/ReplyNode"},
		map[string]any{"$ref": "#/$defs/PictureNode"},
		map[string]any{"$ref": "#/$defs/EmojiNode"},
	}
	mapping := map[string]any{
		"reply": "#/$defs/ReplyNode",
		"pic":   "#/$defs/PictureNode",
		"emoji": "#/$defs/EmojiNode",
	}
	discriminator := map[string]any{"propertyName": "type"}
	if allowAt {
		defs["AtNode"] = map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"type":   map[string]any{"const": "at"},
				"msg_id": map[string]any{"type": "string", "minLength": 1},
			},
			"required": []any{"type", "msg_id"},
		}
		oneOf = append(oneOf, map[string]any{"$ref": "#/$defs/AtNode"})
		mapping["at"] = "#/$defs/AtNode"
	}
	discriminator["mapping"] = mapping
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"msg_id": map[string]any{"type": "string", "minLength": 1},
			"parts": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 16,
				"items": map[string]any{"oneOf": oneOf, "discriminator": discriminator},
			},
		},
		"required": []any{"parts"},
		"$defs":    defs,
	}
}

// BuildReplySupplementInstruction 用 prompt 模板拼装补充编排指令
func BuildReplySupplementInstruction(promptTemplate, guidance string, resources SupplementResources, schema map[string]any) (string, error) {
	inventoryJSON, err := sonic.MarshalString(resources.Inventory())
	if err != nil {
		return "", err
	}
	schemaJSON, err := sonic.MarshalString(schema)
	if err != nil {
		return "", err
	}
	replaced := strings.ReplaceAll(promptTemplate, "{guidance}", guidance)
	replaced = strings.ReplaceAll(replaced, "{resources}", inventoryJSON)
	return strings.ReplaceAll(replaced, "{schema}", schemaJSON), nil
}

// GenerateReplySupplement 保留 Planner 的模型、工具及消息前缀，只在末尾追加编排任务
// 首次失败时带错误重生成一次，再失败由回复链路跳过补充
func GenerateReplySupplement(
	generator SupplementGenerator,
	promptSource PromptSource,
	snapshot PlannerRequestSnapshot,
	guidance string,
	sessionID string,
	isGroup bool,
	history []ContextMessage,
	options SupplementResourceOptions,
) (ReplySupplement, error) {
	supplement, err := generateReplySupplementOnce(
		generator, promptSource, snapshot, guidance, sessionID, isGroup, history, options, "",
	)
	if err == nil {
		return supplement, nil
	}
	zap.L().Warn("回复补充器生成失败，将重生成一次", zap.String("session_id", sessionID), zap.Error(err))
	return generateReplySupplementOnce(
		generator, promptSource, snapshot, guidance, sessionID, isGroup, history, options,
		fmt.Sprintf("%T: %v", err, err),
	)
}

func generateReplySupplementOnce(
	generator SupplementGenerator,
	promptSource PromptSource,
	snapshot PlannerRequestSnapshot,
	guidance string,
	sessionID string,
	isGroup bool,
	history []ContextMessage,
	options SupplementResourceOptions,
	retryError string,
) (ReplySupplement, error) {
	if generator == nil {
		return ReplySupplement{}, errors.New("回复补充器缺少生成端口")
	}
	if promptSource == nil {
		return ReplySupplement{}, errors.New("回复补充器缺少 prompt 源")
	}
	template, err := promptSource.Get("reply_supplement")
	if err != nil {
		return ReplySupplement{}, err
	}
	resources := CaptureSupplementResources(history, sessionID, options)
	schema := BuildReplySupplementSchema(resources.AllowAt)
	instruction, err := BuildReplySupplementInstruction(template, guidance, resources, schema)
	if err != nil {
		return ReplySupplement{}, err
	}
	retryInstruction := ""
	if retryError != "" {
		retryTemplate, retryErr := promptSource.Get("reply_supplement_retry")
		if retryErr != nil {
			return ReplySupplement{}, retryErr
		}
		retryInstruction = strings.ReplaceAll(retryTemplate, "{error}", retryError)
	}
	result, err := generator.GenerateSupplement(snapshot, instruction, retryInstruction, schema)
	if err != nil {
		return ReplySupplement{}, err
	}
	metadata := map[string]any{}
	for key, value := range result.Metadata {
		metadata[key] = value
	}
	metadata["prompt_tokens"] = result.Usage.PromptTokens
	metadata["completion_tokens"] = result.Usage.CompletionTokens
	metadata["total_tokens"] = result.Usage.PromptTokens + result.Usage.CompletionTokens
	metadata["prompt_cache_hit_tokens"] = result.Usage.CacheHitTokens
	metadata["prompt_cache_miss_tokens"] = result.Usage.CacheMissTokens
	if len(result.ToolCalls) > 0 || len(result.NativeToolCalls) > 0 {
		return ReplySupplement{}, errors.New("回复补充器不得调用工具")
	}
	plan, err := ParseReplySupplementPlan(result.Raw)
	if err != nil {
		return ReplySupplement{}, err
	}
	metadata["plan"] = planToPayload(plan)
	return ResolveSupplement(plan, resources, metadata)
}

func planToPayload(plan ReplySupplementPlan) map[string]any {
	parts := make([]any, 0, len(plan.Parts))
	for _, part := range plan.Parts {
		item := map[string]any{"type": part.Type}
		if part.MsgID != "" {
			item["msg_id"] = part.MsgID
		}
		if part.Source != "" {
			item["source"] = part.Source
		}
		if part.Type == SupplementNodePicture || part.Type == SupplementNodeEmoji {
			item["index"] = part.Index
		}
		parts = append(parts, item)
	}
	payload := map[string]any{"parts": parts}
	if plan.MsgID != "" {
		payload["msg_id"] = plan.MsgID
	}
	return payload
}
