package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/mcp_module"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// SendEmojiInput 对齐 send_emoji.py:get_tool_spec，模型可见参数只有 msg_id
// Python 没有 emoji_id / emoji_name 参数；按 ID 直发只保留给 reply.attach_emoji 内部通道
type SendEmojiInput struct {
	MsgID string `json:"msg_id,omitempty" jsonschema:"description=要回复的消息编号；小黑盒发评论时必须指定主帖或评论消息"`
}

// sendEmojiOutput send_emoji 的模型可见结构化结果，键集合对齐 Python structured_result
type sendEmojiOutput struct {
	Success        bool     `json:"success"`
	Message        string   `json:"message"`
	Description    string   `json:"description"`
	Emotion        []string `json:"emotion"`
	MatchedEmotion string   `json:"matched_emotion"`
	Reason         string   `json:"reason"`
	// SelectionMetadata 选择器产出的监控元数据，不进 structured_content，由结果转换器消费
	SelectionMetadata map[string]any `json:"selection_metadata,omitempty"`
}

// ShowEmojiListOutput show_emoji_list 的模型可见结构化结果
type ShowEmojiListOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// sendEmojiTool send_emoji 工具，模型可见参数只有 msg_id
type sendEmojiTool struct{ info *schema.ToolInfo }

func (t *sendEmojiTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

func (t *sendEmojiTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	input := SendEmojiInput{}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	output := sendEmojiCore(ctx, input.MsgID, 0)
	encoded, err := sonic.MarshalString(output)
	if err != nil {
		return "", err
	}
	return encoded, nil
}

// showEmojiListTool show_emoji_list 工具，拼图通过 InvokeToolResult 作为图片内容项进入上下文
type showEmojiListTool struct{ info *schema.ToolInfo }

func (t *showEmojiListTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

func (t *showEmojiListTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	output := showEmojiListCore(ctx)
	encoded, err := sonic.MarshalString(output)
	if err != nil {
		return "", err
	}
	return encoded, nil
}

// InvokeToolResult 让拼图作为 image 内容项进入模型上下文，对齐 Python post_history_messages
func (t *showEmojiListTool) InvokeToolResult(ctx context.Context, _ string) (mcp.ToolExecutionResult, error) {
	result := showEmojiListResult(ctx)
	if !result.Success {
		return mcp.ToolExecutionResult{Success: false, ErrorMessage: result.Message}, nil
	}
	item := mcp.ToolContentItem{
		ContentType: "image",
		Text:        result.Message,
		Data:        emojiCollageBase64(result.Collage),
		MIMEType:    emojiCollageMIME,
		Metadata: map[string]any{
			"emoji_hashes":  result.Hashes,
			"emoji_indices": result.Indices,
		},
	}
	return mcp.ToolExecutionResult{
		Success: true,
		Content: result.Message,
		StructuredContent: map[string]any{
			"success": true,
			"message": result.Message,
		},
		ContentItems: []mcp.ToolContentItem{item},
	}, nil
}

// showEmojiListSnapshot show_emoji_list 的内部结果，携带拼图与序号映射
type showEmojiListSnapshot struct {
	Success bool
	Message string
	Collage []byte
	Indices []int
	Hashes  map[int]string
}

// showEmojiListCore 只返回模型可见文本，供 eino 回退路径使用
func showEmojiListCore(ctx context.Context) ShowEmojiListOutput {
	result := showEmojiListResult(ctx)
	return ShowEmojiListOutput{Success: result.Success, Message: result.Message}
}

// showEmojiListResult 对齐 show_emoji_list.py:handle_tool
func showEmojiListResult(ctx context.Context) showEmojiListSnapshot {
	if !emojiVisualPlannerActive(ctx) {
		return showEmojiListSnapshot{Message: emojiVisualPlannerRequiredMessage}
	}
	mgr, err := emojiManagerForTool(ctx)
	if err != nil {
		return showEmojiListSnapshot{Message: err.Error()}
	}
	candidates := mgr.SampleCandidates(emojiCandidateLimit())
	if len(candidates) == 0 {
		return showEmojiListSnapshot{Message: emojiLibraryEmptyMessage}
	}
	// 在任何 IO 之前一次性分配连续编号，并发调用也不会拿到重复序号
	indices := nextEmojiIndices(len(candidates))
	collage, err := buildEmojiCollage(ctx, mgr, candidates, indices[0])
	if err != nil {
		return showEmojiListSnapshot{Message: "生成表情包拼图失败：" + err.Error()}
	}
	hashes := emojiHashesByIndex(indices, candidates)
	text := emojiSelectionText(indices[0], indices[len(indices)-1], emojiPassiveReplyer(ctx))
	if tc := GetToolContext(ctx); tc != nil {
		candidatesByIndex := make(map[int]uint, len(indices))
		for position, index := range indices {
			candidatesByIndex[index] = candidates[position].ID
		}
		tc.EmojiCandidates = candidatesByIndex
	}
	return showEmojiListSnapshot{Success: true, Message: text, Collage: collage, Indices: indices, Hashes: hashes}
}

// sendEmojiCore 对齐 send_emoji.py:handle_tool 的选择与发送主流程
// emojiID 非零表示 reply.attach_emoji 已解析出表情主键，直接发送并跳过视觉选择
func sendEmojiCore(ctx context.Context, msgID string, emojiID uint) sendEmojiOutput {
	output := sendEmojiOutput{Emotion: []string{}}
	if trimmed := strings.TrimSpace(msgID); trimmed != "" && !emojiTargetExists(ctx, trimmed) {
		output.Message = "请指定可回复的主帖或评论消息编号 msg_id，无法猜测表情包发送目标。"
		return output
	}
	mgr, err := emojiManagerForTool(ctx)
	if err != nil {
		output.Message = err.Error()
		return output
	}
	if emojiID > 0 {
		sent, _ := sendEmojiByStickerID(ctx, mgr, emojiID, output)
		return sent
	}
	tc := GetToolContext(ctx)
	var router *llm.Router
	if tc != nil {
		router = tc.LLMRouter
	}
	candidates := mgr.SampleCandidates(emojiCandidateLimit())
	if len(candidates) == 0 {
		output.Message = emojiLibraryEmptyMessage
		return output
	}
	indices := nextEmojiIndices(len(candidates))
	selection, selectErr := selectEmojiWithSubAgent(ctx, emojiSelectionRequest{
		Manager:       mgr,
		Router:        router,
		PlannerVisual: emojiVisualPlannerActive(ctx),
		Candidates:    candidates,
		Indices:       indices,
	})
	if selectErr != nil {
		output.SelectionMetadata = buildEmojiMonitorMetadata(nil, nil, selectErr.Error())
		if errors.Is(selectErr, errEmojiVLMNotConfigured) || strings.Contains(selectErr.Error(), emojiVLMNotConfiguredMessage) {
			output.Message = emojiVLMNotConfiguredMessage
			return output
		}
		output.Message = "发送表情包时发生异常：" + selectErr.Error()
		return output
	}
	metadata := selection.selectionMetadata()
	sent, outcome := sendEmojiByStickerID(ctx, mgr, selection.Emoji.ID, output)
	sent.Reason = selection.Reason
	sent.SelectionMetadata = buildEmojiMonitorMetadata(metadata, &outcome, "")
	return sent
}

// sendEmojiByStickerID 发送指定表情
// 第一个返回值是工具结构化输出：成功时后三键保持空值，失败时回填真实描述与情绪标签
// 第二个返回值对齐 Python send_result：无论成败都带描述与情绪标签，供监控详情「表情发送结果」段使用
func sendEmojiByStickerID(ctx context.Context, mgr *emojisystem.Manager, emojiID uint, output sendEmojiOutput) (sendEmojiOutput, emojiSendOutcome) {
	emoji, err := mgr.GetByID(emojiID)
	if err != nil {
		message := "发送表情包时发生异常：" + err.Error()
		output.Message = message
		return output, emojiSendOutcome{Message: message}
	}
	emojisystem.TryDescribeIfEmpty(ctx, emoji.FileHash)
	sent, sendErr := sendStickerFunc(ctx, &SendStickerInput{StickerID: emojiID})
	if sendErr != nil {
		message := "发送表情包时发生异常：" + sendErr.Error()
		return sendEmojiOutputFromSelection(false, message, emoji, ""), emojiSendOutcomeFromEmoji(false, message, emoji)
	}
	if sent == nil || !sent.Success {
		message := "发送表情包失败。"
		if sent != nil && strings.TrimSpace(sent.Message) != "" {
			message = sent.Message
		}
		return sendEmojiOutputFromSelection(false, message, emoji, ""), emojiSendOutcomeFromEmoji(false, message, emoji)
	}
	output.Success = true
	output.Message = emojiSuccessMessage
	return output, emojiSendOutcomeFromEmoji(true, emojiSendResultMessage(emoji), emoji)
}

// emojiSendOutcomeFromEmoji 按 Python MaisakaEmojiSendResult 组装发送结果
func emojiSendOutcomeFromEmoji(success bool, message string, emoji *emojisystem.Emoji) emojiSendOutcome {
	outcome := emojiSendOutcome{Success: success, Message: message}
	if emoji == nil {
		return outcome
	}
	outcome.Description = strings.TrimSpace(emoji.Description)
	outcome.Emotions = append([]string{}, emoji.Emotions...)
	return outcome
}

// emojiSendResultMessage 对齐 maisaka_tool.py 成功分支的 send_result.message
func emojiSendResultMessage(emoji *emojisystem.Emoji) string {
	if emoji == nil {
		return "已发送表情包"
	}
	description := strings.TrimSpace(emoji.Description)
	if len(emoji.Emotions) > 0 {
		return "已发送表情包：" + description + "（情绪：" + strings.Join(emoji.Emotions, ", ") + "）"
	}
	return "已发送表情包：" + description
}

// sendEmojiOutputFromSelection 按 Python send_result 组装六键输出；成功时后三键保持空值
func sendEmojiOutputFromSelection(success bool, message string, emoji *emojisystem.Emoji, matchedEmotion string) sendEmojiOutput {
	output := sendEmojiOutput{Success: success, Message: message, Emotion: []string{}}
	if success || emoji == nil {
		return output
	}
	output.Description = emoji.Description
	output.Emotion = append([]string{}, emoji.Emotions...)
	output.MatchedEmotion = matchedEmotion
	return output
}

// emojiTargetExists 判断 msg_id 是否指向可回复的上下文消息
func emojiTargetExists(ctx context.Context, msgID string) bool {
	runtime := emojiToolRuntime(ctx)
	if runtime == nil {
		return false
	}
	_, ok := runtime.FindSourceMessageByID(msgID)
	return ok
}

// resolveAttachEmojiID 把 attach_emoji 序号解析为表情主键
// 先查当前工具上下文的候选表，再回落到历史表情候选消息的 emoji_hashes，支持跨轮引用
func resolveAttachEmojiID(ctx context.Context, index int) (uint, bool) {
	if index <= 0 {
		return 0, false
	}
	tc := GetToolContext(ctx)
	if tc != nil {
		if id := tc.EmojiCandidates[index]; id > 0 {
			return id, true
		}
	}
	runtime := emojiToolRuntime(ctx)
	if runtime == nil {
		return 0, false
	}
	history := runtime.History()
	for position := len(history) - 1; position >= 0; position-- {
		hashes := maisakaContextEmojiHashes(history[position])
		hash, ok := hashes[index]
		if !ok || strings.TrimSpace(hash) == "" {
			continue
		}
		mgr, err := emojiManagerForTool(ctx)
		if err != nil {
			return 0, false
		}
		emoji, err := mgr.GetByHash(ctx, hash)
		if err != nil || emoji == nil {
			return 0, false
		}
		return emoji.ID, true
	}
	return 0, false
}

// maisakaContextEmojiHashes 读取历史消息上的序号到 file_hash 映射
func maisakaContextEmojiHashes(message maisaka.ContextMessage) map[int]string {
	if message.Metadata == nil {
		return nil
	}
	switch value := message.Metadata["emoji_hashes"].(type) {
	case map[int]string:
		return value
	case map[string]any:
		hashes := make(map[int]string, len(value))
		for rawIndex, item := range value {
			index, err := strconv.Atoi(rawIndex)
			if err != nil {
				continue
			}
			if hash, ok := item.(string); ok {
				hashes[index] = hash
			}
		}
		return hashes
	}
	return nil
}

// decodeToolArguments 解析工具参数 JSON，空串按空对象处理
func decodeToolArguments(arguments string, target any) error {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	if err := sonic.UnmarshalString(trimmed, target); err != nil {
		return fmt.Errorf("工具参数解析失败: %w", err)
	}
	return nil
}

func NewSendEmojiTool() (tool.InvokableTool, error) {
	plain, err := utils.InferTool("send_emoji", "发送一个表情包来表达情绪，参与聊天。", func(ctx context.Context, input *SendEmojiInput) (*sendEmojiOutput, error) {
		output := sendEmojiCore(ctx, input.MsgID, 0)
		return &output, nil
	})
	if err != nil {
		return nil, err
	}
	info, err := plain.Info(context.Background())
	if err != nil {
		return nil, err
	}
	return &sendEmojiTool{info: info}, nil
}

func NewShowEmojiListTool() (tool.InvokableTool, error) {
	plain, err := utils.InferTool("show_emoji_list", "查看随机抽取的表情包选择拼图。图片会进入你的上下文，不会发送给聊天对象。调用 reply 时将选中图片的序号填入 attach_emoji；历史拼图的序号也可使用。", func(ctx context.Context, _ *struct{}) (*ShowEmojiListOutput, error) {
		output := showEmojiListCore(ctx)
		return &output, nil
	})
	if err != nil {
		return nil, err
	}
	info, err := plain.Info(context.Background())
	if err != nil {
		return nil, err
	}
	return &showEmojiListTool{info: info}, nil
}

// toolStreamID 对齐 maisaka_tool 的 stream_id：会话键 group:ID / private:ID
func toolStreamID(ctx context.Context) string {
	tc := GetToolContext(ctx)
	if tc == nil {
		return ""
	}
	return chatsession.ConversationKey(tc.ConversationKind, tc.TargetID)
}

// DescribeEmojiInput describe_emoji 参数，Go 侧扩展工具
type DescribeEmojiInput struct {
	FileHash string `json:"file_hash" jsonschema:"description=表情包 image_hash（images 表 image_type=EMOJI）"`
}

// DescribeEmojiOutput describe_emoji 结果
type DescribeEmojiOutput struct {
	Success     bool     `json:"success"`
	Description string   `json:"description,omitempty"`
	Emotions    []string `json:"emotions,omitempty"`
	Stored      bool     `json:"stored,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func describeEmojiFunc(ctx context.Context, input *DescribeEmojiInput) (*DescribeEmojiOutput, error) {
	if input == nil || strings.TrimSpace(input.FileHash) == "" {
		return &DescribeEmojiOutput{Success: false, Message: "请提供 file_hash"}, nil
	}
	result, err := emojisystem.Describe(ctx, input.FileHash)
	if err != nil {
		return &DescribeEmojiOutput{Success: false, Message: err.Error()}, nil
	}
	return &DescribeEmojiOutput{
		Success: true, Description: result.Description, Emotions: result.Emotions,
		Stored: result.Stored, Message: "已生成表情描述",
	}, nil
}

// NewDescribeEmojiTool 用 VLM 为本地表情包生成中文情绪标签并写入库
func NewDescribeEmojiTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"describe_emoji",
		"用 VLM 为本地表情包生成中文情绪标签并写入库。需配置 model_task_config.vlm 且 sticker 文件存在于 MAIBOT_STICKER_DIR。",
		describeEmojiFunc,
	)
}
