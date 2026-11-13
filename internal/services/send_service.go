package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"maibot-go/internal/common/database"
	"maibot-go/internal/emoji_system"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/onebot"
)

// SendOptions 对齐 send_service 常用发送选项
type SendOptions struct {
	Typing               bool
	SetReply             bool
	ReplyMessageID       string
	StorageMessage       bool
	ShowLog              bool
	SyncToMaisakaHistory bool
	MaisakaSourceKind    string
}

// ReplyPreview 被引用消息的预览信息，对齐 Python ReplyComponent 的 target_* 字段
type ReplyPreview struct {
	Content  string
	SenderID string
	Nickname string
	Cardname string
}

// SendRequest 是 services 层出站消息
type SendRequest struct {
	MessageID                string
	SessionID                string
	Platform                 string
	AccountID                string
	Scope                    string
	ChatType                 platformio.ChatType
	TargetID                 string
	Segments                 []platformio.Segment
	PlainText                string
	ReplyToMessageID         string
	ExternalReplyToMessageID string
	// ReplyPreview 引用目标预览，非空时补进 reply 段并计入纯文本摘要
	ReplyPreview ReplyPreview
	// Kind 用于区分布尔发送类型，目前仅表情发送参与打字时长计算
	Kind string
}

// SendResult 是发送成功后的最终消息
type SendResult struct {
	MessageID         string                   `json:"message_id"`
	PlatformMessageID string                   `json:"platform_message_id"`
	HistorySynced     bool                     `json:"history_synced"`
	Batch             platformio.DeliveryBatch `json:"-"`
}

// MaisakaSentMessage 是同步到 Maisaka 内部历史的已发送消息
type MaisakaSentMessage struct {
	SessionID         string
	MessageID         string
	PlatformMessageID string
	Platform          string
	AccountID         string
	Scope             string
	TargetID          string
	ChatType          string
	Text              string
	SourceKind        string
	// SpeakerName bot 自身展示名，用于 message.sent 监控
	SpeakerName string
	// ReplyToMessageID 被引用消息编号，写入 Planner 前缀的 quote 属性
	ReplyToMessageID string
	Timestamp        time.Time
}

// MaisakaHistoryAppender 由主程序 Agent 实现，把已发送消息追加到对应会话的 Maisaka 历史
type MaisakaHistoryAppender interface {
	AppendSentMessage(ctx context.Context, message MaisakaSentMessage) error
}

// SleepFunc 可替换的等待函数，测试注入后不真实睡眠
type SleepFunc func(ctx context.Context, duration time.Duration) error

// ServiceSentMessage 是 SendService 成功投递后的记忆自动化事件
// 字段保持字符串 ID，避免宿主把外部平台身份压成数值
type ServiceSentMessage struct {
	MessageID         string
	PlatformMessageID string
	SessionID         string
	Platform          string
	AccountID         string
	Scope             string
	ChatType          string
	TargetID          string
	Text              string
	ReplyToMessageID  string
	Timestamp         time.Time
}

// ServiceSentMessageHook 发送成功后的记忆自动化端口
// 实现方返回的错误只告警，不影响已成功的发送结果
type ServiceSentMessageHook interface {
	OnServiceMessageSent(ctx context.Context, message ServiceSentMessage) error
}

var (
	defaultSentHookMu sync.RWMutex
	defaultSentHook   ServiceSentMessageHook
)

// SetDefaultSendMessageHook 注入包级发送后钩子；传 nil 清除
func SetDefaultSendMessageHook(hook ServiceSentMessageHook) {
	defaultSentHookMu.Lock()
	defaultSentHook = hook
	defaultSentHookMu.Unlock()
}

func currentDefaultSendMessageHook() ServiceSentMessageHook {
	defaultSentHookMu.RLock()
	defer defaultSentHookMu.RUnlock()
	return defaultSentHook
}

