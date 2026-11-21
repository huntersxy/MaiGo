package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"strconv"

	"MaiGo/internal/chat/debounce"
	heartflow "MaiGo/internal/chat/heart_flow"
	messagereceive "MaiGo/internal/chat/message_receive"
	chatutils "MaiGo/internal/chat/utils"
	"MaiGo/internal/common/database"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/core/announcement"
	"MaiGo/internal/learners"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"
	maisakactx "MaiGo/internal/maisaka/context"
	"MaiGo/internal/maisaka/focus"
	"MaiGo/internal/maisaka/monitor"
	"MaiGo/internal/mcp_module"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// AgentDebouncer 把 Agent 的消息防抖状态适配为 maisaka.Debouncer
type AgentDebouncer struct {
	Schedule *conversationTurnScheduleStore
	Kind     string
	TargetID int64
}

// NewAgentDebouncer 创建会话防抖端口
func NewAgentDebouncer(a *Agent, kind string, targetID int64) *AgentDebouncer {
	if a == nil {
		return nil
	}
	return &AgentDebouncer{Schedule: a.turnSchedule, Kind: kind, TargetID: targetID}
}

// Remaining 距离本批消息收齐还需等待的时长
func (d *AgentDebouncer) Remaining(now time.Time) time.Duration {
	if d == nil || d.Schedule == nil {
		return 0
	}
	return d.Schedule.debounceRemaining(d.Kind, d.TargetID, now)
}

// SyncMode 同步防抖档位
func (d *AgentDebouncer) SyncMode(mode string) {
	if d == nil || d.Schedule == nil {
		return
	}
	d.Schedule.syncDebounceMode(d.Kind, d.TargetID, debounce.NormalizeMode(mode))
}

// Consume 本批消息已收走
func (d *AgentDebouncer) Consume() {
	if d == nil || d.Schedule == nil {
		return
	}
	d.Schedule.consumeDebounce(d.Kind, d.TargetID)
}

// Observe 记录一条入站消息并返回规则说明
func (d *AgentDebouncer) Observe(in maisaka.DebounceObserve) string {
	if d == nil || d.Schedule == nil {
		return ""
	}
	return d.Schedule.observeDebounce(d.Kind, d.TargetID, debounce.ObserveInput{
		Author:      in.Author,
		IsBareImage: in.IsBareImage,
		IsPureEmoji: in.IsPureEmoji,
		Mode:        debounce.NormalizeMode(in.Mode),
		AtBot:       in.AtBot,
		ReceivedAt:  in.ReceivedAt,
		WallTime:    in.WallTime,
		Now:         in.Now,
	})
}

// Deadline 当前批次截止时间
func (d *AgentDebouncer) Deadline() (time.Time, bool) {
	if d == nil || d.Schedule == nil {
		return time.Time{}, false
	}
	return d.Schedule.debounceDeadline(d.Kind, d.TargetID)
}

// AgentMonitorEmitter 把 maisaka 监控事件转写到 zap 与既有监控出口
type AgentMonitorEmitter struct {
	LogPrefix string
	// FindSource 按消息编号回查会话历史，供引用预览补齐发送者与原文
	FindSource func(string) (maisaka.ContextMessage, bool)
}

// NewAgentMonitorEmitter 创建监控端口
func NewAgentMonitorEmitter(prefix string, findSource func(string) (maisaka.ContextMessage, bool)) *AgentMonitorEmitter {
	if strings.TrimSpace(prefix) == "" {
		prefix = "maisaka"
	}
	return &AgentMonitorEmitter{LogPrefix: prefix, FindSource: findSource}
}

// SessionStart 会话开始，对齐 emit_session_start 的载荷
func (e *AgentMonitorEmitter) SessionStart(runID, sessionID, sessionName string, isGroup bool) {
	monitor.EmitSessionStart(sessionID, sessionName, isGroup, "", "", "")
}

// MessageIngested 入站消息，对齐 emit_message_ingested 的载荷
func (e *AgentMonitorEmitter) MessageIngested(sessionID string, message maisaka.ContextMessage) {
	monitor.EmitMessage(monitor.EventMessageIngested, sessionID,
		monitorSpeakerName(message), maisaka.BuildMonitorMessageContent(message), message.MessageID,
		monitorMessageTimestamp(message), "", monitorMessageExtra(message, true, e.FindSource))
}

// MessageSent 消息发送，对齐 emit_message_sent 的载荷
func (e *AgentMonitorEmitter) MessageSent(sessionID, speakerName, sourceKind string, message maisaka.ContextMessage) {
	monitor.EmitMessage(monitor.EventMessageSent, sessionID,
		firstNonEmptyString(speakerName, monitorSpeakerName(message)),
		maisaka.BuildMonitorMessageContent(message), message.MessageID,
		monitorMessageTimestamp(message), sourceKind, monitorMessageExtra(message, false, e.FindSource))
}

// MessageUpdated 消息更新，对齐 emit_message_updated 的载荷
func (e *AgentMonitorEmitter) MessageUpdated(sessionID string, refreshKey [2]string, message maisaka.ContextMessage) {
	monitor.EmitMessage(monitor.EventMessageUpdated, sessionID,
		monitorSpeakerName(message), maisaka.BuildMonitorMessageContent(message), message.MessageID,
		monitorMessageTimestamp(message), "", monitorMessageExtra(message, true, e.FindSource))
}

// monitorSpeakerName 对齐 Python user_cardname or user_nickname or user_id
func monitorSpeakerName(message maisaka.ContextMessage) string {
	return firstNonEmptyString(
		message.MetadataString("sender_cardname"),
		message.MetadataString("user_cardname"),
		message.MetadataString("sender_name"),
		message.MetadataString("nickname"),
		message.UserID,
	)
}

func monitorMessageTimestamp(message maisaka.ContextMessage) float64 {
	if message.Timestamp.IsZero() {
		return float64(time.Now().UnixNano()) / 1e9
	}
	return float64(message.Timestamp.UnixNano()) / 1e9
}

// monitorMessageExtra 构造消息事件的平台与引用附加字段
func monitorMessageExtra(message maisaka.ContextMessage, withReceivedAt bool, findSource func(string) (maisaka.ContextMessage, bool)) map[string]any {
	extra := map[string]any{
		"platform": message.Platform,
		"user_id":  message.UserID,
	}
	if withReceivedAt && message.ReceivedAt != nil {
		extra["received_at"] = float64(message.ReceivedAt.UnixNano()) / 1e9
	}
	if media := monitorMessageMedia(message); len(media) > 0 {
		extra["media"] = media
	}
	if preview := monitorMessageReplyPreview(message, findSource); preview != nil {
		extra["reply_to"] = preview
	}
	return extra
}

