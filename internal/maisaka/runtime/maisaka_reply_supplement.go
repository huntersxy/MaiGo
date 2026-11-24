package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"

	"go.uber.org/zap"
)

// agentReplySupplementGenerator 复用 Planner 模型与请求前缀生成回复补充编排
type agentReplySupplementGenerator struct {
	ctx context.Context
}

// GenerateSupplement 在 Planner 快照末尾追加编排任务并解析结构化输出
func (g *agentReplySupplementGenerator) GenerateSupplement(snapshot maisaka.PlannerRequestSnapshot, instruction string, retryInstruction string, _ map[string]any) (maisaka.SupplementGeneration, error) {
	if snapshot.Client == nil {
		return maisaka.SupplementGeneration{}, fmt.Errorf("回复补充器缺少 Planner 请求快照客户端")
	}
	items := make([]maisaka.ContextItem, 0, len(snapshot.Items)+2)
	items = append(items, snapshot.Items...)
	items = append(items, maisaka.ContextItem{Role: maisaka.RoleUser, Text: instruction})
	if strings.TrimSpace(retryInstruction) != "" {
		items = append(items, maisaka.ContextItem{Role: maisaka.RoleUser, Text: retryInstruction})
	}
	request := maisaka.ChatRequest{
		RequestKind:   "maisaka.reply_supplement",
		ModelTaskName: snapshot.ModelTask,
		SystemPrompt:  snapshot.SystemPrompt,
		Items:         items,
		Tools:         snapshot.Tools,
		SessionID:     snapshot.SessionID,
		Stream:        snapshot.Stream,
		MaxImageSide:  snapshot.MaxImageSide,
	}
	response, err := snapshot.Client.Chat(g.ctx, request)
	if err != nil {
		return maisaka.SupplementGeneration{}, fmt.Errorf("回复补充器模型调用失败: %w", err)
	}
	metadata := map[string]any{}
	if strings.TrimSpace(response.ModelName) != "" {
		metadata["model_name"] = response.ModelName
	}
	return maisaka.SupplementGeneration{
		Raw:             strings.TrimSpace(response.Content),
		ToolCalls:       response.ToolCalls,
		NativeToolCalls: response.NativeToolCalls,
		Usage:           response.Usage,
		Metadata:        metadata,
	}, nil
}

// buildReplySupplementArguments 把补充编排映射为 reply 工具参数并解析表情附件
func buildReplySupplementArguments(
	ctx context.Context, base map[string]any, content string, supplement *maisaka.ReplySupplement,
	resolveEmoji func(context.Context, map[string]any) (tools.EmojiAttachment, error),
) (map[string]any, []tools.EmojiAttachment) {
	arguments := make(map[string]any, len(base)+8)
	for key, value := range base {
		arguments[key] = value
	}
	arguments["content"] = content
	arguments["_replyer_generated"] = true
	arguments["chat_anlyze"] = content
	delete(arguments, "attach_emoji")
	delete(arguments, "attach_at")
	delete(arguments, "attach_pic")
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
	} else {
		// 补充器未选择引用目标时，不对原始回复目标额外发起引用
		delete(arguments, "reply_to")
	}
	attachAt := make([]string, 0, 2)
	attachPic := make([]any, 0, 2)
	emojis := make([]tools.EmojiAttachment, 0, 2)
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
			if component.EmojiError != "" {
				zap.L().Warn("attach_emoji resolution=failed source=context path=supplement", zap.String("error", component.EmojiError))
				continue
			}
			if resolveEmoji == nil || component.EmojiReference == nil {
				zap.L().Warn("attach_emoji resolution=failed path=supplement error=resolver unavailable")
				continue
			}
			attachment, err := resolveEmoji(ctx, component.EmojiReference)
			if err != nil {
				zap.L().Warn("attach_emoji resolution=failed path=supplement", zap.Error(err))
				continue
			}
			emojis = append(emojis, attachment)
			zap.L().Info("attach_emoji resolution=resolved path=supplement")
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
