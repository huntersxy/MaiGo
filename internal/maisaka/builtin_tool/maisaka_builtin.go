package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/maisaka"
	maisakactx "MaiGo/internal/maisaka/context"
	"MaiGo/internal/person_info"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

type maisakaStubOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	// Content 供 wait 等工具透传 Python content 文本
	Content string `json:"content,omitempty"`
	// ConsecutiveWaitCount 对齐 Python wait 的连续 wait 次数
	ConsecutiveWaitCount int `json:"consecutive_wait_count,omitempty"`
	// PlannerMessageSteering 对齐 chat.reply_timing.planner_message_steering
	PlannerMessageSteering bool `json:"planner_message_steering,omitempty"`
}

func maisakaNotImplemented(capability string) (*maisakaStubOutput, error) {
	return &maisakaStubOutput{
		Success: false,
		Message: fmt.Sprintf("%s 尚未在 MaiGo 实现；参见 internal/maisaka/builtin_catalog.go 中的 Go 对等工具", capability),
	}, nil
}

// ==================== stop（Maisaka）→ stayQuiet ====================

type StopInput struct {
	Reason string `json:"reason,omitempty" jsonschema:"description=结束思考的原因（笔记）"`
}

func stopFunc(ctx context.Context, input *StopInput) (*StayQuietOutput, error) {
	reason := ""
	if input != nil {
		reason = input.Reason
	}
	return stayQuietFunc(ctx, &StayQuietInput{Reason: reason})
}

func NewStopTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"stop",
		"结束本轮思考并等待新消息，与不调用任何工具时一致；无需继续行动时调用。",
		stopFunc,
	)
}

// ==================== reply（Maisaka）→ speak 桥接 ====================

type ReplyAttachPicture struct {
	MsgID      string `json:"msg_id,omitempty" jsonschema:"description=图片所在消息的 msg_id；与 media_index 二选一"`
	MediaIndex string `json:"media_index,omitempty" jsonschema:"description=工具结果媒体索引，例如 tool_result:call_x:1；与 msg_id 二选一"`
	Index      int    `json:"index,omitempty" jsonschema:"description=该消息或工具结果中的图片序号，从 0 开始"`
	// 以下别名只容忍调用方旧字段，不进入工具 schema，对齐 Python _resolve_image_attachment 的归一化
	MessageID  string `json:"message_id,omitempty" jsonschema:"-"`
	Source     string `json:"source,omitempty" jsonschema:"-"`
	ImageIndex *int   `json:"image_index,omitempty" jsonschema:"-"`
}

// pictureTarget 按 media_index → msg_id → message_id → source 的顺序取图片引用
// 顺序对齐 Python src/maisaka/builtin_tool/context.py:301-307
func (p ReplyAttachPicture) pictureTarget() string {
	for _, candidate := range []string{p.MediaIndex, p.MsgID, p.MessageID, p.Source} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// pictureIndex 对齐 Python raw_index = index or image_index
func (p ReplyAttachPicture) pictureIndex() int {
	if p.Index != 0 {
		return p.Index
	}
	if p.ImageIndex != nil {
		return *p.ImageIndex
	}
	return 0
}

type ReplyInput struct {
	MsgID            string               `json:"msg_id,omitempty" jsonschema:"description=要回复的消息 msg_id"`
	ReplyReference   string               `json:"reply_reference,omitempty" jsonschema:"description=回复方向与上下文要点，不是正文"`
	ReplyStyle       string               `json:"reply_style,omitempty" jsonschema:"description=可选；控制本次回复的篇幅和表达方式"`
	ExpressionIntent map[string]any       `json:"expression_intent,omitempty" jsonschema:"description=可选；给表达方式选择使用的结构化意图"`
	Content          string               `json:"content,omitempty" jsonschema:"description=内部桥接字段，模型不要填写；正文由回复生成器生成"`
	Text             string               `json:"text,omitempty" jsonschema:"description=内部桥接字段，模型不要填写"`
	ReplyerGenerated bool                 `json:"_replyer_generated,omitempty" jsonschema:"-"`
	ReplyTo          int64                `json:"reply_to,omitempty" jsonschema:"description=内部桥接字段，模型不要填写；引用目标由 msg_id 解析"`
	AttachAt         []string             `json:"attach_at,omitempty" jsonschema:"description=可选；随回复 at 的目标消息 msg_id 列表"`
	AttachPic        []ReplyAttachPicture `json:"attach_pic,omitempty" jsonschema:"description=可选；随回复发送的图片列表"`
	AttachEmoji      int                  `json:"attach_emoji,omitempty" jsonschema:"description=可选；show_emoji_list 返回的拼图序号，0 表示不发送"`
}

// ReplyOutput reply 工具的输出，字段对齐 Python reply 的 content 与 structured_content
type ReplyOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	// Content 本次实际发送的正文
	Content string `json:"content,omitempty"`
	// ReplyTo 本次回复引用的目标消息 ID
	ReplyTo int64 `json:"reply_to,omitempty"`
	// MsgID 本次回复引用的原始 msg_id 字符串
	MsgID string `json:"msg_id,omitempty"`
	// Sent 分段是否全部发送成功，用于区分「生成成功但发送失败」
	Sent bool `json:"sent"`
	// SetQuote 引用意图，对齐 Python structured_content.set_quote
	SetQuote bool `json:"set_quote"`
	// EffectiveSetQuote 首段实际生效的引用，对齐 Python effective_set_quote
	EffectiveSetQuote bool `json:"effective_set_quote"`
	// ReplySegments 逐段正文，对齐 Python reply_segments
	ReplySegments []string `json:"reply_segments"`
	// SendResults 逐段发送结果，对齐 Python send_results
	SendResults []ReplySendSegment `json:"send_results"`
	// TargetUserName 目标消息作者显示名，对齐 Python target_user_name
	TargetUserName string `json:"target_user_name"`
	// SendFailed 正文生成成功但发送失败，失败分支需要保留 structured_content
	SendFailed bool `json:"send_failed,omitempty"`
	// SentMessageIDs 已送达消息编号，对齐 Python reply_metadata.sent_message_ids
	SentMessageIDs []string `json:"sent_message_ids,omitempty"`
	// QuoteProbability 引用抽样概率，对齐 Python reply_metadata.quote_probability
	QuoteProbability *float64 `json:"quote_probability,omitempty"`
	// MonitorDetail 统一监控详情，对齐 Python reply_metadata.monitor_detail
	MonitorDetail map[string]any `json:"monitor_detail,omitempty"`
	// ReplySupplement 补充编排详情，对齐 Python reply_metadata.reply_supplement
	ReplySupplement map[string]any `json:"reply_supplement,omitempty"`
	// ReplyExtensions 插件回复扩展参数，对齐 Python reply_metadata.reply_extensions
	ReplyExtensions map[string]any `json:"reply_extensions,omitempty"`
	// ReplyID 插件回复编号，对齐 Python reply_metadata.reply_id
	ReplyID string `json:"reply_id,omitempty"`
}