// monitorMessageMedia 把图片/表情组件投影为监控媒体载荷
func monitorMessageMedia(message maisaka.ContextMessage) []map[string]any {
	media := make([]map[string]any, 0, len(message.Images)+len(message.Emojis))
	for index, image := range message.Images {
		media = append(media, map[string]any{
			"kind": monitor.MonitorMediaImage, "hash": image.MediaRef, "text": image.Description,
			"format": image.Format, "index": index,
		})
	}
	for index, emoji := range message.Emojis {
		media = append(media, map[string]any{
			"kind": monitor.MonitorMediaEmoji, "hash": emoji.MediaRef, "text": emoji.Description,
			"format": emoji.Format, "index": len(message.Images) + index,
		})
	}
	return media
}

// monitorMessageReplyPreview 构造回复引用预览，缺正文时回查会话历史
func monitorMessageReplyPreview(message maisaka.ContextMessage, findSource func(string) (maisaka.ContextMessage, bool)) map[string]any {
	target := map[string]any{
		"reply_to":              message.MetadataString("reply_to"),
		"reply_preview":         message.MetadataString("quote_content"),
		"reply_sender":          message.MetadataString("quote_sender"),
		"reply_sender_cardname": message.MetadataString("quote_sender_cardname"),
		"reply_sender_nickname": message.MetadataString("quote_sender_nickname"),
	}
	if strings.TrimSpace(message.MetadataString("quote_content")) == "" && findSource != nil {
		if replyTo := strings.TrimSpace(message.MetadataString("reply_to")); replyTo != "" {
			if source, ok := findSource(replyTo); ok {
				target["reply_preview"] = maisaka.BuildMonitorMessageContent(source)
				target["reply_sender"] = monitorSpeakerName(source)
			}
		}
	}
	return monitor.BuildMonitorReplyPreview(target, nil, nil)
}

// FlowStep 流程步骤
func (e *AgentMonitorEmitter) FlowStep(sessionID, step, title, detail string, cycleID int) {
	var cycle *int
	if cycleID > 0 {
		cycle = &cycleID
	}
	monitor.EmitFlowStep(sessionID, "", step, title, detail, cycle, nil)
}

// PlannerProgress Planner 进度
func (e *AgentMonitorEmitter) PlannerProgress(sessionID string, payload map[string]any) {
	monitor.Publish(monitor.EventPlannerProgress, payload)
}

// PlannerFinalized Planner 完成
func (e *AgentMonitorEmitter) PlannerFinalized(sessionID string, payload map[string]any) {
	monitor.Publish(monitor.EventPlannerFinalized, payload)
}

// AgentLearnerPort 裁切历史学习端口，未注入学习回调时安全跳过
type AgentLearnerPort struct {
	Trigger func(ctx context.Context, sessionID string, messages []maisaka.ContextMessage, onProgress func(map[string]any)) error
}

// TriggerTrimmedHistoryLearning 触发裁切历史学习
func (p *AgentLearnerPort) TriggerTrimmedHistoryLearning(ctx context.Context, sessionID string, messages []maisaka.ContextMessage, onProgress func(map[string]any)) error {
	if p == nil || p.Trigger == nil {
		return nil
	}
	return p.Trigger(ctx, sessionID, messages, onProgress)
}

// CancelTrimmedHistoryLearning 取消学习
func (p *AgentLearnerPort) CancelTrimmedHistoryLearning() {}

type AgentReplyExecutor struct {
	Speak func(ctx context.Context, content string, target *maisaka.ContextMessage) error
	// Generate 走独立 Replyer 提示词与模型；nil 时降级为 Planner 正文直发
	Generate func(ctx context.Context, plannerContent string, target *maisaka.ContextMessage, replyToolArgs map[string]any) (string, error)
	// Supplement 生成引用、@、图片与表情补充编排；nil 时保持纯文本回复
	Supplement func(ctx context.Context, guidance string, target *maisaka.ContextMessage) (*maisaka.ReplySupplement, error)
	// ReplyTool 按 reply 工具参数发送已生成正文与补充附件
	ReplyTool func(ctx context.Context, arguments map[string]any) maisaka.ToolExecutionResult
	// ResolveEmoji 解析补充编排中的上下文或语义表情引用
	ResolveEmoji func(ctx context.Context, reference map[string]any) (tools.EmojiAttachment, error)
	// SendEmojiAttachment 在正文发送后发送补充编排中的表情
	SendEmojiAttachment func(ctx context.Context, attachment tools.EmojiAttachment) error
	// History 返回当前会话历史，供 Replyer 渲染
	History func() []maisaka.ContextMessage
	// PreviousReply 返回最近一次回复同一目标消息的正文，用于重复回复提醒
	PreviousReply func(msgID string) string
	// RecordReply 记录成功发送的回复目标与正文
	RecordReply func(msgID string, content string)
}

const duplicateTargetReplyReminderTemplate = maisaka.DuplicateTargetReplyReminderTemplate

// withDuplicateTargetReplyReminder 在重复回复同一目标时给 Replyer 追加独立提醒
func withDuplicateTargetReplyReminder(args map[string]any, previous string) map[string]any {
	normalized := maisaka.NormalizeDuplicateReplyText(previous)
	if normalized == "" {
		return args
	}
	updated := make(map[string]any, len(args)+1)
	for key, value := range args {
		updated[key] = value
	}
	updated["_duplicate_target_reply_reminder"] = fmt.Sprintf(duplicateTargetReplyReminderTemplate, normalized)
	return updated
}