// SendService 对齐 MaiBot send_service 的构建、Platform IO 投递与成功落库
type SendService struct {
	Manager  *platformio.Manager
	DB       *gorm.DB
	BotName  string
	Accounts *BotAccountService

	// History 为 nil 时 sync_to_maisaka_history 只告警，不伪造同步成功
	History MaisakaHistoryAppender
	// Messages 用于查询被引用消息，为 reply 段补齐预览；nil 时只带引用编号
	Messages *MessageService
	// Sleep 为 nil 时使用 time.Timer
	Sleep SleepFunc
	// TypingSpeed 对齐 calculate_typing_time 的 typing_speed，未设置按 1 处理
	TypingSpeed float64
	// TypingChineseTimeSec / TypingEnglishTimeSec 未设置时按 0.3 / 0.15 处理
	TypingChineseTimeSec float64
	TypingEnglishTimeSec float64

	// sentHook 实例级发送后钩子，优先于包级默认钩子
	sentHook ServiceSentMessageHook
}

func NewSendService(manager *platformio.Manager, db *gorm.DB, botName string, accounts *BotAccountService) *SendService {
	return &SendService{Manager: manager, DB: db, BotName: strings.TrimSpace(botName), Accounts: accounts}
}

// SetBotName 注入机器人昵称，已发送消息落库时写入 Nickname
func (s *SendService) SetBotName(name string) error {
	if s == nil {
		return errors.New("发送服务未初始化")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("bot 昵称不能为空")
	}
	s.BotName = name
	return nil
}

// SetMaisakaHistoryAppender 注入 Maisaka 历史写入端口
func (s *SendService) SetMaisakaHistoryAppender(appender MaisakaHistoryAppender) error {
	if s == nil {
		return errors.New("发送服务未初始化")
	}
	if appender == nil {
		return errors.New("Maisaka 历史 appender 不能为空")
	}
	s.History = appender
	return nil
}

// SetSentMessageHook 注入实例级发送后钩子；传 nil 时回退包级默认钩子
func (s *SendService) SetSentMessageHook(hook ServiceSentMessageHook) error {
	if s == nil {
		return errors.New("发送服务未初始化")
	}
	s.sentHook = hook
	return nil
}

// buildSendPlainText 构造出站纯文本摘要
//
// 对齐 Python _build_processed_plain_text：显式传入正文优先，
// set_reply 时改由调用方按含引用段的组件序列重建。
func buildSendPlainText(request SendRequest) string {
	if text := strings.TrimSpace(request.PlainText); text != "" {
		return text
	}
	return plainTextFromSegments(request.Segments)
}