func replyFunc(ctx context.Context, input *ReplyInput) (any, error) {
	if input == nil {
		return nil, fmt.Errorf("reply 参数不能为空")
	}
	reference := strings.TrimSpace(input.ReplyReference)
	if reference == "" {
		return &maisakaStubOutput{
			Success: false,
			Message: "reply 工具必须提供非空字符串 `reply_reference`，请填写本次回复的上下文关键信息与回复方向后重新调用。",
		}, nil
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		content = strings.TrimSpace(input.Text)
	}
	replyTo := input.ReplyTo
	if replyTo == 0 {
		replyTo = parseMessageIDAlias(input.MsgID)
	}
	if replyTo == 0 {
		return &maisakaStubOutput{
			Success: false,
			Message: "reply 工具需要提供有效的 `msg_id` 参数。",
		}, nil
	}
	tc := GetToolContext(ctx)
	if len(input.AttachAt) > 0 && (tc == nil || !tc.EnableReplyAt) {
		return &maisakaStubOutput{
			Success: false,
			Message: "回复时 @ 用户已关闭，不能使用 attach_at。",
		}, nil
	}
	if input.AttachEmoji < 0 {
		return &maisakaStubOutput{Success: false, Message: "attach_emoji 必须填写 show_emoji_list 拼图中的正整数序号。"}, nil
	}
	if input.AttachEmoji > 0 && (tc == nil || !tc.UseNewSendLogic) {
		return &maisakaStubOutput{Success: false, Message: "新表情包发送逻辑未开启，请使用 send_emoji 工具。"}, nil
	}
	pictures, err := resolveReplyPictures(tc, input.AttachPic)
	if err != nil {
		return &maisakaStubOutput{Success: false, Message: err.Error()}, nil
	}
	var emojiID uint
	if input.AttachEmoji > 0 {
		resolved, ok := resolveAttachEmojiID(ctx, input.AttachEmoji)
		if !ok {
			return &maisakaStubOutput{
				Success: false,
				Message: fmt.Sprintf("表情包序号 %d 不在当前历史中，请先调用 show_emoji_list。", input.AttachEmoji),
			}, nil
		}
		emojiID = resolved
	}
	if content == "" || !input.ReplyerGenerated {
		if tc == nil || tc.ReplyerCallback == nil {
			return &maisakaStubOutput{
				Success: false,
				Message: "Maisaka 回复生成器当前不可用。",
			}, nil
		}
		replyArgs := replyToolArguments(input)
		targetKey := strconv.FormatInt(replyTo, 10)
		if tc.PreviousTargetReply != nil {
			if previous := maisaka.NormalizeDuplicateReplyText(tc.PreviousTargetReply(targetKey)); previous != "" {
				replyArgs["_duplicate_target_reply_reminder"] = fmt.Sprintf(maisaka.DuplicateTargetReplyReminderTemplate, previous)
			}
		}
		generated, err := tc.ReplyerCallback(ctx, reference, strings.TrimSpace(input.MsgID), replyArgs)
		if err != nil {
			return &maisakaStubOutput{
				Success: false,
				Message: fmt.Sprintf("生成可见回复失败：%v", err),
			}, nil
		}
		content = strings.TrimSpace(generated)
		if content == "" {
			return &maisakaStubOutput{Success: false, Message: "生成可见回复失败。"}, nil
		}
	}
	content, postProcess, err := emitReplyBeforePostProcess(ctx, content, replyTo, input)
	if err != nil {
		return nil, err
	}
	if content == "" {
		return &maisakaStubOutput{
			Success: false,
			Message: "回复正文被 Hook 清空，未发送",
		}, nil
	}
	mentions := make([]int64, 0, len(input.AttachAt))
	if len(input.AttachAt) > 0 {
		if tc := GetToolContext(ctx); tc != nil && tc.ResolveMessageSender != nil {
			for _, raw := range input.AttachAt {
				if senderID, ok := tc.ResolveMessageSender(raw); ok && senderID > 0 {
					mentions = append(mentions, senderID)
				}
			}
		}
	}
	out, outcome, err := sendReplyContent(replyer.WithPostProcessOptions(ctx, postProcess), content, replyTo, mentions)
	if err != nil {
		return nil, err
	}
	out.Content = content
	out.ReplyTo = replyTo
	out.MsgID = strings.TrimSpace(input.MsgID)
	out.SetQuote = replyTo != 0
	out.EffectiveSetQuote = outcome.EffectiveSetQuote
	out.Sent = outcome.Sent
	if !outcome.Sent {
		// 对齐 Python reply 发送失败分支：正文已生成但发送失败时保留 structured_content
		out.Success = false
		out.Message = "可见回复生成成功，但发送失败。"
		out.SendFailed = true
		return out, nil
	}
	if targetUserName := resolveReplyTargetUserName(tc, out.MsgID, replyTo); targetUserName != "" {
		out.TargetUserName = targetUserName
	}
	if tc != nil && tc.RecordReplyTarget != nil {
		tc.RecordReplyTarget(strconv.FormatInt(replyTo, 10), content)
	}
	if tc != nil && tc.SendImageURLCallback != nil {
		for _, picture := range pictures {
			if strings.TrimSpace(picture.Path) == "" {
				continue
			}
			if err := tc.SendImageURLCallback(ctx, picture.Path); err != nil {
				return nil, fmt.Errorf("发送回复图片失败: %w", err)
			}
		}
	}
	if emojiID > 0 {
		emojiOutput := sendEmojiCore(ctx, "", emojiID)
		if !emojiOutput.Success {
			return nil, fmt.Errorf("发送回复表情失败: %s", maisakaFirstNonEmpty(emojiOutput.Message, "未知错误"))
		}
	}
	return out, nil
}

