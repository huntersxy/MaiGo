package tools

import (
	"bytes"
	"context"
	"encoding/json"
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
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type maisakaStubOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	// Content 供 wait 等工具透传 Python content 文本
	Content string `json:"content,omitempty"`
	// ConsecutiveWaitCount 对齐 Python wait 的连续 wait 次数
	ConsecutiveWaitCount int `json:"consecutive_wait_count,omitempty"`
	// WaitSeconds 定时等待秒数；nil 表示无参数 wait 结束本轮
	WaitSeconds *int `json:"wait_seconds,omitempty"`
	// PlannerMessageSteering 对齐 chat.reply_timing.planner_message_steering
	PlannerMessageSteering bool `json:"planner_message_steering,omitempty"`
}

func maisakaNotImplemented(capability string) (*maisakaStubOutput, error) {
	return &maisakaStubOutput{
		Success: false,
		Message: fmt.Sprintf("%s 尚未在 MaiGo 实现；参见 internal/maisaka/builtin_catalog.go 中的 Go 对等工具", capability),
	}, nil
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
	MsgID string `json:"msg_id,omitempty" jsonschema:"description=要回复的消息 msg_id"`
	// AfterReply 回复成功后的行为：continue/wait
	AfterReply *string `json:"after_reply,omitempty" jsonschema:"description=回复发送成功后的行为：continue 继续思考和调用工具；wait（默认）暂停，未填 wait_seconds 则结束本轮，填写 wait_seconds 则定时再判断"`
	// WaitSeconds after_reply=wait 时的等待秒数；nil 表示结束本轮
	WaitSeconds *int `json:"wait_seconds,omitempty" jsonschema:"description=可选。after_reply=wait 时最多等待的秒数，到期后即使没有新消息也会继续思考。不填表示结束本轮。"`
	// WaitUntil 已移除的绝对时间参数，出现时直接拒绝
	WaitUntil        any                  `json:"wait_until,omitempty" jsonschema:"-"`
	ChatAnlyze       string               `json:"chat_anlyze,omitempty" jsonschema:"description=回复方向与上下文要点，不是正文"`
	ReplyStyle       string               `json:"reply_style,omitempty" jsonschema:"description=控制本次回复的篇幅和表达方式，当其他人回复都偏短时使用正常回复或者简短表达。当需要解释说明复杂问题时使用长回复，其他情况视情况决定。"`
	ExpressionIntent map[string]any       `json:"expression_intent,omitempty" jsonschema:"description=可选；给表达方式选择使用的结构化意图"`
	Content          string               `json:"content,omitempty" jsonschema:"-"`
	Text             string               `json:"text,omitempty" jsonschema:"-"`
	ReplyerGenerated bool                 `json:"_replyer_generated,omitempty" jsonschema:"-"`
	ReplyTo          int64                `json:"reply_to,omitempty" jsonschema:"-"`
	AttachAt         []ReplyAttachAt      `json:"attach_at,omitempty" jsonschema:"-"`
	AttachPic        []ReplyAttachPicture `json:"attach_pic,omitempty" jsonschema:"description=可选；随回复发送的图片列表"`
	AttachEmoji      any                  `json:"attach_emoji,omitempty" jsonschema:"description=可选；上下文表情引用或按情绪、画面内容匹配的表情"`
	AttachVoice      string               `json:"attach_voice,omitempty" jsonschema:"-"`
}

// ReplyAttachAt 对齐 Python attach_at：字符串为消息 ID，对象为平台用户 ID
type ReplyAttachAt struct {
	MsgID  string
	UserID string
}

func (t *ReplyAttachAt) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*t = ReplyAttachAt{}
		return nil
	}
	if data[0] == '"' {
		var msgID string
		if err := json.Unmarshal(data, &msgID); err != nil {
			return err
		}
		*t = ReplyAttachAt{MsgID: strings.TrimSpace(msgID)}
		return nil
	}
	var object struct {
		UserID    string `json:"user_id"`
		MsgID     string `json:"msg_id"`
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*t = ReplyAttachAt{
		UserID: strings.TrimSpace(object.UserID),
		MsgID:  strings.TrimSpace(maisakaFirstNonEmpty(object.MsgID, object.MessageID)),
	}
	return nil
}