// plainTextFromSegments 按组件序列拼装纯文本摘要
func plainTextFromSegments(segments []platformio.Segment) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment.Kind {
		case platformio.SegmentText:
			parts = append(parts, segment.Text)
		case platformio.SegmentAt:
			parts = append(parts, "@"+segment.AtUserID)
		case platformio.SegmentImage:
			parts = append(parts, "[图片]")
		case platformio.SegmentEmoji:
			parts = append(parts, "[表情]")
		case platformio.SegmentVoice, platformio.SegmentRecord:
			parts = append(parts, "[语音]")
		case platformio.SegmentVideo:
			parts = append(parts, "[视频]")
		case platformio.SegmentFile:
			parts = append(parts, "[文件]")
		case platformio.SegmentForward:
			parts = append(parts, "[合并转发]")
		case platformio.SegmentReply:
			// 对齐 Python _build_processed_plain_text：引用段用目标正文，缺失时给占位
			parts = append(parts, firstNonEmpty(segment.Extra["target_message_content"], "[回复消息]"))
		case platformio.SegmentDict:
			parts = append(parts, dictSegmentSummary(segment.Data))
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// dictSegmentSummary 对齐 Python _build_processed_plain_text 的 dict 段摘要
func dictSegmentSummary(data any) string {
	payload, ok := data.(map[string]any)
	if !ok {
		return "[自定义消息]"
	}
	if rawType := strings.TrimSpace(fmt.Sprint(payload["type"])); rawType != "" && rawType != "<nil>" {
		return "[" + rawType + "消息]"
	}
	return "[自定义消息]"
}

// describeMessageSequence 生成消息段序列的简短类型描述
//
// 对齐 Python send_service.py:402-442 `_describe_message_sequence`：
// 多段返回 "message_sequence"；dict 段返回负载里的 type（空则 "dict"）；
// 其余按 Python 组件类的 format_name 返回，未知类型返回 "unknown"。
func describeMessageSequence(segments []platformio.Segment) string {
	if len(segments) != 1 {
		return "message_sequence"
	}
	segment := segments[0]
	if segment.Kind == platformio.SegmentDict {
		if payload, ok := segment.Data.(map[string]any); ok {
			if customType := strings.TrimSpace(fmt.Sprint(payload["type"])); customType != "" && customType != "<nil>" {
				return customType
			}
		}
		return "dict"
	}
	// 各组件 format_name 取值对齐 MaiBot 的 message_component 定义
	formatNames := map[platformio.SegmentKind]string{
		platformio.SegmentText:    "text",
		platformio.SegmentImage:   "image",
		platformio.SegmentEmoji:   "emoji",
		platformio.SegmentVoice:   "voice",
		platformio.SegmentRecord:  "voice",
		platformio.SegmentFile:    "file",
		platformio.SegmentAt:      "at",
		platformio.SegmentReply:   "reply",
		platformio.SegmentForward: "forward_node",
	}
	if name, ok := formatNames[segment.Kind]; ok {
		return name
	}
	return "unknown"
}

// buildOutboundLogPreview 构造出站消息的日志预览文本
//
// 对齐 Python send_service.py:497-514 `_build_outbound_log_preview`：
// 优先用已处理文本，为空时退到组件类型描述；空白折叠后按 maxLength 截断并补 "..."
func buildOutboundLogPreview(plainText string, segments []platformio.Segment, maxLength int) string {
	preview := strings.TrimSpace(plainText)
	if preview == "" {
		preview = "[" + describeMessageSequence(segments) + "]"
	}
	normalized := strings.Join(strings.Fields(preview), " ")
	runes := []rune(normalized)
	if len(runes) <= maxLength {
		return normalized
	}
	return string(runes[:maxLength]) + "..."
}

// Send 发送一条消息；失败返回真实错误，不伪造成功
func (s *SendService) Send(ctx context.Context, request SendRequest, options SendOptions) (SendResult, error) {
	if strings.TrimSpace(request.MessageID) == "" {
		return SendResult{}, errors.New("消息缺少 message_id，无法发送")
	}
	if s == nil || s.Manager == nil {
		return SendResult{}, errors.New("Platform IO 发送管理器未接线")
	}
	if strings.TrimSpace(request.Platform) == "" {
		return SendResult{}, errors.New("消息缺少 platform，无法发送")
	}
	if strings.TrimSpace(request.TargetID) == "" {
		return SendResult{}, errors.New("消息缺少 target_id，无法发送")
	}
	if options.SetReply && strings.TrimSpace(options.ReplyMessageID) == "" {
		return SendResult{}, errors.New("set_reply=True 时必须提供 reply_message_id")
	}
	if err := s.Manager.EnsureSendPipelineReady(ctx); err != nil {
		return SendResult{}, fmt.Errorf("准备 Platform IO 发送管线失败: %w", err)
	}
	routeKey, err := (platformio.RouteKey{Platform: request.Platform, AccountID: request.AccountID, Scope: request.Scope}).Normalize()
	if err != nil {
		return SendResult{}, err
	}
	chatType := request.ChatType
	if chatType == "" {
		chatType = platformio.ChatTypeGroup
	}
	segments := append([]platformio.Segment(nil), request.Segments...)
	if len(segments) == 0 {
		return SendResult{}, errors.New("消息缺少可发送的消息段")
	}
	// 出站标记按插入引用段之前的组件判定，对齐 Python 在 _prepare_message_for_platform_io 前算 flags
	flags := detectOutboundMessageFlags(segments)
	request.ReplyPreview = s.resolveReplyPreview(ctx, request, options)
	segments = withReplySegment(segments, options, request)
	plainText := buildSendPlainText(request)
	if options.SetReply {
		// 对齐 Python：set_reply 时按含引用段的组件序列重建纯文本
		plainText = plainTextFromSegments(segments)
	}
	if options.Typing {
		duration := s.typingDuration(request, plainText)
		if err := s.sleep(ctx, duration); err != nil {
			return SendResult{}, fmt.Errorf("模拟打字等待失败: %w", err)
		}
	}
	var groupInfo *platformio.OutboundGroupInfo
	if chatType == platformio.ChatTypeGroup {
		groupInfo = &platformio.OutboundGroupInfo{GroupID: request.TargetID}
	}
	envelope := platformio.OutboundMessageEnvelope{
		InternalMessageID: request.MessageID, RouteKey: routeKey, ChatType: chatType,
		TargetID: request.TargetID, Segments: segments, PlainText: plainText,
		ReplyToMessageID: options.ReplyMessageID, ExternalReplyToMessageID: request.ExternalReplyToMessageID,
		Timestamp: time.Now(),
		SessionID: request.SessionID,
		MessageInfo: platformio.OutboundMessageInfo{
			UserInfo:  platformio.OutboundUserInfo{UserID: request.AccountID, UserNickname: s.BotName},
			GroupInfo: groupInfo,
		},
		IsEmoji:   flags.IsEmoji,
		IsPicture: flags.IsPicture,
		IsCommand: flags.IsCommand,
	}
	batch := s.Manager.SendMessage(ctx, envelope, routeKey)
	if !batch.HasSuccess() {
		details := make([]string, 0, len(batch.FailedReceipts()))
		for _, receipt := range batch.FailedReceipts() {
			details = append(details, fmt.Sprintf("driver=%s status=%s error=%s", receipt.DriverID, receipt.Status, receipt.Error))
		}
		if len(details) == 0 {
			details = append(details, "未命中任何发送路由")
		}
		return SendResult{Batch: batch}, fmt.Errorf("Platform IO 发送失败: platform=%s %s", routeKey.Platform, strings.Join(details, "; "))
	}
	sent := batch.SentReceipts()
	finalID := request.MessageID
	platformMessageID := ""
	if len(sent) > 0 {
		if external := strings.TrimSpace(sent[0].ExternalMessageID); external != "" {
			platformMessageID = external
			finalID = external
		}
	}
	if options.StorageMessage {
		if err := s.storeSentMessage(ctx, request, finalID, platformMessageID); err != nil {
			return SendResult{}, err
		}
	}
	s.recordSentEmojiUsage(ctx, segments)
	historySynced := false
	if options.SyncToMaisakaHistory {
		historySynced = s.syncSentMessageToHistory(ctx, request, finalID, platformMessageID, plainText, options.MaisakaSourceKind)
	}
	s.notifySentMessage(ctx, request, finalID, platformMessageID, plainText, string(chatType))
	// 对齐 Python send_service.py:1127 的 debug 日志：按组件类型描述本次发送
	zap.L().Debug("发送消息",
		zap.String("sequence", describeMessageSequence(request.Segments)),
		zap.String("session_id", request.SessionID),
		zap.String("preview", buildOutboundLogPreview(plainText, request.Segments, 160)))
	return SendResult{MessageID: finalID, PlatformMessageID: platformMessageID, HistorySynced: historySynced, Batch: batch}, nil
}

// notifySentMessage 在成功发送并完成落库/历史同步后通知记忆自动化
// 钩子失败只记录真实错误，不回滚已成功的发送
func (s *SendService) notifySentMessage(ctx context.Context, request SendRequest, messageID, platformMessageID, plainText, chatType string) {
	hook := s.sentHook
	if hook == nil {
		hook = currentDefaultSendMessageHook()
	}
	if hook == nil {
		return
	}
	event := ServiceSentMessage{
		MessageID: messageID, PlatformMessageID: platformMessageID, SessionID: request.SessionID,
		Platform: request.Platform, AccountID: request.AccountID, Scope: request.Scope,
		ChatType: chatType, TargetID: request.TargetID, Text: plainText,
		ReplyToMessageID: request.ReplyToMessageID, Timestamp: time.Now(),
	}
	if err := hook.OnServiceMessageSent(ctx, event); err != nil {
		zap.L().Warn("发送后记忆自动化失败", zap.String("message_id", messageID), zap.Error(err))
	}
}

func (s *SendService) typingDuration(request SendRequest, plainText string) time.Duration {
	speed := s.TypingSpeed
	if speed <= 0 {
		speed = 1
	}
	chinese := s.TypingChineseTimeSec
	if chinese <= 0 {
		chinese = 0.3
	}
	english := s.TypingEnglishTimeSec
	if english <= 0 {
		english = 0.15
	}
	hasText, hasImage := false, false
	for _, segment := range request.Segments {
		switch segment.Kind {
		case platformio.SegmentText:
			if strings.TrimSpace(segment.Text) != "" {
				hasText = true
			}
		case platformio.SegmentImage, platformio.SegmentEmoji:
			hasImage = true
		}
	}
	return calculateSendTypingDuration(plainText, request.Kind == "emoji", hasImage && !hasText, chinese, english, speed)
}

func calculateSendTypingDuration(input string, isEmoji, isImage bool, chineseTime, englishTime, typingSpeed float64) time.Duration {
	if typingSpeed <= 0 {
		return 0
	}
	trimmed := strings.TrimSpace(input)
	chineseCount := 0
	for _, r := range input {
		if r >= 0x4E00 && r <= 0x9FFF {
			chineseCount++
		}
	}
	if !isEmoji && !isImage && chineseCount == 1 && len([]rune(trimmed)) == 1 {
		return time.Duration((chineseTime*3 + 0.3) * float64(time.Second))
	}
	var total float64
	for _, r := range input {
		if r >= 0x4E00 && r <= 0x9FFF {
			total += chineseTime
		} else {
			total += englishTime
		}
	}
	if isEmoji || isImage {
		total = 1
	}
	return time.Duration(total * typingSpeed * float64(time.Second))
}

func (s *SendService) sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	if s.Sleep != nil {
		return s.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// recordSentEmojiUsage 发送成功后按表情段记录使用次数，对齐 _record_sent_emoji_usage
func (s *SendService) recordSentEmojiUsage(ctx context.Context, segments []platformio.Segment) {
	if s == nil || len(segments) == 0 {
		return
	}
	registry := emojisystem.DefaultRegistry()
	for _, segment := range segments {
		sticker := segment.Kind == platformio.SegmentEmoji ||
			(segment.Kind == platformio.SegmentImage && strings.TrimSpace(segment.Extra["sub_type"]) == "1")
		if !sticker {
			continue
		}
		path := strings.TrimSpace(segment.Extra["file"])
		if path == "" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			zap.L().Warn("读取已发送表情包失败，跳过使用计数", zap.String("path", path), zap.Error(err))
			continue
		}
		sum := sha256.Sum256(content)
		if _, err := registry.RecordUsageByHash(ctx, hex.EncodeToString(sum[:])); err != nil {
			zap.L().Warn("记录表情包使用次数失败", zap.String("path", path), zap.Error(err))
		}
	}
}

// firstNonEmpty 返回第一个非空字符串
// resolveReplyPreview 查询被引用消息，构造 reply 段的预览信息
//
// 对齐 Python _build_reply_component(reply_message)：只有查到的消息编号与目标一致才带预览，
// 查不到或存在多条同名消息时只保留引用编号。
func (s *SendService) resolveReplyPreview(ctx context.Context, request SendRequest, options SendOptions) ReplyPreview {
	if !options.SetReply || s == nil || s.Messages == nil {
		return ReplyPreview{}
	}
	target := strings.TrimSpace(options.ReplyMessageID)
	if target == "" {
		return ReplyPreview{}
	}
	rows, err := s.Messages.ListMessagesByID(ctx, request.Platform, request.SessionID, target)
	if err != nil || len(rows) != 1 {
		return ReplyPreview{}
	}
	row := rows[0]
	preview := ReplyPreview{
		Content:  strings.TrimSpace(row.ProcessedPlainText),
		Nickname: strings.TrimSpace(row.UserNickname),
		Cardname: strings.TrimSpace(row.UserCardname),
	}
	preview.SenderID = strings.TrimSpace(row.ExternalUserID)
	if preview.SenderID == "" && row.UserID > 0 {
		preview.SenderID = strconv.FormatInt(row.UserID, 10)
	}
	return preview
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// withReplySegment 在 set_reply 时补齐 reply 段与预览信息
//
// 对齐 Python _ensure_reply_component：首段已是同一目标的 reply 段时只补预览字段，
// 否则在首位插入新 reply 段；预览来自被引用消息，供下游展示与摘要使用。
func withReplySegment(segments []platformio.Segment, options SendOptions, request SendRequest) []platformio.Segment {
	if !options.SetReply {
		return segments
	}
	target := strings.TrimSpace(options.ReplyMessageID)
	if target == "" {
		return segments
	}
	preview := request.ReplyPreview
	applyPreview := func(extra map[string]string) map[string]string {
		if extra == nil {
			extra = map[string]string{}
		}
		setIfEmpty(extra, "target_message_content", preview.Content)
		setIfEmpty(extra, "target_message_sender_id", preview.SenderID)
		setIfEmpty(extra, "target_message_sender_nickname", preview.Nickname)
		setIfEmpty(extra, "target_message_sender_cardname", preview.Cardname)
		return extra
	}
	if len(segments) > 0 && segments[0].Kind == platformio.SegmentReply && replySegmentTarget(segments[0]) == target {
		segments[0].Extra = applyPreview(segments[0].Extra)
		return segments
	}
	reply := platformio.Segment{Kind: platformio.SegmentReply, Text: target, Extra: applyPreview(map[string]string{"id": target})}
	out := make([]platformio.Segment, 0, len(segments)+1)
	out = append(out, reply)
	out = append(out, segments...)
	return out
}

// replySegmentTarget 读取 reply 段的目标消息编号
func replySegmentTarget(segment platformio.Segment) string {
	if id := strings.TrimSpace(segment.Extra["id"]); id != "" {
		return id
	}
	return strings.TrimSpace(segment.Text)
}

// setIfEmpty 只在目标键为空时写入，对齐 Python 预览字段的「已有值不覆盖」语义
func setIfEmpty(extra map[string]string, key, value string) {
	if strings.TrimSpace(extra[key]) != "" {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		extra[key] = trimmed
	}
}

func (s *SendService) syncSentMessageToHistory(ctx context.Context, request SendRequest, messageID, platformMessageID, text, sourceKind string) bool {
	if s == nil || s.History == nil {
		zap.L().Warn("已发送消息未同步 Maisaka 历史：历史写入端口未接线",
			zap.String("session_id", request.SessionID),
			zap.String("message_id", messageID),
		)
		return false
	}
	// 对齐 Python send_service.py:1004 的 `maisaka_source_kind or "outbound_send"`；
	// 插件能力路径由调用方显式传 "plugin_send"，两条默认值不能合并
	if strings.TrimSpace(sourceKind) == "" {
		sourceKind = "outbound_send"
	}
	entry := MaisakaSentMessage{
		SessionID: request.SessionID, MessageID: messageID, PlatformMessageID: platformMessageID,
		Platform: request.Platform, AccountID: request.AccountID, Scope: request.Scope,
		TargetID: request.TargetID, ChatType: string(request.ChatType), Text: text,
		SourceKind: sourceKind, SpeakerName: s.BotName, Timestamp: time.Now(),
		ReplyToMessageID: firstNonEmpty(request.ExternalReplyToMessageID, request.ReplyToMessageID),
	}
	if err := s.History.AppendSentMessage(ctx, entry); err != nil {
		zap.L().Warn("已发送消息同步 Maisaka 历史失败",
			zap.String("session_id", request.SessionID),
			zap.String("message_id", messageID),
			zap.Error(err),
		)
		return false
	}
	return true
}

func (s *SendService) storeSentMessage(ctx context.Context, request SendRequest, messageID, platformMessageID string) error {
	if s == nil || s.DB == nil {
		return errors.New("已发送消息数据库未接线")
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return nil
	}
	row := s.buildSentMessageLog(request, messageID, platformMessageID)
	if err := s.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("写入已发送消息失败: %w", err)
	}
	return nil
}

// buildSentMessageLog 组装落库行，便于测试 Nickname 等字段
func (s *SendService) buildSentMessageLog(request SendRequest, messageID, platformMessageID string) memory.MessageLog {
	numericMessageID, _ := strconv.ParseInt(platformMessageID, 10, 64)
	if numericMessageID == 0 {
		numericMessageID = onebot.SyntheticIdentityID("send_service", request.Platform, request.AccountID, request.Scope, request.SessionID, messageID)
	}
	userID, _ := strconv.ParseInt(request.AccountID, 10, 64)
	if userID == 0 {
		userID = onebot.SyntheticIdentityID("send_service_user", request.Platform, request.AccountID)
	}
	targetID, _ := strconv.ParseInt(request.TargetID, 10, 64)
	if targetID == 0 {
		targetID = onebot.SyntheticIdentityID("send_service_target", request.Platform, request.TargetID)
	}
	text := buildSendPlainText(request)
	flags := detectOutboundMessageFlags(request.Segments)
	return memory.MessageLog{
		MessageID: numericMessageID, ConversationKind: string(request.ChatType), TargetID: targetID, UserID: userID,
		UserNickname: s.BotName, ProcessedPlainText: text,
		Timestamp: time.Now(), Platform: request.Platform, AccountID: request.AccountID, Scope: request.Scope,
		SessionID: request.SessionID, ExternalMessageID: platformMessageID, ExternalTargetID: request.TargetID,
		ExternalUserID: request.AccountID,
		// 出站标记对齐 Python send_service.py:592 的 _detect_outbound_message_flags
		IsEmoji: flags.IsEmoji, IsPicture: flags.IsPicture, IsCommand: flags.IsCommand,
	}
}

// outboundMessageFlags 出站消息的组件类型标记，对应 Python 的 flags 字典
type outboundMessageFlags struct {
	IsEmoji   bool
	IsPicture bool
	IsCommand bool
}

// detectOutboundMessageFlags 按消息段推断出站标记
//
// 对齐 Python send_service.py:374-397 `_detect_outbound_message_flags`：
// 只有单段消息才判定，多段一律三项全 false；emoji/picture 按组件类型，
// command 只认 dict 段负载里 type == "command"（去空白、转小写后比较）。
func detectOutboundMessageFlags(segments []platformio.Segment) outboundMessageFlags {
	if len(segments) != 1 {
		return outboundMessageFlags{}
	}
	segment := segments[0]
	flags := outboundMessageFlags{
		IsEmoji:   segment.Kind == platformio.SegmentEmoji,
		IsPicture: segment.Kind == platformio.SegmentImage,
	}
	if segment.Kind == platformio.SegmentDict {
		flags.IsCommand = dictSegmentCommandType(segment.Data)
	}
	return flags
}

// dictSegmentCommandType 判断 dict 段负载的类型名是否为 command
func dictSegmentCommandType(data any) bool {
	payload, ok := data.(map[string]any)
	if !ok {
		return false
	}
	raw, ok := payload["type"]
	if !ok || raw == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(raw)), "command")
}

// TextToStream 构建文本消息并发送
func (s *SendService) TextToStream(ctx context.Context, text, sessionID, platform, accountID, scope, targetID string, options SendOptions) (SendResult, error) {
	request := SendRequest{
		MessageID: fmt.Sprintf("send_api_%d", time.Now().UnixMilli()), SessionID: sessionID, Platform: platform,
		AccountID: accountID, Scope: scope, TargetID: targetID,
		Segments: []platformio.Segment{{Kind: platformio.SegmentText, Text: text}}, PlainText: text,
	}
	return s.Send(ctx, request, options)
}