// sendReplyContent 发送可见回复；接入 ReplySendCallback 时保留逐段真实发送结果
func sendReplyContent(ctx context.Context, content string, replyTo int64, mentions []int64) (*ReplyOutput, ReplySendOutcome, error) {
	tc := GetToolContext(ctx)
	out := &ReplyOutput{Success: true, Message: "发言成功"}
	outcome := ReplySendOutcome{SetQuote: replyTo != 0, EffectiveSetQuote: replyTo != 0, Sent: true}
	if tc == nil || tc.ReplySendCallback == nil {
		if _, err := speakContent(ctx, content, replyTo, mentions); err != nil {
			return nil, ReplySendOutcome{}, err
		}
		out.ReplySegments = []string{content}
		out.SendResults = []ReplySendSegment{{Index: 0, Segment: content, SetQuote: replyTo != 0, Success: true}}
		return out, outcome, nil
	}
	result, err := tc.ReplySendCallback(ctx, tc.TargetID, content, replyTo, mentions)
	if err != nil {
		return nil, ReplySendOutcome{}, err
	}
	outcome = result
	out.SetQuote = replyTo != 0
	out.EffectiveSetQuote = result.EffectiveSetQuote
	out.Sent = result.Sent
	out.SendResults = result.Segments
	out.ReplySegments = make([]string, 0, len(result.Segments))
	// 对齐 Python reply.py:735-745：逐段收集已送达消息编号
	sentIDs := make([]string, 0, len(result.Segments))
	for _, segment := range result.Segments {
		out.ReplySegments = append(out.ReplySegments, segment.Segment)
		if segment.Success {
			if id := strings.TrimSpace(segment.MessageID); id != "" {
				sentIDs = append(sentIDs, id)
			}
		}
	}
	out.SentMessageIDs = sentIDs
	return out, outcome, nil
}

// resolveReplyTargetUserName 解析目标消息作者显示名：群名片优先，其次昵称，最后 user_id
func resolveReplyTargetUserName(tc *ToolContext, msgID string, replyTo int64) string {
	if tc == nil || tc.ResolveMessageAuthor == nil {
		return ""
	}
	candidates := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(msgID); trimmed != "" {
		candidates = append(candidates, trimmed)
	}
	if replyTo != 0 {
		candidates = append(candidates, strconv.FormatInt(replyTo, 10))
	}
	for _, raw := range candidates {
		author, ok := tc.ResolveMessageAuthor(raw)
		if !ok {
			continue
		}
		if author.IsSelf {
			return ""
		}
		return maisakaFirstNonEmpty(strings.TrimSpace(author.Cardname), strings.TrimSpace(author.Nickname), strings.TrimSpace(author.UserID))
	}
	return ""
}

type resolvedReplyPicture struct {
	Path        string
	Description string
}

// resolveReplyPictures 在发送正文前解析全部图片附件，任一失败则整条回复失败
// 字段归一化与错误文案对齐 Python _resolve_image_attachment 与 resolve_context_image
func resolveReplyPictures(tc *ToolContext, pictures []ReplyAttachPicture) ([]resolvedReplyPicture, error) {
	out := make([]resolvedReplyPicture, 0, len(pictures))
	for _, picture := range pictures {
		if tc == nil || tc.ResolvePicture == nil {
			return nil, fmt.Errorf("图片附件解析能力未初始化。")
		}
		target := picture.pictureTarget()
		index := picture.pictureIndex()
		path, description, err := tc.ResolvePicture(target, index)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("目标图片数据不可读取：%s，index=%d", maisakactx.ImageReference(target), index)
		}
		out = append(out, resolvedReplyPicture{Path: path, Description: description})
	}
	return out, nil
}

