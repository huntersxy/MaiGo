package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"MaiGo/internal/maisaka"
)

// agentReplySupplementGenerator 复用 Planner 模型与请求前缀生成回复补充编排
type agentReplySupplementGenerator struct {
	agent *Agent
	ctx   context.Context
}

// GenerateSupplement 在 Planner 快照末尾追加编排任务并解析结构化输出
func (g *agentReplySupplementGenerator) GenerateSupplement(snapshot maisaka.PlannerRequestSnapshot, instruction string, schema map[string]any) (maisaka.SupplementGeneration, error) {
	if g == nil || g.agent == nil {
		return maisaka.SupplementGeneration{}, fmt.Errorf("回复补充器缺少 Agent 绑定")
	}
	generation := generationFrom(g.ctx)
	if generation == nil {
		generation = g.agent.currentGeneration()
	}
	if generation == nil {
		return maisaka.SupplementGeneration{}, fmt.Errorf("回复补充器模型未就绪")
	}
	model := generation.plannerModel
	modelName := generation.plannerModelName
	if model == nil {
		model = generation.model
		modelName = generation.modelName
	}
	if model == nil {
		return maisaka.SupplementGeneration{}, fmt.Errorf("回复补充器模型未就绪")
	}
	items := make([]maisaka.ContextItem, 0, len(snapshot.Items)+1)
	items = append(items, snapshot.Items...)
	items = append(items, maisaka.ContextItem{Role: maisaka.RoleUser, Text: instruction})
	request := maisaka.ChatRequest{
		RequestKind:   "maisaka.reply_supplement",
		ModelTaskName: snapshot.ModelTask,
		SystemPrompt:  snapshot.SystemPrompt,
		Items:         items,
		Tools:         snapshot.Tools,
		ResponseFormat: map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "ReplySupplementPlan",
				"schema": schema,
				"strict": true,
			},
		},
		SessionID: snapshot.SessionID,
	}
	client := &maisakaChatClient{model: model, modelName: modelName}
	response, err := client.Chat(g.ctx, request)
	if err != nil {
		return maisaka.SupplementGeneration{}, fmt.Errorf("回复补充器模型调用失败: %w", err)
	}
	metadata := map[string]any{}
	if strings.TrimSpace(modelName) != "" {
		metadata["model_name"] = modelName
	}
	return maisaka.SupplementGeneration{
		Raw:             strings.TrimSpace(response.Content),
		ToolCalls:       response.ToolCalls,
		NativeToolCalls: response.NativeToolCalls,
		Usage:           response.Usage,
		Metadata:        metadata,
	}, nil
}

// buildReplySupplementArguments 把补充编排映射为 reply 工具参数；表情由独立发送端口处理
func buildReplySupplementArguments(base map[string]any, content string, supplement *maisaka.ReplySupplement) (map[string]any, []string) {
	arguments := make(map[string]any, len(base)+8)
	for key, value := range base {
		arguments[key] = value
	}
	arguments["content"] = content
	arguments["_replyer_generated"] = true
	arguments["reply_reference"] = content
	if supplement == nil {
		return arguments, nil
	}
	if supplement.Quote != nil {
		messageID := strings.TrimSpace(supplement.Quote.MessageID)
		if messageID != "" {
			arguments["msg_id"] = messageID
			if parsed, err := strconv.ParseInt(messageID, 10, 64); err == nil && parsed != 0 {
				arguments["reply_to"] = parsed
			}
		}
	}
	attachAt := make([]string, 0, 2)
	attachPic := make([]any, 0, 2)
	emojis := make([]string, 0, 2)
	for _, component := range supplement.Components {
		if component == nil {
			continue
		}
		switch component.Kind {
		case "at":
			if messageID := strings.TrimSpace(component.MessageID); messageID != "" {
				attachAt = append(attachAt, messageID)
			}
		case "image":
			if messageID := strings.TrimSpace(component.MessageID); messageID != "" {
				attachPic = append(attachPic, map[string]any{"msg_id": messageID, "index": component.ImageIndex})
			}
		case "emoji":
			if hash := strings.TrimSpace(component.EmojiHash); hash != "" {
				emojis = append(emojis, hash)
			}
		}
	}
	if len(attachAt) > 0 {
		arguments["attach_at"] = attachAt
	}
	if len(attachPic) > 0 {
		arguments["attach_pic"] = attachPic
	}
	return arguments, emojis
}

// replyerGeneratedArgument 判断 reply 工具参数是否已由 Replyer 生成正文
func replyerGeneratedArgument(arguments map[string]any) bool {
	if arguments == nil {
		return false
	}
	switch value := arguments["_replyer_generated"].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

// sendEmojiByHash 按收藏表情哈希找到文件并发送，补齐补充编排的多表情语义
func (a *Agent) sendEmojiByHash(ctx context.Context, kind string, targetID int64, hash string) error {
	if a == nil || a.memory == nil {
		return fmt.Errorf("表情发送缺少记忆库")
	}
	sticker, err := a.memory.GetEmojiRecordByHash(ctx, strings.TrimSpace(hash))
	if err != nil {
		return fmt.Errorf("查找表情包失败: %w", err)
	}
	if sticker == nil {
		return fmt.Errorf("查找表情包失败: 未找到 %s", hash)
	}
	fileName := filepath.Base(filepath.FromSlash(sticker.FullPath))
	if kind == "group" {
		return a.doSendSticker(ctx, targetID, fileName, sticker.Description)
	}
	return a.doPrivateSticker(ctx, targetID, fileName, sticker.Description)
}