// ReplyFromContent 先由 Replyer 与补充编排并行生成，再按编排发送引用、@、图片与表情
func (e *AgentReplyExecutor) ReplyFromContent(ctx context.Context, content string, target *maisaka.ContextMessage, replyToolArgs map[string]any) maisaka.ToolExecutionResult {
	if e == nil || (e.Speak == nil && e.ReplyTool == nil) {
		return maisaka.ToolExecutionResult{Success: false, Message: "回复执行端口未注入"}
	}
	if strings.TrimSpace(content) == "" {
		return maisaka.ToolExecutionResult{Success: false, Message: "回复内容为空"}
	}
	replyText := strings.TrimSpace(content)
	activeArgs := replyToolArgs
	// 对齐 Python reply.py:498：并行补充编排时引用目标尚未确定，不加重复回复提醒
	if target != nil && e.PreviousReply != nil && e.Supplement == nil && strings.TrimSpace(target.MessageID) != "" {
		activeArgs = withDuplicateTargetReplyReminder(activeArgs, e.PreviousReply(strings.TrimSpace(target.MessageID)))
	}
	var supplement *maisaka.ReplySupplement
	if e.Generate != nil {
		var err error
		if e.Supplement != nil {
			replyText, supplement, err = e.generateReplyAndSupplement(ctx, content, target, activeArgs)
		} else {
			replyText, err = e.Generate(ctx, content, target, activeArgs)
		}
		if err != nil {
			return maisaka.ToolExecutionResult{Success: false, Message: fmt.Sprintf("生成可见回复失败: %v", err)}
		}
		replyText = strings.TrimSpace(replyText)
		if replyText == "" {
			return maisaka.ToolExecutionResult{Success: false, Message: "生成可见回复失败。"}
		}
	}
	if supplement != nil {
		if e.ReplyTool == nil {
			return maisaka.ToolExecutionResult{Success: false, Message: "缺少 reply 工具发送端口"}
		}
		arguments, emojis := buildReplySupplementArguments(ctx, activeArgs, replyText, supplement, e.ResolveEmoji)
		if len(emojis) > 0 && e.SendEmojiAttachment == nil {
			return maisaka.ToolExecutionResult{Success: false, Message: "缺少表情发送端口"}
		}
		result := e.ReplyTool(ctx, arguments)
		if !result.Success {
			return result
		}
		for _, attachment := range emojis {
			if err := e.SendEmojiAttachment(ctx, attachment); err != nil {
				return maisaka.ToolExecutionResult{Success: false, Message: fmt.Sprintf("发送补充表情失败: %v", err)}
			}
		}
		return maisaka.ToolExecutionResult{Success: true, Content: replyText, Message: "回复已发送"}
	}
	if e.Speak == nil {
		return maisaka.ToolExecutionResult{Success: false, Message: "回复执行端口未注入"}
	}
	if err := e.Speak(ctx, replyText, target); err != nil {
		return maisaka.ToolExecutionResult{Success: false, Message: err.Error()}
	}
	if target != nil && e.RecordReply != nil && strings.TrimSpace(target.MessageID) != "" {
		e.RecordReply(strings.TrimSpace(target.MessageID), replyText)
	}
	return maisaka.ToolExecutionResult{Success: true, Content: replyText, Message: "回复已发送"}
}

// generateReplyAndSupplement 并行生成正文与补充编排；任一侧失败都不发送
func (e *AgentReplyExecutor) generateReplyAndSupplement(ctx context.Context, content string, target *maisaka.ContextMessage, replyToolArgs map[string]any) (string, *maisaka.ReplySupplement, error) {
	if e == nil || e.Generate == nil || e.Supplement == nil {
		return "", nil, fmt.Errorf("回复生成端口未注入")
	}
	generationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type textResult struct {
		text string
		err  error
	}
	type supplementResult struct {
		supplement *maisaka.ReplySupplement
		err        error
	}
	textCh := make(chan textResult, 1)
	supplementCh := make(chan supplementResult, 1)
	go func() {
		text, err := e.Generate(generationCtx, content, target, replyToolArgs)
		textCh <- textResult{text: text, err: err}
	}()
	go func() {
		supplement, err := e.Supplement(generationCtx, content, target)
		supplementCh <- supplementResult{supplement: supplement, err: err}
	}()
	text := <-textCh
	supp := <-supplementCh
	if text.err != nil {
		return "", nil, fmt.Errorf("生成可见回复或补充内容时发生异常: %w", text.err)
	}
	if supp.err != nil || supp.supplement == nil {
		// 补充失败只跳过补充，正文成功即可继续发送
		if supp.err != nil {
			zap.L().Error("回复补充生成失败，跳过补充并继续发送正文", zap.Error(supp.err))
		}
		return strings.TrimSpace(text.text), nil, nil
	}
	return strings.TrimSpace(text.text), supp.supplement, nil
}

// AgentChatClient 把 Maisaka ChatRequest 转成宿主模型调用
type AgentChatClient struct {
	ChatFunc func(ctx context.Context, request maisaka.ChatRequest) (maisaka.ChatResponse, error)
}

// Chat 执行一次模型请求
func (c *AgentChatClient) Chat(ctx context.Context, request maisaka.ChatRequest) (maisaka.ChatResponse, error) {
	if c == nil || c.ChatFunc == nil {
		return maisaka.ChatResponse{}, fmt.Errorf("maisaka ChatClient 未注入真实模型调用")
	}
	return c.ChatFunc(ctx, request)
}

// AgentHistoryCompactor 历史压缩端口
type AgentHistoryCompactor struct {
	Agent *Agent
	// SummarizeFunc 可注入自定义实现；nil 时使用真实 Planner 模型生成滚动摘要
	SummarizeFunc func(ctx context.Context, input maisaka.HistoryCompactionInput) (*maisaka.ContextMessage, error)
}

// Summarize 为被裁切的历史生成滚动摘要，裁切边界由后处理统一决定
func (c *AgentHistoryCompactor) Summarize(ctx context.Context, input maisaka.HistoryCompactionInput) (*maisaka.ContextMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("maisaka HistoryCompactor 未初始化")
	}
	if c.SummarizeFunc != nil {
		return c.SummarizeFunc(ctx, input)
	}
	if c.Agent == nil {
		return nil, fmt.Errorf("maisaka HistoryCompactor 缺少 Agent 绑定")
	}
	snapshot := input.Snapshot
	if snapshot == nil {
		return nil, nil
	}
	if len(input.Removed) == 0 {
		return nil, fmt.Errorf("没有可压缩的历史内容")
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		sessionID = snapshot.SessionID
	}
	timestamp := input.Timestamp
	if timestamp.IsZero() {
		timestamp = input.Removed[len(input.Removed)-1].Timestamp
	}
	summary, err := c.Agent.buildContextSummary(ctx, contextSummaryInput{
		Snapshot:      snapshot,
		Removed:       input.Removed,
		Recent:        input.Recent,
		Previous:      input.Previous,
		SessionID:     sessionID,
		Timestamp:     timestamp,
		Focus:         input.Focus,
		DisplayPrefix: input.DisplayPrefix,
	})
	if err != nil {
		return nil, err
	}
	return summary, nil
}

// AgentToolRuntime 把 Maisaka 工具循环接到 Eino 工具快照
type AgentToolRuntime struct {
	Agent    *Agent
	Kind     string
	TargetID int64
	Runtime  *maisaka.Runtime
}

// desktopPetToolVisible 对齐 Python ChatSessionToolProvider/TodoToolProvider 的 owner 会话过滤
func desktopPetToolVisible(ctx context.Context, name string) bool {
	return tools.DesktopPetToolVisible(ctx, name)
}