// replyToolArguments 透传 Replyer 可见的 reply 工具参数，忽略内部桥接字段
func replyToolArguments(input *ReplyInput) map[string]any {
	if input == nil {
		return nil
	}
	args := map[string]any{}
	if value := strings.TrimSpace(input.MsgID); value != "" {
		args["msg_id"] = value
	}
	if value := strings.TrimSpace(input.ReplyReference); value != "" {
		args["reply_reference"] = value
	}
	if value := strings.TrimSpace(input.ReplyStyle); value != "" {
		args["reply_style"] = value
	}
	if len(input.ExpressionIntent) > 0 {
		args["expression_intent"] = input.ExpressionIntent
	}
	if len(input.AttachAt) > 0 {
		args["attach_at"] = append([]string(nil), input.AttachAt...)
	}
	if input.AttachEmoji > 0 {
		args["attach_emoji"] = input.AttachEmoji
	}
	if len(input.AttachPic) > 0 {
		args["attach_pic"] = input.AttachPic
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

func parseMessageIDAlias(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return id
	}
	return 0
}

func NewReplyTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"reply",
		"根据当前思考生成并发送可见回复；必须提供目标消息 msg_id 与回复参考 reply_reference。",
		replyFunc,
	)
}

// ==================== wait ====================

type WaitInput struct {
	Seconds *int `json:"seconds,omitempty" jsonschema:"description=等待秒数"`
}

func waitFunc(ctx context.Context, input *WaitInput) (*maisakaStubOutput, error) {
	seconds := 30
	if input != nil && input.Seconds != nil {
		seconds = *input.Seconds
		if seconds < 0 {
			seconds = 0
		}
	}
	tc := GetToolContext(ctx)
	if tc == nil || (tc.BeginWait == nil && tc.BeginWaitCounted == nil) {
		return &maisakaStubOutput{
			Success: false,
			Message: fmt.Sprintf("wait(%d) 会话 wait 状态未接入 Agent", seconds),
		}, nil
	}
	count := 0
	if tc.BeginWaitCounted != nil {
		var err error
		count, err = tc.BeginWaitCounted(seconds)
		if err != nil {
			return nil, err
		}
	} else if err := tc.BeginWait(seconds); err != nil {
		return nil, err
	}
	detail := "当前对话循环进入等待状态，将固定等待 %d 秒；期间收到的新消息不会提前打断本次等待。"
	if tc.PlannerMessageSteering {
		detail = "当前对话循环进入等待状态，最多等待 %d 秒；新消息可提前结束等待并继续思考。"
	}
	content := fmt.Sprintf(detail, seconds) + fmt.Sprintf("连续 wait 次数：%d。", count)
	return &maisakaStubOutput{
		Success:                true,
		Content:                content,
		Message:                content,
		ConsecutiveWaitCount:   count,
		PlannerMessageSteering: tc.PlannerMessageSteering,
	}, nil
}

func NewWaitTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"wait",
		"等待一段时间；期间不查看聊天，用于一段时间后再次查看聊天内容。不适用于正在进行的聊天。",
		waitFunc,
	)
}

// ==================== query_image_memory / query_person_profile ====================

type QueryImageMemoryInput struct {
	MessageID     string `json:"message_id,omitempty" jsonschema:"description=包含目标图片的消息 ID"`
	ComponentPath string `json:"component_path,omitempty" jsonschema:"description=消息内图片组件路径"`
	Keyword       string `json:"keyword,omitempty" jsonschema:"description=兼容旧参数：描述或情绪关键词，搜索已收藏表情包"`
	Query         string `json:"query,omitempty" jsonschema:"description=同 keyword"`
	Limit         int    `json:"limit,omitempty" jsonschema:"description=最多返回条数，默认 8，最大 20"`
}

type QueryImageMemoryHit struct {
	ID              uint     `json:"id,omitempty"`
	Description     string   `json:"description,omitempty"`
	UseCount        int      `json:"use_count,omitempty"`
	MatchKind       string   `json:"match_kind,omitempty"`
	Similarity      float64  `json:"similarity,omitempty"`
	Observations    []string `json:"observations,omitempty"`
	RelatedMemories []string `json:"related_memories,omitempty"`
}

type QueryImageMemoryOutput struct {
	Success bool                  `json:"success"`
	Message string                `json:"message,omitempty"`
	Keyword string                `json:"keyword,omitempty"`
	Hits    []QueryImageMemoryHit `json:"hits,omitempty"`
	Note    string                `json:"note,omitempty"`
}

