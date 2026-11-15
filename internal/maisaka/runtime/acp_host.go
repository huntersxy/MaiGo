package runtime

// ACP 传话、桌宠跨会话任务与文件入站/出站接缝

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/platform_io/drivers/onebot"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

// FileIngestor 在消息落库前登记入站文件组件；实现方负责更新 msg.Files
type FileIngestor func(ctx context.Context, msg *onebot.ConversationMessage, sessionID string) error

// ExternalFileSender 非 QQ 平台的文件出站接缝
type ExternalFileSender func(ctx context.Context, platform, conversationKind string, targetID int64, fileURL, name string) (int64, uint64, error)

// SetFileIngestor 注入入站文件登记器
func (a *Agent) SetFileIngestor(ingestor FileIngestor) {
	a.setCallbacks(func(c *AgentCallbacks) { c.FileIngestor = ingestor })
}

// SetExternalFileSender 注入非 QQ 平台文件发送实现
func (a *Agent) SetExternalFileSender(sender ExternalFileSender) {
	a.setCallbacks(func(c *AgentCallbacks) { c.ExternalFileSender = sender })
}

// SendFileToConversationForPlatform 按平台选择文件出站链路
func (a *Agent) SendFileToConversationForPlatform(ctx context.Context, platform, conversationKind string, targetID int64, fileURL, name string) (int64, uint64, error) {
	if a == nil {
		return 0, 0, fmt.Errorf("Agent 未就绪")
	}
	if platform != "" && !strings.EqualFold(strings.TrimSpace(platform), "qq") {
		sender := a.externalFileSenderPort()
		if sender == nil {
			return 0, 0, fmt.Errorf("文件发送尚未支持该平台")
		}
		return sender(ctx, platform, conversationKind, targetID, fileURL, name)
	}
	return a.SendFileToConversation(ctx, conversationKind, targetID, fileURL, name)
}

// EnqueueInternalTask 向真实会话投递内部任务并唤醒思考，对齐 enqueue_internal_task
func (a *Agent) EnqueueInternalTask(ctx context.Context, kind string, targetID int64, text, sourceKind, reason string) (map[string]any, error) {
	return a.EnqueueInternalTaskWithImages(ctx, kind, targetID, text, sourceKind, reason, nil)
}

// EnqueueInternalTaskWithImages 带图片附件投递内部任务，消息编号与任务编号一致
func (a *Agent) EnqueueInternalTaskWithImages(ctx context.Context, kind string, targetID int64, text, sourceKind, reason string, images []media.Ref) (map[string]any, error) {
	if a == nil || a.memory == nil {
		return nil, fmt.Errorf("Agent 未就绪")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("任务内容不能为空")
	}
	if kind != memory.ConversationKindGroup && kind != memory.ConversationKindPrivate {
		return nil, fmt.Errorf("目标会话类型无效")
	}
	if targetID <= 0 {
		return nil, fmt.Errorf("目标会话无效")
	}
	allowed, err := a.memory.ConversationAllowed(ctx, kind, targetID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("适配器策略禁止向此聊天流传话")
	}
	now := time.Now()
	message, err := internalTaskMessage(text, sourceKind, reason, images, now)
	if err != nil {
		return nil, err
	}
	payload, err := sonic.Marshal(message)
	if err != nil {
		return nil, err
	}
	entry, err := a.memory.AppendUserTranscript(ctx, kind, targetID, 0, payload, newRunID())
	if err != nil {
		return nil, err
	}
	// 任务编号依赖 transcript 行编号，插入后补齐 attach_pic 引用并回写持久化消息
	taskID := fmt.Sprintf("%s:%d", sourceKind, entry.ID)
	if len(images) > 0 {
		message.ContentBlocks[0].UserInputText.Text += "\n[图片附件引用] attach_pic=" + renderAttachPicReferences(taskID, len(images))
	}
	message.Extra["message_id"] = taskID
	finalPayload, err := sonic.Marshal(message)
	if err != nil {
		return nil, err
	}
	if err := a.memory.GetDB().WithContext(ctx).Model(&memory.TranscriptEntry{}).Where("id = ?", entry.ID).Update("payload", string(finalPayload)).Error; err != nil {
		return nil, err
	}
	session := a.sessions.get(kind, targetID)
	session.AppendInternalTaskMessage(message, entry.ID, now, images)
	if kind == memory.ConversationKindPrivate {
		a.schedulePrivateThink(targetID, now)
	} else {
		a.scheduleGroupThink(targetID, now)
	}
	return map[string]any{"task_id": taskID, "queued": true}, nil
}

// internalTaskMessage 组装内部任务消息：首块文本、后续图片块，message_id 在编号确定后补齐
func internalTaskMessage(text, sourceKind, reason string, images []media.Ref, now time.Time) (*schema.AgenticMessage, error) {
	blocks := make([]*schema.ContentBlock, 0, len(images)+1)
	blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: text}))
	for index, ref := range images {
		image := mediaRefImageBlock(ref)
		if image == nil {
			return nil, fmt.Errorf("图片附件读取失败：index=%d hash=%s", index, ref.Hash)
		}
		blocks = append(blocks, schema.NewContentBlock(image))
	}
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}
	message.Extra = map[string]any{
		"source":      "internal_task",
		"source_kind": sourceKind,
		"reason":      reason,
		"created_at":  now,
	}
	return message, nil
}

// renderAttachPicReferences 生成模型可见的附件引用，空格风格对齐 Python json.dumps
func renderAttachPicReferences(taskID string, count int) string {
	displayID := maisaka.ToDisplayMessageID(taskID)
	var builder strings.Builder
	builder.WriteString("[")
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString("{\"msg_id\": \"")
		builder.WriteString(displayID)
		builder.WriteString("\", \"index\": ")
		builder.WriteString(strconv.Itoa(index))
		builder.WriteString("}")
	}
	builder.WriteString("]")
	return builder.String()
}