// AvailableTools 返回当前 Action Loop 可见工具
func (t *AgentToolRuntime) AvailableTools(ctx context.Context) ([]maisaka.ToolSpec, error) {
	if t == nil || t.Agent == nil {
		return nil, fmt.Errorf("工具运行时未绑定 Agent")
	}
	deferred := map[string]bool{}
	if tc := tools.GetToolContext(ctx); tc != nil {
		for _, entry := range tc.ToolCatalog {
			deferred[entry.Name] = entry.Deferred
		}
	}
	specs := make([]maisaka.ToolSpec, 0, len(t.Agent.generationTools(t.Kind)))
	for _, base := range t.Agent.generationTools(t.Kind) {
		if base == nil {
			continue
		}
		info, err := base.Info(ctx)
		if err != nil || info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		spec := maisaka.ToolSpec{Name: info.Name, Description: info.Desc, Deferred: deferred[info.Name]}
		spec = maisaka.AnnotateBuiltinToolSpec(spec)
		if !t.builtinToolAllowed(info.Name) {
			continue
		}
		if !desktopPetToolVisible(ctx, info.Name) {
			continue
		}
		if !tools.AgentChatToolVisible(ctx, info.Name, t.Agent.agentChatPolicy()) {
			continue
		}
		switch maisaka.GetBuiltinToolVisibility(spec) {
		case maisaka.BuiltinHidden:
			continue
		case maisaka.BuiltinDeferred:
			spec.Deferred = true
		}
		if info.ParamsOneOf != nil {
			params, err := jsonSchemaToMap(info.ParamsOneOf)
			if err != nil {
				return nil, fmt.Errorf("读取工具 %s 参数结构失败: %w", info.Name, err)
			}
			spec.Parameters = params
		}
		specs = append(specs, spec)
	}
	if len(specs) > 0 {
		return specs, nil
	}
	if tc := tools.GetToolContext(ctx); tc != nil {
		return toolSpecsFromCatalog(tc.ToolCatalog), nil
	}
	session := t.Agent.sessions.get(t.Kind, t.TargetID)
	if session == nil {
		return nil, fmt.Errorf("会话不存在: %s:%d", t.Kind, t.TargetID)
	}
	tc := tools.GetToolContext(t.Agent.buildConversationToolContext(ctx, t.Kind, t.TargetID, session))
	if tc == nil {
		return nil, nil
	}
	return toolSpecsFromCatalog(tc.ToolCatalog), nil
}