func queryImageMemoryFunc(ctx context.Context, input *QueryImageMemoryInput) (*QueryImageMemoryOutput, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil {
		return &QueryImageMemoryOutput{
			Success: false,
			Message: "工具上下文或记忆管理器未初始化",
		}, nil
	}
	keyword := ""
	limit := 8
	if input != nil {
		keyword = strings.TrimSpace(input.Keyword)
		if keyword == "" {
			keyword = strings.TrimSpace(input.Query)
		}
		if input.Limit > 0 {
			limit = input.Limit
		}
	}
	if limit > 20 {
		limit = 20
	}
	messageID, componentPath := "", ""
	if input != nil {
		messageID = strings.TrimSpace(input.MessageID)
		componentPath = strings.TrimSpace(input.ComponentPath)
	}
	if messageID != "" || componentPath != "" {
		if messageID == "" {
			return &QueryImageMemoryOutput{Success: false, Message: "查询图片记忆工具需要提供有效的 message_id 参数。"}, nil
		}
		if componentPath == "" {
			return &QueryImageMemoryOutput{Success: false, Message: "查询图片记忆工具需要提供有效的 component_path 参数。"}, nil
		}
		if tc.ImageMemorySearch == nil {
			return &QueryImageMemoryOutput{Success: false, Message: "A_Memorix 图片记忆服务未接线。"}, nil
		}
		result, err := tc.ImageMemorySearch(ctx, messageID, componentPath, limit)
		if err != nil {
			return &QueryImageMemoryOutput{Success: false, Message: err.Error()}, nil
		}
		return formatImageMemoryResult(result, messageID, componentPath), nil
	}
	if keyword == "" {
		return &QueryImageMemoryOutput{
			Success: false,
			Message: "query_image_memory 需要提供 message_id 与 component_path，或兼容参数 keyword/query。",
		}, nil
	}

	page, err := tc.MemoryMgr.SearchEmojiPage(ctx, keyword, "", limit)
	if err != nil {
		return nil, fmt.Errorf("图片记忆检索失败: %w", err)
	}

	note := "Bridge：仅搜索 images 表（EMOJI）description 文本；无 A_Memorix 聊天图片 VLM/向量索引，空描述需 describe_emoji/VLM 打标后才可命中"
	if len(page.Items) == 0 {
		return &QueryImageMemoryOutput{
			Success: true,
			Keyword: keyword,
			Message: "没有找到匹配描述的表情包；若库中图片尚无 VLM 描述，请先 describe_emoji 或 send_emoji 触发打标",
			Note:    note,
		}, nil
	}

	hits := make([]QueryImageMemoryHit, 0, len(page.Items))
	lines := make([]string, 0, len(page.Items))
	for i, item := range page.Items {
		hits = append(hits, QueryImageMemoryHit{
			ID:          item.ID,
			Description: item.Description,
			UseCount:    item.QueryCount,
		})
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = "(无描述)"
		}
		lines = append(lines, fmt.Sprintf("%d. id=%d 描述：%s", i+1, item.ID, desc))
	}

	return &QueryImageMemoryOutput{
		Success: true,
		Keyword: keyword,
		Hits:    hits,
		Message: strings.Join(lines, "\n"),
		Note:    note,
	}, nil
}

// formatImageMemoryResult 把 A_Memorix image_memory 返回整理成工具输出
func formatImageMemoryResult(result map[string]any, messageID, componentPath string) *QueryImageMemoryOutput {
	output := &QueryImageMemoryOutput{Success: true, Keyword: messageID, Note: "A_Memorix 图片记忆：" + componentPath}
	if result == nil {
		output.Message = "没有找到指定图片的记忆。"
		return output
	}
	if success, ok := result["success"].(bool); ok && !success {
		output.Success = false
		if message, ok := result["error"].(string); ok && strings.TrimSpace(message) != "" {
			output.Message = message
		} else {
			output.Message = "图片记忆检索失败。"
		}
		return output
	}
	rawHits, _ := result["hits"].([]any)
	lines := make([]string, 0, len(rawHits))
	for index, raw := range rawHits {
		hit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := "相似图片"
		if text, _ := hit["match_kind"].(string); text == "exact_hash" {
			kind = "同一图片"
		}
		similarity := numberValue(hit["similarity"])
		lines = append(lines, fmt.Sprintf("%d. %s，相似度 %.3f", index+1, kind, similarity))
		observations := make([]string, 0)
		if items, ok := hit["observations"].([]any); ok {
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, _ := entry["text"].(string); strings.TrimSpace(text) != "" {
					observations = append(observations, strings.TrimSpace(text))
					lines = append(lines, "   图片认知："+strings.TrimSpace(text))
				}
			}
		}
		related := make([]string, 0)
		if items, ok := hit["related_memories"].([]any); ok {
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, _ := entry["content"].(string); strings.TrimSpace(text) != "" {
					related = append(related, strings.TrimSpace(text))
					lines = append(lines, "   关联记忆："+strings.TrimSpace(text))
				}
			}
		}
		output.Hits = append(output.Hits, QueryImageMemoryHit{
			MatchKind: kind, Similarity: similarity, Observations: observations, RelatedMemories: related,
		})
	}
	if len(lines) == 0 {
		output.Message = "没有找到指定图片的记忆。"
		return output
	}
	output.Message = strings.Join(lines, "\n")
	return output
}

func numberValue(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func NewQueryImageMemoryTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"query_image_memory",
		"检索图片相关记忆。Bridge 阶段按 keyword 搜索已收藏表情包描述；非 MaiBot 同图向量记忆。",
		queryImageMemoryFunc,
	)
}

type QueryPersonProfileInput struct {
	PersonID   string `json:"person_id,omitempty"`
	PersonName string `json:"person_name,omitempty"`
	Query      string `json:"query,omitempty" jsonschema:"description=兼容字段，同 person_name"`
	Limit      int    `json:"limit,omitempty"`
}

type QueryPersonProfileOutput struct {
	Success             bool                                   `json:"success"`
	Message             string                                 `json:"message,omitempty"`
	Summary             string                                 `json:"summary,omitempty"`
	Traits              []string                               `json:"traits,omitempty"`
	PersonID            string                                 `json:"person_id,omitempty"`
	PersonName          string                                 `json:"person_name,omitempty"`
	ProfileSource       string                                 `json:"profile_source,omitempty"`
	HasManualOverride   bool                                   `json:"has_manual_override"`
	FromCache           bool                                   `json:"from_cache,omitempty"`
	ProfileVersion      int64                                  `json:"profile_version,omitempty"`
	ProfileText         string                                 `json:"profile_text,omitempty"`
	Evidence            []personinfo.ProfileEvidence           `json:"evidence,omitempty"`
	UncertainCandidates []personinfo.ProfileUncertainCandidate `json:"uncertain_candidates,omitempty"`
	Limit               int                                    `json:"limit,omitempty"`
	Profile             *personinfo.Profile                    `json:"profile,omitempty"`
}