// ReplyOutput reply 工具的输出，字段对齐 Python reply 的 content 与 structured_content
type ReplyOutput struct {
	Success               bool   `json:"success"`
	Message               string `json:"message,omitempty"`
	EmojiAttachmentStatus string `json:"emoji_attachment_status,omitempty"`
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
	// AfterReply 回复成功后的行为，对齐 reply 工具 after_reply
	AfterReply string `json:"after_reply,omitempty"`
	// WaitSeconds after_reply=wait 时的等待秒数；nil 表示结束本轮
	WaitSeconds *int `json:"wait_seconds,omitempty"`
	// ReplyExtensions 插件回复扩展参数，对齐 Python reply_metadata.reply_extensions
	ReplyExtensions map[string]any `json:"reply_extensions,omitempty"`
	// ReplyID 插件回复编号，对齐 Python reply_metadata.reply_id
	ReplyID string `json:"reply_id,omitempty"`
}

func replyFunc(ctx context.Context, input *ReplyInput) (any, error) {
	if input == nil {
		return nil, fmt.Errorf("reply 参数不能为空")
	}
	afterReply := "wait"
	if input.AfterReply != nil {
		afterReply = strings.TrimSpace(*input.AfterReply)
	}
	if afterReply != "continue" && afterReply != "wait" {
		return &maisakaStubOutput{Success: false, Message: "after_reply 必须为 continue 或 wait。"}, nil
	}
	if input.WaitUntil != nil {
		return &maisakaStubOutput{Success: false, Message: "不支持 wait_until，请使用 wait_seconds 指定等待秒数。"}, nil
	}
	if input.WaitSeconds != nil && *input.WaitSeconds < 0 {
		return &maisakaStubOutput{Success: false, Message: "wait_seconds 必须是非负整数"}, nil
	}
	if afterReply == "continue" && input.WaitSeconds != nil {
		return &maisakaStubOutput{Success: false, Message: "after_reply=continue 时不能填写等待时间。"}, nil
	}
	reference := strings.TrimSpace(input.ChatAnlyze)
	if reference == "" {
		return &maisakaStubOutput{
			Success: false,
			Message: "reply 工具必须提供非空字符串 `chat_anlyze`，请精炼转述当前对话局面与查证到的关键事实后重新调用。",
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
	// 部分适配器（如 QQ 官方机器人）的消息 ID 是字符串而非数字：此时无法用
	// 数字 reply_to 引用，按「不引用」发送，而不是让整条回复失败。
	msgID := strings.TrimSpace(input.MsgID)
	if replyTo == 0 && msgID != "" {
		zap.L().Debug("目标消息 ID 不是数字，按不引用发送",
			zap.String("plugin_msg_id", msgID))
	}
	tc := GetToolContext(ctx)
	botUserID := ""
	if tc != nil {
		if tc.ResolveBotAccount != nil {
			resolved, err := tc.ResolveBotAccount(ctx, tc.Platform, tc.AccountID)
			if err != nil {
				return &maisakaStubOutput{Success: false, Message: err.Error()}, nil
			}
			botUserID = strings.TrimSpace(resolved)
		}
		if botUserID == "" {
			return &maisakaStubOutput{
				Success: false,
				Message: "无法确定 Bot 发送账号，请为聊天流明确指定 account_id。",
			}, nil
		}
		tc.ReplyBotUserID = botUserID
	}
	if len(input.AttachAt) > 0 && (tc == nil || !tc.EnableReplyAt) {
		return &maisakaStubOutput{
			Success: false,
			Message: "回复时 @ 用户已关闭，不能使用 attach_at。",
		}, nil
	}
	var emojiAttachment *EmojiAttachment
	var emojiResolveErr error
	switch value := input.AttachEmoji.(type) {
	case float64:
		if value == 0 {
			input.AttachEmoji = nil
		}
	case int:
		if value == 0 {
			input.AttachEmoji = nil
		}
	}
	if input.AttachEmoji != nil {
		logger := zap.L().With(zap.Int64("reply_to", replyTo))
		if tc != nil {
			logger = logger.With(zap.String("session_id", tc.SessionID))
		}
		if tc == nil || tc.ResolveEmojiAttachment == nil {
			emojiResolveErr = fmt.Errorf("表情解析端口未接入")
			logger.Warn("attach_emoji resolution=failed", zap.Error(emojiResolveErr))
		} else if resolved, resolveErr := tc.ResolveEmojiAttachment(ctx, input.AttachEmoji); resolveErr != nil {
			emojiResolveErr = resolveErr
			logger.Warn("attach_emoji resolution=failed", zap.Error(resolveErr))
		} else {
			emojiAttachment = &resolved
			logger.Info("attach_emoji resolution=resolved", zap.String("emoji_hash", resolved.Hash))
		}
	}
	pictures, err := resolveReplyPictures(tc, input.AttachPic)
	if err != nil {
		return &maisakaStubOutput{Success: false, Message: err.Error()}, nil
	}
	var generatedMonitorDetail map[string]any
	var generatedPromptHTMLURI string
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
		content = strings.TrimSpace(generated.Text)
		generatedMonitorDetail = generated.MonitorDetail
		generatedPromptHTMLURI = generated.PromptHTMLURI
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
	mentions, err := resolveReplyAttachAtMentions(GetToolContext(ctx), input.AttachAt)
	if err != nil {
		return &maisakaStubOutput{Success: false, Message: err.Error()}, nil
	}
	voiceMode, voiceErr := resolveAttachVoiceMode(ctx, input.AttachVoice)
	if voiceErr != nil {
		return &maisakaStubOutput{Success: false, Message: voiceErr.Error()}, nil
	}
	if voiceMode == VoiceModeOnly {
		if err := sendReplyVoiceOnly(ctx, content, replyTo); err != nil {
			return &maisakaStubOutput{Success: false, Message: "生成语音附件失败：" + err.Error()}, nil
		}
		out := &ReplyOutput{
			Success: true, Message: "发言成功", Content: content, ReplyTo: replyTo,
			MsgID: strings.TrimSpace(input.MsgID), Sent: true, SetQuote: replyTo != 0, EffectiveSetQuote: replyTo != 0,
		}
		if tc != nil && tc.RecordReplyTarget != nil {
			tc.RecordReplyTarget(strconv.FormatInt(replyTo, 10), content)
		}
		return finishReplyAttachments(ctx, tc, out, replyTo, pictures, emojiAttachment, emojiResolveErr, afterReply, input.WaitSeconds)
	}
	out, outcome, err := sendReplyContent(replyer.WithPostProcessOptions(ctx, postProcess), content, replyTo, mentions)
	if err != nil {
		return nil, err
	}
	out.Content = content
	out.ReplyTo = replyTo
	out.MsgID = strings.TrimSpace(input.MsgID)
	if len(generatedMonitorDetail) > 0 {
		out.MonitorDetail = generatedMonitorDetail
	}
	if generatedPromptHTMLURI != "" {
		if out.MonitorDetail == nil {
			out.MonitorDetail = map[string]any{}
		}
		out.MonitorDetail["prompt_html_uri"] = generatedPromptHTMLURI
	}
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
	if voiceMode == VoiceModeWithText {
		if err := sendReplyVoiceWithText(ctx, content); err != nil {
			return &maisakaStubOutput{Success: false, Message: "生成语音附件失败：" + err.Error()}, nil
		}
	}
	return finishReplyAttachments(ctx, tc, out, replyTo, pictures, emojiAttachment, emojiResolveErr, afterReply, input.WaitSeconds)
}

func finishReplyAttachments(ctx context.Context, tc *ToolContext, out *ReplyOutput, replyTo int64, pictures []resolvedReplyPicture, emojiAttachment *EmojiAttachment, emojiResolveErr error, afterReply string, waitSeconds *int) (*ReplyOutput, error) {
	if tc != nil && tc.SendImageURLCallback != nil {
		attachmentCtx := WithReplyAnchor(ctx, replyTo)
		for _, picture := range pictures {
			if strings.TrimSpace(picture.Path) == "" {
				continue
			}
			if err := tc.SendImageURLCallback(attachmentCtx, picture.Path); err != nil {
				return nil, fmt.Errorf("发送回复图片失败: %w", err)
			}
		}
	}
	if emojiAttachment != nil {
		var sendErr error
		if tc == nil || tc.SendEmojiAttachment == nil {
			sendErr = fmt.Errorf("表情发送端口未接入")
		} else {
			sendErr = tc.SendEmojiAttachment(WithReplyAnchor(ctx, replyTo), tc.TargetID, *emojiAttachment)
		}
		if sendErr != nil {
			out.EmojiAttachmentStatus = "send_failed"
			out.Message = fmt.Sprintf("正文已发送，表情发送失败：%v", sendErr)
		} else {
			out.EmojiAttachmentStatus = "sent"
		}
	} else if emojiResolveErr != nil {
		out.EmojiAttachmentStatus = "resolution_failed"
		out.Message = fmt.Sprintf("正文已发送，表情解析失败：%v", emojiResolveErr)
	}
	out.AfterReply = afterReply
	if afterReply == "wait" {
		out.WaitSeconds = waitSeconds
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

// resolveReplyAttachAtMentions 对齐 Python _resolve_at_attachment：支持消息 ID 或 user_id 对象
func resolveReplyAttachAtMentions(tc *ToolContext, targets []ReplyAttachAt) ([]int64, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	mentions := make([]int64, 0, len(targets))
	for _, target := range targets {
		if userID := strings.TrimSpace(target.UserID); userID != "" {
			id, err := strconv.ParseInt(userID, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("无法解析 at 目标用户：user_id=%s", userID)
			}
			mentions = append(mentions, id)
			continue
		}
		msgID := strings.TrimSpace(target.MsgID)
		if msgID == "" {
			return nil, fmt.Errorf("无法解析 at 目标消息：msg_id=")
		}
		if tc == nil || tc.ResolveMessageSender == nil {
			return nil, fmt.Errorf("无法解析 at 目标消息：msg_id=%s", msgID)
		}
		senderID, ok := tc.ResolveMessageSender(msgID)
		if !ok || senderID <= 0 {
			return nil, fmt.Errorf("无法解析 at 目标消息：msg_id=%s", msgID)
		}
		mentions = append(mentions, senderID)
	}
	return mentions, nil
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
	if value := strings.TrimSpace(input.ChatAnlyze); value != "" {
		args["chat_anlyze"] = value
	}
	if value := strings.TrimSpace(input.ReplyStyle); value != "" {
		args["reply_style"] = value
	}
	if len(input.ExpressionIntent) > 0 {
		args["expression_intent"] = input.ExpressionIntent
	}
	if len(input.AttachAt) > 0 {
		attachAt := make([]any, 0, len(input.AttachAt))
		for _, target := range input.AttachAt {
			if userID := strings.TrimSpace(target.UserID); userID != "" {
				attachAt = append(attachAt, map[string]any{"user_id": userID})
				continue
			}
			if msgID := strings.TrimSpace(target.MsgID); msgID != "" {
				attachAt = append(attachAt, msgID)
			}
		}
		args["attach_at"] = attachAt
	}
	if input.AttachEmoji != nil {
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
		"根据当前思考生成并发送可见回复；必须提供对话分析 chat_anlyze，可选填写目标消息 msg_id。",
		replyFunc,
	)
}

// ==================== wait ====================

type WaitInput struct {
	Seconds *int `json:"seconds,omitempty" jsonschema:"description=等待秒数"`
}

func waitFunc(ctx context.Context, input *WaitInput) (*maisakaStubOutput, error) {
	var waitSeconds *int
	if input != nil {
		waitSeconds = input.Seconds
	}
	if waitSeconds != nil && *waitSeconds < 0 {
		return &maisakaStubOutput{Success: false, Message: "seconds 必须是非负整数"}, nil
	}
	if waitSeconds == nil {
		return &maisakaStubOutput{
			Success: true,
			Content: "已结束本轮思考，等待新消息。",
			Message: "已结束本轮思考，等待新消息。",
		}, nil
	}
	tc := GetToolContext(ctx)
	if tc == nil || (tc.BeginWait == nil && tc.BeginWaitCounted == nil) {
		return &maisakaStubOutput{
			Success: false,
			Message: fmt.Sprintf("wait(%d) 会话 wait 状态未接入 Agent", *waitSeconds),
		}, nil
	}
	count := 0
	if tc.BeginWaitCounted != nil {
		var err error
		count, err = tc.BeginWaitCounted(*waitSeconds)
		if err != nil {
			return nil, err
		}
	} else if err := tc.BeginWait(*waitSeconds); err != nil {
		return nil, err
	}
	detail := "当前对话循环进入等待状态，将固定等待 %d 秒；期间收到的新消息不会提前打断本次等待。"
	if tc.PlannerMessageSteering {
		detail = "当前对话循环进入等待状态，最多等待 %d 秒；新消息可提前结束等待并继续思考。"
	}
	content := fmt.Sprintf(detail, *waitSeconds) + fmt.Sprintf("连续 wait 次数：%d。", count)
	return &maisakaStubOutput{
		Success:                true,
		Content:                content,
		Message:                content,
		ConsecutiveWaitCount:   count,
		WaitSeconds:            waitSeconds,
		PlannerMessageSteering: tc.PlannerMessageSteering,
	}, nil
}

func NewWaitTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"wait",
		"暂停当前思考，不发送消息。不填 seconds 表示结束本轮，等待新消息；填写 seconds 表示定时等待，到期后即使没有新消息也会继续思考。定时等待可能被新消息提前唤醒。",
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
			Message: "没有找到匹配描述的表情包；若库中图片尚无 VLM 描述，请先调用 describe_emoji 生成描述",
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

	summary, traits := formatPersonProfileSummary(prof)
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
	Limit *int   `json:"limit,omitempty" jsonschema:"description=最多返回多少个工具，默认 5"`
}

type ToolSearchToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolSearchOutput struct {
	Success                  bool                       `json:"success"`
	Message                  string                     `json:"message,omitempty"`
	Query                    string                     `json:"query,omitempty"`
	MatchedToolNames         []string                   `json:"matched_tool_names"`
	NewlyDiscoveredToolNames []string                   `json:"newly_discovered_tool_names"`
	Status                   string                     `json:"status,omitempty"`
	Instruction              string                     `json:"instruction,omitempty"`
	Tools                    []ToolSearchToolDefinition `json:"tools"`
}

func toolSearchFunc(ctx context.Context, input *ToolSearchInput) (*ToolSearchOutput, error) {
	rawQuery := ""
	limit := 5
	if input != nil {
		rawQuery = strings.TrimSpace(input.Query)
		if input.Limit != nil {
			if *input.Limit < 1 {
				return &ToolSearchOutput{Success: false, Message: "tool_search 的 `limit` 必须是正整数。"}, nil
			}
			limit = *input.Limit
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
	matched := SearchToolCatalog(catalog, rawQuery, limit)
	matchedNames := make([]string, 0, len(matched))
	for _, entry := range matched {
		matchedNames = append(matchedNames, entry.Name)
	}

	newlyDiscovered := []string{}
	if tc != nil {
		newlyDiscovered = append(newlyDiscovered, tc.DiscoverDeferredTools(matchedNames)...)
	}
	structured := ToolSearchOutput{
		Success:                  true,
		Query:                    rawQuery,
		MatchedToolNames:         matchedNames,
		NewlyDiscoveredToolNames: newlyDiscovered,
		Status:                   "not_found",
		Instruction:              "未找到匹配工具，请换用工具名或其他关键词重新搜索。",
		Tools:                    make([]ToolSearchToolDefinition, 0, len(matched)),
	}
	if len(matched) > 0 {
		structured.Status = "loaded"
		structured.Instruction = "本次返回的工具定义已经加载，现在可直接按定义的名称和参数发起工具调用；无需等待顶层 tools 数组更新，也不要再次搜索已返回的工具。"
	}
	for _, entry := range matched {
		parameters := entry.Parameters
		if len(parameters) == 0 {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		structured.Tools = append(structured.Tools, ToolSearchToolDefinition{
			Name: entry.Name, Description: entry.Description, Parameters: parameters,
		})
	}
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