func jsonSchemaToMap(params *schema.ParamsOneOf) (map[string]any, error) {
	jsonSchema, err := params.ToJSONSchema()
	if err != nil {
		return nil, err
	}
	raw, err := sonic.Marshal(jsonSchema)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := sonic.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func toolSpecsFromCatalog(catalog []tools.ToolCatalogEntry) []maisaka.ToolSpec {
	specs := make([]maisaka.ToolSpec, 0, len(catalog))
	for _, entry := range catalog {
		spec := maisaka.AnnotateBuiltinToolSpec(maisaka.ToolSpec{
			Name: entry.Name, Description: entry.Description, Parameters: entry.Parameters, Deferred: entry.Deferred,
		})
		switch maisaka.GetBuiltinToolVisibility(spec) {
		case maisaka.BuiltinHidden:
			continue
		case maisaka.BuiltinDeferred:
			spec.Deferred = true
		}
		specs = append(specs, spec)
	}
	return specs
}

// replyGenerationError 携带 replyer 失败类型，供 reply 工具反馈给 Planner
type replyGenerationError struct {
	Code    string
	Reason  string
	Details map[string]any
}

func (e *replyGenerationError) Error() string { return e.Reason }

// replyToolFailureResult 把 replyer 失败类型转换成 Planner 可读的工具结果
func replyToolFailureResult(invocation maisaka.ToolInvocation, err error) (maisaka.ToolExecutionResult, bool) {
	var failure *replyGenerationError
	if !errors.As(err, &failure) {
		return maisaka.ToolExecutionResult{}, false
	}
	msgID := strings.TrimSpace(fmt.Sprint(invocation.Arguments["msg_id"]))
	payload := map[string]any{"msg_id": msgID, "error_code": failure.Code, "reason": failure.Reason}
	message := failure.Reason
	if abort, ok := failure.Details["hook_abort"].(map[string]any); ok {
		reason := strings.TrimSpace(fmt.Sprint(abort["reason"]))
		payload["error_code"] = "plugin_aborted"
		payload["hook_abort"] = abort
		message = "本次操作被插件主动拦截：" + reason + "。请根据原因调整调用或停止，勿原样重复调用。"
	}
	payload["message"] = message
	encoded, marshalErr := sonic.MarshalString(payload)
	if marshalErr != nil {
		return maisaka.ToolExecutionResult{}, false
	}
	return maisaka.ToolExecutionResult{
		Success:  false,
		ToolName: "reply",
		Content:  encoded,
		Message:  message,
		Metadata: map[string]any{"structured_content": payload},
	}, true
}

// injectReplyerContent 让 reply 工具先走独立 Replyer 生成正文
func (t *AgentToolRuntime) injectReplyerContent(ctx context.Context, kind string, targetID int64, session *Session, arguments map[string]any) error {
	if arguments == nil {
		return fmt.Errorf("reply 工具参数为空")
	}
	rawMessageID := strings.TrimSpace(fmt.Sprint(arguments["msg_id"]))
	if rawMessageID == "" || rawMessageID == "<nil>" {
		return fmt.Errorf("reply 工具需要提供有效的 msg_id 参数")
	}
	var runtime *maisaka.Runtime
	if session != nil {
		runtime = session.MaisakaRuntime()
	}
	history := []maisaka.ContextMessage(nil)
	if runtime != nil {
		history = runtime.History()
	}
	messageIDs := make([]string, 0, len(history))
	for _, message := range history {
		if message.MessageID != "" {
			messageIDs = append(messageIDs, message.MessageID)
		}
	}
	if expanded, ok := maisaka.ExpandMessageIDAliases(rawMessageID, maisaka.BuildAliasMap(messageIDs)).(string); ok && expanded != "" {
		rawMessageID = expanded
	}
	arguments["msg_id"] = rawMessageID
	target := (*maisaka.ContextMessage)(nil)
	if runtime != nil {
		if message, ok := runtime.FindSourceMessageByID(rawMessageID); ok {
			target = &message
		}
	}
	if target == nil {
		return fmt.Errorf("未找到要回复的目标消息，msg_id=%s", maisaka.ToDisplayMessageID(rawMessageID))
	}
	if parsed, err := strconv.ParseInt(target.MessageID, 10, 64); err == nil && parsed != 0 {
		arguments["reply_to"] = parsed
	}
	reference, _ := arguments["chat_anlyze"].(string)
	if strings.TrimSpace(reference) == "" {
		return fmt.Errorf("reply 工具必须提供非空 chat_anlyze")
	}
	generated, err := t.Agent.generateReplyerText(ctx, reference, target, history, kind == memory.ConversationKindGroup, arguments)
	if err != nil {
		return fmt.Errorf("生成可见回复失败: %w", err)
	}
	if strings.TrimSpace(generated) == "" {
		return fmt.Errorf("生成可见回复失败")
	}
	arguments["content"] = generated
	arguments["_replyer_generated"] = true
	return nil
}

// Invoke 执行一次工具调用；工具结果后处理仍由 Eino hook 负责
func (t *AgentToolRuntime) Invoke(ctx context.Context, invocation maisaka.ToolInvocation) (maisaka.ToolExecutionResult, error) {
	if t == nil || t.Agent == nil {
		return maisaka.ToolExecutionResult{}, fmt.Errorf("工具运行时未绑定 Agent")
	}
	if invocation.ToolName == maisaka.ToolToolSearchName && t.Runtime != nil {
		defer func() {
			if tc := tools.GetToolContext(ctx); tc != nil {
				names := make([]string, 0, len(tc.DiscoveredTools))
				for name := range tc.DiscoveredTools {
					names = append(names, name)
				}
				t.Runtime.DiscoverDeferredTools(names)
			}
		}()
	}
	if invocation.ToolName == "wait" {
		raw, hasSeconds := invocation.Arguments["seconds"]
		if !hasSeconds {
			return maisaka.ToolExecutionResult{
				Success:   true,
				ToolName:  "wait",
				PauseTool: "wait",
				Content:   "已结束本轮思考，等待新消息。",
				Message:   "已结束本轮思考，等待新消息。",
				Metadata:  map[string]any{"pause_execution": true},
			}, nil
		}
		seconds := 0
		valid := true
		switch value := raw.(type) {
		case float64:
			seconds = int(value)
		case int:
			seconds = value
		case int64:
			seconds = int(value)
		case json.Number:
			parsed, err := value.Int64()
			if err != nil {
				valid = false
			} else {
				seconds = int(parsed)
			}
		default:
			valid = false
		}
		if !valid || seconds < 0 {
			return maisaka.ToolExecutionResult{Success: false, ToolName: "wait", Message: "seconds 必须是非负整数"}, nil
		}
		count, err := t.Agent.beginConversationWaitCountedWithIDs(t.Kind, t.TargetID, seconds, invocation.CallID, invocation.LogicalTurnID)
		if err != nil {
			return maisaka.ToolExecutionResult{Success: false, ToolName: "wait", Message: err.Error()}, nil
		}
		detail := "当前对话循环进入等待状态，将固定等待 %d 秒；期间收到的新消息不会提前打断本次等待。"
		if t.Runtime != nil && t.Runtime.PlannerMessageSteering() {
			detail = "当前对话循环进入等待状态，最多等待 %d 秒；新消息可提前结束等待并继续思考。"
		}
		content := fmt.Sprintf(detail, seconds) + fmt.Sprintf("连续 wait 次数：%d。", count)
		return maisaka.ToolExecutionResult{
			Success:   true,
			ToolName:  "wait",
			PauseTool: "wait",
			Content:   content,
			Message:   content,
			Metadata: map[string]any{
				"pause_execution":        true,
				"consecutive_wait_count": count,
				"wait_seconds":           seconds,
			},
		}, nil
	}
	session := t.Agent.sessions.get(t.Kind, t.TargetID)
	if tools.GetToolContext(ctx) == nil && session != nil {
		ctx = t.Agent.buildConversationToolContext(ctx, t.Kind, t.TargetID, session)
	}
	if chatID := t.Agent.resolveMaiBotSessionID(ctx, t.Kind, t.TargetID); chatID != "" {
		if slices.Contains(announcement.Default.GetDisabledChatTools(chatID), invocation.ToolName) {
			return maisaka.ToolExecutionResult{Success: false, ToolName: invocation.ToolName, Message: "工具 " + invocation.ToolName + " 在当前聊天已被禁用"}, nil
		}
	}
	if invocation.ToolName == "reply" && !replyerGeneratedArgument(invocation.Arguments) {
		if err := t.injectReplyerContent(ctx, t.Kind, t.TargetID, session, invocation.Arguments); err != nil {
			if result, ok := replyToolFailureResult(invocation, err); ok {
				return result, nil
			}
			return maisaka.ToolExecutionResult{Success: false, ToolName: "reply", Message: err.Error()}, nil
		}
	}
	candidates := t.Agent.generationTools(t.Kind)
	for _, base := range candidates {
		if base == nil {
			continue
		}
		info, err := base.Info(ctx)
		if err != nil || info == nil || info.Name != invocation.ToolName {
			continue
		}
		invokable, ok := base.(tool.InvokableTool)
		if !ok {
			return maisaka.ToolExecutionResult{}, fmt.Errorf("工具 %s 不支持直接调用", invocation.ToolName)
		}
		arguments := "{}"
		if invocation.Arguments != nil {
			raw, err := sonic.Marshal(invocation.Arguments)
			if err != nil {
				return maisaka.ToolExecutionResult{}, fmt.Errorf("工具 %s 参数序列化失败: %w", invocation.ToolName, err)
			}
			arguments = string(raw)
		}
		invokeCtx := context.WithValue(ctx, maisakaInvocationContextKey{}, maisakaInvocationContext{
			CallID:        invocation.CallID,
			LogicalTurnID: invocation.LogicalTurnID,
		})
		if structured, ok := base.(mcp.ToolResultInvoker); ok {
			toolResult, err := structured.InvokeToolResult(invokeCtx, arguments)
			if err != nil {
				return maisaka.ToolExecutionResult{Success: false, ToolName: invocation.ToolName, Message: err.Error()}, nil
			}
			return mcpToolExecutionResult(invocation, info, toolResult), nil
		}
		if structured, ok := base.(tools.StructuredToolResultInvoker); ok {
			structuredResult, err := structured.InvokeStructuredToolResult(invokeCtx, arguments)
			if err != nil {
				return maisaka.ToolExecutionResult{Success: false, ToolName: invocation.ToolName, Message: err.Error()}, nil
			}
			return structuredToolExecutionResult(invocation, structuredResult), nil
		}
		content, err := invokable.InvokableRun(invokeCtx, arguments)
		if err != nil {
			return maisaka.ToolExecutionResult{Success: false, Message: err.Error()}, nil
		}
		result := maisaka.ToolExecutionResult{Success: true, ToolName: invocation.ToolName, Content: content, Message: content}
		switch invocation.ToolName {
		case "wait":
			result.PauseTool = "wait"
			result.Metadata = map[string]any{"pause_execution": true}
		}
		return result, nil
	}
	return maisaka.ToolExecutionResult{}, fmt.Errorf("当前会话不可用工具: %s", invocation.ToolName)
}

// AfterExecution 工具后置副作用；Eino hook 已负责记录落库，这里只补结构化日志
func (t *AgentToolRuntime) AfterExecution(ctx context.Context, invocation maisaka.ToolInvocation, result maisaka.ToolExecutionResult) {
	if t != nil && t.Runtime != nil && invocation.ToolName == maisaka.ToolToolSearchName {
		if tc := tools.GetToolContext(ctx); tc != nil {
			names := make([]string, 0, len(tc.DiscoveredTools))
			for name := range tc.DiscoveredTools {
				names = append(names, name)
			}
			t.Runtime.DiscoverDeferredTools(names)
		}
	}
	zap.L().Debug("maisaka 工具执行完成", zap.String("tool", invocation.ToolName),
		zap.Bool("success", result.Success), zap.String("call_id", invocation.CallID))
}

type maisakaInvocationContextKey struct{}

type maisakaInvocationContext struct {
	CallID        string
	LogicalTurnID string
}

// maisakaInvocationFromContext 读取当前工具调用绑定的轮次标识
func maisakaInvocationFromContext(ctx context.Context) (string, string) {
	if value, ok := ctx.Value(maisakaInvocationContextKey{}).(maisakaInvocationContext); ok {
		return value.CallID, value.LogicalTurnID
	}
	return "", ""
}

// builtinToolAllowed 按实例配置过滤内置工具；宿主未注入配置时全部保留
func (t *AgentToolRuntime) builtinToolAllowed(name string) bool {
	if t == nil || t.Runtime == nil || !t.Runtime.BuiltinToolConfigSet() {
		return true
	}
	return maisaka.BuiltinToolEnabledByConfig(name, t.Runtime.BuiltinToolConfig())
}

// maisakaBuiltinConfig 从实例配置读取内置工具开关，对齐 Python _is_builtin_tool_enabled_by_config
func maisakaBuiltinConfig(bot *maibotcfg.BotConfigFile, model *maibotcfg.ModelConfigFile) maisaka.BuiltinToolConfig {
	if bot == nil {
		return maisaka.BuiltinToolConfig{}
	}
	webSearchEnabled := false
	if model != nil {
		webSearchEnabled = len(model.ModelTaskConfig.WebSearch.ModelList) > 0
	}
	return maisaka.BuiltinToolConfig{
		PlannerSubagent:      bot.Capabilities.PlannerSubagent,
		ToolResultCondense:   bot.Capabilities.ToolResultCondense,
		WebSearchEnabled:     webSearchEnabled,
		MemoryQueryEnabled:   bot.AMemorix.Integration.EnableMemoryQueryTool,
		AMemorixEnabled:      bot.AMemorix.Plugin.Enabled,
		PersonProfileEnabled: bot.AMemorix.Integration.EnablePersonProfileQueryTool,
		ImageMemoryEnabled:   imageMemoryEnabled(bot, model),
	}
}

// imageMemoryEnabled 图片记忆需要总开关、图片开关与图片嵌入模型同时满足
func imageMemoryEnabled(bot *maibotcfg.BotConfigFile, model *maibotcfg.ModelConfigFile) bool {
	if bot == nil || model == nil || !bot.AMemorix.Plugin.Enabled || !bot.AMemorix.ImageMemory.Enabled {
		return false
	}
	for _, name := range model.ModelTaskConfig.ImageEmbedding.ModelList {
		if strings.TrimSpace(name) != "" {
			return true
		}
	}
	return false
}

const (
	// 对齐 Python src/config/official_configs.py:875-905 的默认值
	defaultGroupMaxContextSize         = 50
	defaultPrivateMaxContextSize       = 75
	defaultToolResultCondenseThreshold = 3000
)

// maisakaMaxContextSize 返回当前会话实时生效的上下文条数上限
// 对齐 Python src/maisaka/runtime.py:213-222：群聊取 chat.max_context_size，私聊取 max_private_context_size
func (a *Agent) maisakaMaxContextSize(isGroup bool) int {
	fallback := defaultGroupMaxContextSize
	if !isGroup {
		fallback = defaultPrivateMaxContextSize
	}
	bot := a.instanceBotConfig()
	if bot == nil {
		return fallback
	}
	size := bot.Chat.MaxContextSize
	if !isGroup {
		size = bot.Chat.MaxPrivateContextSize
	}
	if size <= 0 {
		return fallback
	}
	return size
}

// maisakaToolResultCondense 返回工具结果整理开关与字符阈值
// 对齐 Python src/config/official_configs.py:1029-1055 与 src/maisaka/tool_result_condenser.py:55-66
func (a *Agent) maisakaToolResultCondense() (bool, int) {
	bot := a.instanceBotConfig()
	if bot == nil {
		return false, defaultToolResultCondenseThreshold
	}
	threshold := bot.Capabilities.ToolResultCondenseThreshold
	if threshold <= 0 {
		threshold = defaultToolResultCondenseThreshold
	}
	return bot.Capabilities.ToolResultCondense, threshold
}

// generationTools 取当前 generation 的会话工具快照
func (a *Agent) generationTools(kind string) []tool.BaseTool {
	a.generationMu.RLock()
	defer a.generationMu.RUnlock()
	if a.generation == nil {
		return nil
	}
	if kind == memory.ConversationKindPrivate {
		return a.generation.privateTools
	}
	return a.generation.groupTools
}

// passiveReplyerEnabled 读取 experimental.passive_replyer，未加载实例配置时按默认 false
func passiveReplyerEnabled(bot *maibotcfg.BotConfigFile) bool {
	if bot == nil {
		return false
	}
	return bot.Experimental.PassiveReplyer
}

// buildMaisakaRuntime 按会话构造 Maisaka 决策运行时
// 工具、回复、监控、学习端口接真实实现；模型与压缩端口在调用时返回真实缺失错误
func (a *Agent) buildMaisakaRuntime(kind string, targetID int64, session *Session) *maisaka.Runtime {
	if a == nil || session == nil {
		return nil
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	sessionID := a.resolveMaiBotSessionID(ctx, kind, targetID)
	if sessionID == "" {
		return nil
	}
	isGroup := kind == memory.ConversationKindGroup
	bot := a.instanceBotConfig()
	opts := maisaka.RuntimeOptions{
		SessionID:       sessionID,
		SessionName:     sessionID,
		IsGroupSession:  isGroup,
		PlannerSteering: a.plannerMessageSteeringEnabled(),
		Debouncer:       NewAgentDebouncer(a, kind, targetID),
		DebounceMode: func(group bool) string {
			if group {
				return string(a.groupDebounceMode())
			}
			return string(a.privateDebounceMode())
		},
		ReplyFrequency: func() float64 {
			if isGroup {
				return a.gateFrequency(targetID)
			}
			return a.privateReplyFrequency(targetID)
		},
		// 上下文条数按 Python chat.max_context_size / max_private_context_size 解析，token 窗口只用于压缩
		MaxContextSize:     func() int { return a.maisakaMaxContextSize(isGroup) },
		ToolResultCondense: a.maisakaToolResultCondense,
		MentionDetector:    a.buildMentionDetector(kind, targetID),
		OnlineBotAccountIDs: func(string) map[string]struct{} {
			out := map[string]struct{}{}
			if a.SelfID() > 0 {
				out[strconv.FormatInt(a.SelfID(), 10)] = struct{}{}
			}
			return out
		},
		Monitor: NewAgentMonitorEmitter(sessionID, func(messageID string) (maisaka.ContextMessage, bool) {
			runtime := session.MaisakaRuntime()
			if runtime == nil {
				return maisaka.ContextMessage{}, false
			}
			return runtime.FindSourceMessageByID(messageID)
		}),
		Learners: &AgentLearnerPort{
			Trigger: a.triggerTrimmedHistoryLearning,
		},
		EnableExpressionLearning: func() bool {
			_, enabled := learners.DefaultRegistry().ChatConfig().GetExpressionConfig(sessionID)
			return enabled
		},
		EnableJargonLearning: func() bool {
			_, enabled := learners.DefaultRegistry().ChatConfig().GetJargonConfig(sessionID)
			return enabled
		},
		Prompts: a.newMaisakaPromptSource(),
		Compactor: &AgentHistoryCompactor{
			Agent: a,
		},
		HistoryPostProcessor: func(history []maisaka.ContextMessage, options maisaka.HistoryPostProcessOptions) maisaka.HistoryPostProcessResult {
			return maisakactx.ProcessChatHistoryAfterCycle(history, options)
		},
		HistoryTrimSink: func(original, final []maisaka.ContextMessage) {
			session.ApplyTrimmedHistory(original, final)
		},
		PassiveReplyer: passiveReplyerEnabled(bot),
		VisualRefresh:  a.agentVisual(),
		// 每次展示时热读配置，WebUI 切换 show_maisaka_thinking 后下一轮立即生效
		ShowThinking: func() bool {
			cfg := a.instanceBotConfig()
			return cfg != nil && cfg.Debug.ShowMaisakaThinking
		},
		ToolRecords: &agentToolRecordStore{
			mem:              a.memory,
			recordStructured: bot != nil && bot.Debug.RecordToolStructuredContent,
		},
		ToolPostExecution: newToolPostExecutionHandler(&agentMemoryFeedbackService{}),
	}
	toolRuntime := &AgentToolRuntime{Agent: a, Kind: kind, TargetID: targetID}
	opts.Tools = toolRuntime
	var built *maisaka.Runtime
	// 主动回合回填真实用户消息，避免触发消息本身成为回复引用目标
	opts.ProactiveContextRestore = func(callCtx context.Context, limit int) ([]maisaka.ContextMessage, error) {
		return a.restoreProactiveContextMessages(callCtx, kind, targetID, sessionID, limit)
	}
	// 处于 Focus 共享组时主动任务改由共享组登记，对齐 Python src/maisaka/runtime.py:824-838
	opts.FocusProactiveRegistrar = func(trigger maisaka.ContextMessage) bool {
		if built == nil {
			return false
		}
		session.mu.Lock()
		group := session.maisakaFocusGroup
		session.mu.Unlock()
		if group == nil {
			return false
		}
		member := &focusGroupMember{agent: a, runtime: built, session: session, sessionID: sessionID, group: group}
		group.RegisterProactive(member, trigger)
		return true
	}
	// 插件发送路径只有 runtime，通过该端口补记 Focus 共享组，对齐 Python record_sent_message
	opts.FocusSentRecorder = func(message maisaka.ContextMessage) {
		session.mu.Lock()
		group := session.maisakaFocusGroup
		session.mu.Unlock()
		if group == nil {
			return
		}
		group.RecordSentMessageForSession(sessionID, message)
	}
	opts.Reply = &AgentReplyExecutor{
		Speak: func(callCtx context.Context, content string, target *maisaka.ContextMessage) error {
			replyTo := int64(0)
			if target != nil {
				if parsed, err := strconv.ParseInt(strings.TrimSpace(target.MessageID), 10, 64); err == nil {
					replyTo = parsed
				}
			}
			if isGroup {
				return a.doSpeak(callCtx, targetID, content, replyTo, nil)
			}
			return a.doPrivateSpeak(callCtx, targetID, content, replyTo)
		},
		History: func() []maisaka.ContextMessage {
			if built == nil {
				return nil
			}
			return built.History()
		},
		PreviousReply: session.previousReplyToTarget,
		RecordReply:   session.recordReplyToTarget,
		Generate: func(callCtx context.Context, plannerContent string, target *maisaka.ContextMessage, replyToolArgs map[string]any) (string, error) {
			var history []maisaka.ContextMessage
			if built != nil {
				history = built.History()
			}
			return a.generateReplyerText(callCtx, plannerContent, target, history, isGroup, replyToolArgs)
		},
		Supplement: func(callCtx context.Context, guidance string, _ *maisaka.ContextMessage) (*maisaka.ReplySupplement, error) {
			if built == nil {
				return nil, fmt.Errorf("缺少本轮 Planner 请求快照，无法生成回复补充内容")
			}
			snapshot := built.PlannerRequestSnapshot()
			if snapshot == nil {
				return nil, fmt.Errorf("缺少本轮 Planner 请求快照，无法生成回复补充内容")
			}
			options := maisaka.SupplementResourceOptions{
				IsGroup:       isGroup,
				EnableReplyAt: a.replyAtEnabled(),
			}
			if bot != nil {
				options.GroupQuoteFrequency = bot.Chat.ReplyStyle.GroupQuoteFrequency
				options.PrivateQuoteFrequency = bot.Chat.ReplyStyle.PrivateQuoteFrequency
			}
			supplement, err := maisaka.GenerateReplySupplement(
				&agentReplySupplementGenerator{agent: a, ctx: callCtx},
				a.newMaisakaPromptSource(),
				*snapshot,
				guidance,
				sessionID,
				isGroup,
				built.History(),
				options,
			)
			if err != nil {
				return nil, err
			}
			return &supplement, nil
		},
		ReplyTool: func(callCtx context.Context, arguments map[string]any) maisaka.ToolExecutionResult {
			result, err := toolRuntime.Invoke(callCtx, maisaka.ToolInvocation{
				ToolName:  "reply",
				Arguments: arguments,
				CallID:    "reply_supplement",
			})
			if err != nil {
				return maisaka.ToolExecutionResult{Success: false, ToolName: "reply", Message: err.Error()}
			}
			return result
		},
		ResolveEmoji: func(callCtx context.Context, reference map[string]any) (tools.EmojiAttachment, error) {
			return a.resolveEmojiAttachment(callCtx, session, sessionID, reference)
		},
		SendEmojiAttachment: func(callCtx context.Context, attachment tools.EmojiAttachment) error {
			return a.sendEmojiAttachment(callCtx, kind, targetID, attachment)
		},
	}
	if built != nil {
		toolRuntime.Runtime = built
	}
	if bot != nil {
		opts.ReplyTiming = func() maisaka.IdleBackoffConfig {
			return maibotcfg.IdleBackoffFromTiming(bot.Chat.ReplyTiming)
		}
		opts.InevitableAtReply = func() bool { return bot.Chat.ReplyTiming.InevitableAtReply }
		opts.MentionedBotReply = func() bool { return bot.Chat.ReplyTiming.MentionedBotReply }
	}
	opts.FocusActive = func() bool {
		settings, _, ok := a.focusExperimentalSnapshot()
		if !ok {
			return false
		}
		focusCtx, focusCancel := a.persistenceContext()
		defer focusCancel()
		info, ok := a.focusSessionInfo(focusCtx, session)
		if !ok {
			return false
		}
		return focus.IsEnabledForSession(settings, info)
	}
	opts.VisualPlannerEnabled = a.plannerVisualEnabled
	opts.PlannerExtraInjections = a.buildMaisakaPlannerExtraInjections(session)
	built = maisaka.NewRuntime(opts)
	if cfg := a.instanceConfigSnapshot(); cfg != nil {
		built.SetBuiltinToolConfig(maisakaBuiltinConfig(&cfg.Bot, &cfg.Model))
	}
	// 初始阈值，运行时热更新由 ToolResultCondense 提供者覆盖
	if _, threshold := a.maisakaToolResultCondense(); threshold > 0 {
		built.SetToolResultCondenseThreshold(threshold)
	}
	toolRuntime.Runtime = built
	return built
}

// attachMaisakaRuntime 按会话缓存 Maisaka 运行时
func (a *Agent) attachMaisakaRuntime(kind string, targetID int64, session *Session) *maisaka.Runtime {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.maisakaRuntime != nil {
		runtime := session.maisakaRuntime
		session.mu.Unlock()
		return runtime
	}
	session.mu.Unlock()
	runtime := a.buildMaisakaRuntime(kind, targetID, session)
	if runtime == nil {
		return nil
	}
	session.mu.Lock()
	if session.maisakaRuntime == nil {
		session.maisakaRuntime = runtime
	}
	cached := session.maisakaRuntime
	session.mu.Unlock()
	return cached
}

// consumeMaisakaWaitState 清理 Maisaka 侧遗留 wait，消息触发或超时后调用
func consumeMaisakaWaitState(session *Session) {
	if session == nil {
		return
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil || !runtime.HasPendingWaitToolCall() {
		return
	}
	toolCallID, logicalTurnID, elapsed, requested, err := runtime.ConsumePendingWaitState()
	if err != nil {
		return
	}
	zap.L().Debug("Maisaka wait 状态清理", zap.String("tool_call_id", toolCallID),
		zap.String("logical_turn_id", logicalTurnID), zap.Float64("elapsed_seconds", elapsed), zap.Any("requested_seconds", requested))
}

// MaisakaRuntime 返回会话的 Maisaka 运行时，未接线时为 nil
func (s *Session) MaisakaRuntime() *maisaka.Runtime {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maisakaRuntime
}

// buildMentionDetector 对齐 Python is_mentioned_bot_in_message，供 Runtime.updateMessageTriggerState 使用。
func (a *Agent) buildMentionDetector(kind string, targetID int64) maisaka.MentionDetector {
	return func(message maisaka.ContextMessage) (bool, bool, float64) {
		in := chatutils.MentionDetectInput{
			Text:        message.Content,
			Platform:    firstNonEmptyString(message.Platform, a.conversationPlatform(kind, targetID)),
			IsAt:        message.MetadataBool("is_at"),
			IsMentioned: message.MetadataBool("is_mentioned"),
		}
		if cfg, ok := message.Metadata["additional_config"].(map[string]any); ok {
			in.AdditionalConfig = cfg
		}
		if host := mentionHostSessionMessage(message); host != nil {
			if in.Text == "" {
				in.Text = host.PlainText()
			}
			if in.Platform == "" {
				in.Platform = host.Platform
			}
			in.IsAt = in.IsAt || host.IsAt
			in.IsMentioned = in.IsMentioned || host.IsMentioned
			if in.AdditionalConfig == nil {
				in.AdditionalConfig = host.MessageInfo.AdditionalConfig
			}
			in.RawMessage = host.RawMessage
		}
		if bot := a.instanceBotConfig(); bot != nil {
			in.Nickname = bot.Bot.Nickname
			in.AliasNames = append([]string(nil), bot.Bot.AliasNames...)
			in.InevitableAt = bot.Chat.ReplyTiming.InevitableAtReply
			in.MentionedReply = bot.Chat.ReplyTiming.MentionedBotReply
		}
		in.BotAccounts = a.mentionBotAccounts(in.Platform)
		in.IsBotSelf = func(platform, userID string) bool {
			return a.mentionIsBotSelf(platform, userID)
		}
		return chatutils.IsMentionedBotInMessage(in)
	}
}

func mentionHostSessionMessage(message maisaka.ContextMessage) *messagereceive.SessionMessage {
	if message.Metadata == nil {
		return nil
	}
	switch typed := message.Metadata[heartflow.MetadataHostSource].(type) {
	case *messagereceive.SessionMessage:
		return typed
	case messagereceive.SessionMessage:
		copy := typed
		return &copy
	default:
		return nil
	}
}

func (a *Agent) mentionBotAccounts(platform string) []string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if a != nil {
		if selfID := a.SelfID(); selfID > 0 {
			add(strconv.FormatInt(selfID, 10))
		}
	}
	if a == nil {
		return out
	}
	a.callbacksMu.RLock()
	provider := a.callbacks.BotAccountPairs
	a.callbacksMu.RUnlock()
	if provider == nil {
		return out
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	pairs, err := provider(ctx)
	if err != nil {
		return out
	}
	for _, pair := range pairs {
		if platform != "" && strings.ToLower(strings.TrimSpace(pair[0])) != platform {
			continue
		}
		add(pair[1])
	}
	return out
}

func (a *Agent) mentionIsBotSelf(platform, userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	for _, account := range a.mentionBotAccounts(platform) {
		if account == userID {
			return true
		}
	}
	return false
}