func queryPersonProfileFunc(ctx context.Context, input *QueryPersonProfileInput) (*QueryPersonProfileOutput, error) {
	if input == nil {
		return nil, fmt.Errorf("query_person_profile 参数不能为空")
	}
	personID := strings.TrimSpace(input.PersonID)
	personName := strings.TrimSpace(input.PersonName)
	if personName == "" {
		personName = strings.TrimSpace(input.Query)
	}
	if personID == "" && personName == "" {
		return nil, fmt.Errorf("query_person_profile 需要提供 person_id 或 person_name 中的一个")
	}

	limit := normalizePersonProfileLimit(input.Limit)
	reg := personinfo.DefaultRegistry()

	if personID == "" {
		platform := ""
		if tc := GetToolContext(ctx); tc != nil {
			platform = tc.Platform
		}
		resolved := reg.ResolvePersonID(ctx, personPlatform(platform), "", personName, true)
		if resolved == "" {
			// person_info 只做唯一精确匹配；别名由 Memorix resolve_identity 补齐
			resolved = reg.ResolveAliasPersonID(ctx, personName)
		}
		if resolved == "" {
			return &QueryPersonProfileOutput{
				Success: false,
				Message: fmt.Sprintf("未找到或无法唯一确认人物 %q", personName),
				Limit:   limit,
			}, nil
		}
		personID = resolved
	}

	snapshot, profileErr := reg.QueryProfile(ctx, personinfo.ProfileQuery{
		PersonID: personID, PersonKeyword: personName, Limit: limit,
	})
	if profileErr != nil || snapshot == nil || !snapshot.Success {
		// Host 已就绪时，画像查询错误或 payload success=false 都必须按 Python
		// memory_service.profile_admin 与 query_person_profile 的失败结果返回；
		// 仅 Host 未启用/未就绪时保留旧 PG 直查，避免人物资料整体不可用
		if message, ready := personProfileHostFailureMessage(ctx); ready {
			message = personProfileFailureMessage(snapshot, profileErr, message)
			return &QueryPersonProfileOutput{
				Success:    false,
				Message:    message,
				PersonID:   personID,
				PersonName: personName,
				Limit:      limit,
			}, nil
		}
	}
	if profileErr == nil && snapshot != nil && snapshot.Success {
		summary := strings.TrimSpace(snapshot.ProfileText)
		if summary == "" {
			summary = strings.TrimSpace(snapshot.Summary)
		}
		if summary == "" {
			summary = "暂无人物画像"
		}
		traits := snapshot.Traits
		if len(traits) == 0 {
			traits = personinfo.ProfileTraits(summary, limit)
		}
		displayName := snapshot.PersonName
		if displayName == "" {
			displayName = personName
		}
		if displayName == "" {
			displayName = snapshot.PersonID
		}
		out := &QueryPersonProfileOutput{
			Success:             true,
			Summary:             summary,
			Message:             summary,
			Traits:              traits,
			PersonID:            snapshot.PersonID,
			PersonName:          displayName,
			ProfileSource:       snapshot.ProfileSource,
			HasManualOverride:   snapshot.HasManualOverride,
			FromCache:           snapshot.FromCache,
			ProfileVersion:      snapshot.ProfileVersion,
			ProfileText:         snapshot.ProfileText,
			Evidence:            snapshot.Evidence,
			UncertainCandidates: snapshot.UncertainCandidates,
			Limit:               limit,
		}
		if out.PersonID == "" {
			out.PersonID = personID
		}
		if out.ProfileSource == "" {
			out.ProfileSource = "memorix_profile"
		}
		if prof, err := reg.GetProfile(ctx, out.PersonID); err == nil {
			out.Profile = prof
			if out.PersonName == "" {
				out.PersonName = prof.PersonName
			}
		}
		return out, nil
	}

	prof, err := reg.GetProfile(ctx, personID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			message := "未找到人物画像"
			if profileErr == nil && snapshot != nil && snapshot.Error != "" {
				message = snapshot.Error
			}
			return &QueryPersonProfileOutput{
				Success:    false,
				Message:    message,
				PersonID:   personID,
				PersonName: personName,
				Limit:      limit,
			}, nil
		}
		return nil, fmt.Errorf("人物画像查询失败: %w", err)
	}

	summary, traits := formatPersonProfileSummary(prof, limit)
	displayName := prof.PersonName
	if displayName == "" {
		displayName = prof.Nickname
	}
	if personName == "" {
		personName = displayName
	}

	return &QueryPersonProfileOutput{
		Success:           true,
		Summary:           summary,
		Message:           summary,
		Traits:            traits,
		PersonID:          prof.PersonID,
		PersonName:        personName,
		ProfileSource:     "person_info_pg",
		HasManualOverride: false,
		Limit:             limit,
		Profile:           prof,
	}, nil
}

// personProfileHostFailureMessage 判断画像错误是否应上抛
// 未启用（disabled）时返回 false，保留 Go-only 的 PG 直查回退；
// 初始化失败时优先返回真实 startup_error；Host 已就绪时返回空消息表示使用调用错误
func personProfileHostFailureMessage(ctx context.Context) (string, bool) {
	host := memorix.DefaultRegistry().Host()
	if host == nil {
		return "", false
	}
	health, err := host.Health(ctx)
	if err != nil {
		return "", false
	}
	if health.Disabled {
		return "", false
	}
	if health.StartupState == "failed" {
		if message := strings.TrimSpace(health.Error); message != "" {
			return message, true
		}
		return "", true
	}
	if health.MemoryEnabled {
		return "", true
	}
	return "", false
}

// personProfileFailureMessage 按 Python query_person_profile.py:112-140 选择失败文案
// Host 初始化错误优先，其次 profile 调用错误或 payload.error，最后是 Python 的未找到文案
func personProfileFailureMessage(snapshot *personinfo.ProfileSnapshot, profileErr error, hostMessage string) string {
	if message := strings.TrimSpace(hostMessage); message != "" {
		return message
	}
	if profileErr != nil {
		return profileErr.Error()
	}
	if snapshot != nil && snapshot.Raw != nil {
		if value, ok := snapshot.Raw["error"].(string); ok {
			if message := strings.TrimSpace(value); message != "" {
				return message
			}
		}
	}
	return "未找到人物画像。"
}

// queryPersonProfileTool 同时实现 eino InvokableTool 与结构化结果接口
type queryPersonProfileTool struct {
	info *schema.ToolInfo
}

// Info 返回工具声明
func (t *queryPersonProfileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

// InvokableRun 供 eino ToolsNode 直接调用，失败时以错误暴露
func (t *queryPersonProfileTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	result, err := t.InvokeStructuredToolResult(ctx, argumentsInJSON)
	if err != nil {
		return "", err
	}
	if !result.Success {
		if strings.TrimSpace(result.ErrorMessage) == "" {
			return "", fmt.Errorf("query_person_profile 执行失败")
		}
		return "", fmt.Errorf("%s", result.ErrorMessage)
	}
	return result.Content, nil
}

// InvokeStructuredToolResult 返回 Python ToolExecutionResult 对应的结构化结果
// 失败时 ErrorMessage 与 structured_content.success=false 同时保留，供 task-46 通道落库
func (t *queryPersonProfileTool) InvokeStructuredToolResult(ctx context.Context, argumentsInJSON string) (StructuredToolResult, error) {
	input := &QueryPersonProfileInput{}
	if strings.TrimSpace(argumentsInJSON) != "" {
		if err := sonic.UnmarshalString(argumentsInJSON, input); err != nil {
			return StructuredToolResult{}, fmt.Errorf("query_person_profile 参数解析失败: %w", err)
		}
	}
	out, err := queryPersonProfileFunc(ctx, input)
	if err != nil {
		return StructuredToolResult{}, err
	}
	raw, err := sonic.Marshal(out)
	if err != nil {
		return StructuredToolResult{}, fmt.Errorf("query_person_profile 结果序列化失败: %w", err)
	}
	structured := map[string]any{}
	if err := sonic.Unmarshal(raw, &structured); err != nil {
		return StructuredToolResult{}, fmt.Errorf("query_person_profile 结果解析失败: %w", err)
	}
	if !out.Success {
		message := strings.TrimSpace(out.Message)
		if message == "" {
			message = "未找到人物画像。"
		}
		return StructuredToolResult{Success: false, ErrorMessage: message, StructuredContent: structured}, nil
	}
	return StructuredToolResult{Success: true, Content: string(raw), StructuredContent: structured}, nil
}

func NewQueryPersonProfileTool() (tool.InvokableTool, error) {
	plain, err := utils.InferTool(
		"query_person_profile",
		"查询人物画像。当需要查询与某人的关系时使用。",
		queryPersonProfileFunc,
	)
	if err != nil {
		return nil, err
	}
	info, err := plain.Info(context.Background())
	if err != nil {
		return nil, err
	}
	return &queryPersonProfileTool{info: info}, nil
}

// ==================== view_forward_message → readForward ====================

type ViewForwardMessageInput struct {
	MsgID string `json:"msg_id" jsonschema:"description=转发消息的 msg_id"`
	Path  []int  `json:"path,omitempty" jsonschema:"description=嵌套转发路径；首次查看可省略"`
}

func viewForwardMessageFunc(ctx context.Context, input *ViewForwardMessageInput) (any, error) {
	if input == nil || strings.TrimSpace(input.MsgID) == "" {
		return nil, fmt.Errorf("查看转发消息工具需要提供有效的 `msg_id` 参数。")
	}
	for _, index := range input.Path {
		if index < 0 {
			return nil, fmt.Errorf("查看转发消息工具的 `path` 必须是由非负整数组成的数组。")
		}
	}
	tc := GetToolContext(ctx)
	if tc == nil || tc.ReadForwardCallback == nil {
		return nil, NewTerminalToolError(fmt.Errorf("合并转发读取能力未初始化"))
	}
	id := strings.TrimSpace(input.MsgID)
	if len(input.Path) == 0 {
		return tc.ReadForwardCallback(ctx, id, -1)
	}
	var current any
	for depth, index := range input.Path {
		result, err := tc.ReadForwardCallback(ctx, id, index)
		if err != nil {
			return nil, err
		}
		current = result
		if depth == len(input.Path)-1 {
			break
		}
		nested := readForwardNested(result)
		if len(nested) == 0 {
			return nil, fmt.Errorf("合并转发 %s 的第 %d 条消息没有嵌套转发", id, index)
		}
		id = nested[0]
	}
	if output, ok := current.(*ReadForwardOutput); ok {
		output.Path = append([]int(nil), input.Path...)
	}
	return current, nil
}

// readForwardNested 提取一次转发读取结果里的嵌套转发句柄
func readForwardNested(result any) []string {
	switch value := result.(type) {
	case *ReadForwardOutput:
		return value.Nested
	case ReadForwardOutput:
		return value.Nested
	case map[string]any:
		if raw, ok := value["nested_forward"]; ok {
			return stringSliceValue(raw)
		}
	}
	return nil
}

func stringSliceValue(raw any) []string {
	switch value := raw.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

func NewViewForwardMessageTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"view_forward_message",
		"根据 msg_id 逐层查看合并转发消息。首次调用只展开顶层；遇到嵌套转发时，使用返回内容中的 path 再次调用以继续展开。",
		viewForwardMessageFunc,
	)
}

// ==================== tool_search / subagent ====================

type ToolSearchInput struct {
	Query string `json:"query" jsonschema:"description=工具名、前缀或关键词"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=最多返回多少个工具，默认 5"`
}

type ToolSearchMatch struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Deferred    bool   `json:"deferred,omitempty"`
}

type ToolSearchOutput struct {
	Success                  bool              `json:"success"`
	Message                  string            `json:"message,omitempty"`
	Query                    string            `json:"query,omitempty"`
	MatchedToolNames         []string          `json:"matched_tool_names,omitempty"`
	NewlyDiscoveredToolNames []string          `json:"newly_discovered_tool_names,omitempty"`
	Matches                  []ToolSearchMatch `json:"matches,omitempty"`
	Note                     string            `json:"note,omitempty"`
}

func toolSearchFunc(ctx context.Context, input *ToolSearchInput) (*ToolSearchOutput, error) {
	rawQuery := ""
	limit := 5
	if input != nil {
		rawQuery = strings.TrimSpace(input.Query)
		if input.Limit > 0 {
			limit = input.Limit
		}
	}
	if rawQuery == "" {
		return &ToolSearchOutput{
			Success: false,
			Message: "tool_search 需要提供非空的 query 字符串参数",
		}, nil
	}

	tc := GetToolContext(ctx)
	catalog := catalogForToolSearch(tc)
	if len(catalog) == 0 {
		return &ToolSearchOutput{
			Success: true,
			Query:   rawQuery,
			Message: "未找到匹配的 deferred tools，请尝试更完整的工具名、前缀或其他关键词。",
		}, nil
	}

	matched := SearchToolCatalog(catalog, rawQuery, limit)
	matchedNames := make([]string, 0, len(matched))
	matches := make([]ToolSearchMatch, 0, len(matched))
	for _, entry := range matched {
		matchedNames = append(matchedNames, entry.Name)
		matches = append(matches, ToolSearchMatch{
			Name:        entry.Name,
			Description: entry.Description,
			Deferred:    entry.Deferred,
		})
	}

	var newlyDiscovered []string
	if tc != nil {
		newlyDiscovered = tc.DiscoverDeferredTools(matchedNames)
	}

	note := "按当前会话 deferred tools 检索；命中的工具会在后续模型轮次加入可用工具列表"
	structured := ToolSearchOutput{
		Success:                  true,
		Query:                    rawQuery,
		MatchedToolNames:         matchedNames,
		NewlyDiscoveredToolNames: newlyDiscovered,
		Matches:                  matches,
		Note:                     note,
	}

	if len(matchedNames) == 0 {
		structured.Message = "未找到匹配的 deferred tools，请尝试更完整的工具名、前缀或其他关键词。"
		return &structured, nil
	}

	newSet := map[string]struct{}{}
	for _, name := range newlyDiscovered {
		newSet[name] = struct{}{}
	}
	lines := []string{
		fmt.Sprintf("已找到 %d 个 deferred tools，它们会在后续轮次中加入可用工具列表：", len(matchedNames)),
	}
	for _, entry := range matched {
		suffix := ""
		if entry.Deferred {
			if _, fresh := newSet[entry.Name]; fresh {
				suffix = "（本次新发现）"
			} else {
				suffix = "（此前已发现）"
			}
		}
		lines = append(lines, fmt.Sprintf("- %s%s", entry.Name, suffix))
	}
	structured.Message = strings.Join(lines, "\n")
	return &structured, nil
}

func catalogForToolSearch(tc *ToolContext) []ToolCatalogEntry {
	if tc == nil || len(tc.ToolCatalog) == 0 {
		return nil
	}
	out := make([]ToolCatalogEntry, 0, len(tc.ToolCatalog))
	for _, entry := range tc.ToolCatalog {
		if entry.Deferred {
			out = append(out, entry)
		}
	}
	return out
}

func NewToolSearchTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"tool_search",
		"在 deferred tools 列表中按名称或关键词搜索工具，并将命中的工具加入后续轮次的可用工具列表。",
		toolSearchFunc,
	)
}

type SubagentInput struct {
	Task  string   `json:"task" jsonschema:"description=完整、自包含的任务说明"`
	Tools []string `json:"tools,omitempty" jsonschema:"description=授权子代理使用的工具名；骨架阶段可不填，仅记录意图"`
}

func NewSubagentTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"subagent",
		"派发子代理执行自包含任务；当前为后台单次模型调用骨架，结论通过 subagent_status 查看。",
		subagentFunc,
	)
}

func NewSubagentStatusTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"subagent_status",
		"查看子代理任务状态；不填 task_id 时列出全部任务。",
		subagentStatusFunc,
	)
}

type SubagentStopInput struct {
	TaskID string `json:"task_id" jsonschema:"description=要终止的任务 id，例如 subagent-1"`
}

func NewSubagentStopTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"subagent_stop",
		"终止仍在运行的子代理任务。",
		subagentStopFunc,
	)
}
