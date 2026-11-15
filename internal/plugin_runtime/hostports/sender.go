package hostports

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime"
	"MaiGo/internal/services"
)

// pluginSendService 插件发送所依赖的真实发送服务；测试可注入内存实现
type pluginSendService interface {
	Send(ctx context.Context, request services.SendRequest, options services.SendOptions) (services.SendResult, error)
}

// sessionResolver 按 stream_id 解析会话；生产由 Sessions 实现
type sessionResolver interface {
	Record(ctx context.Context, sessionID string) (*chatsession.Record, error)
}

// Sender 插件发送端口，持有真实 SendService 完成落库、打字等待与 Maisaka 历史同步
type Sender struct {
	manager  *platformio.Manager
	sessions sessionResolver
	send     pluginSendService
	tempRoot string
	botName  string
}

// NewSender 构造插件发送端口
func NewSender(manager *platformio.Manager, sessions *Sessions, tempRoot string) *Sender {
	if strings.TrimSpace(tempRoot) == "" {
		tempRoot = filepath.Join(os.TempDir(), "maibot-plugin-send")
	}
	sender := &Sender{manager: manager, sessions: sessions, tempRoot: tempRoot}
	if sessions != nil {
		service := services.NewSendService(manager, sessions.db, "", nil)
		// 引用预览需要按消息编号回查，对齐 Python _build_reply_component(reply_message)
		service.Messages = services.NewMessageService(sessions.db, 0, "")
		sender.send = service
	}
	if sessions != nil && sessions.avatar == nil && manager != nil {
		// 头像缓存对齐 Python get_instance_root：优先 MAIBOT_INSTANCE_DIR，否则当前工作目录
		service := platformio.NewAvatarService(pluginruntime.ResolveInstanceRoot())
		service.Provider = NewAdapterAvatarProvider(manager)
		sessions.SetAvatarService(service)
	}
	return sender
}

// SetMaisakaHistoryAppender 注入 Maisaka 历史写入端口；未注入时 SendService 只告警
func (s *Sender) SetMaisakaHistoryAppender(appender services.MaisakaHistoryAppender) error {
	if s == nil {
		return fmt.Errorf("发送端口未初始化")
	}
	if appender == nil {
		return fmt.Errorf("Maisaka 历史 appender 不能为空")
	}
	if s.send == nil {
		return fmt.Errorf("发送端口未接线：发送服务未初始化")
	}
	setter, ok := s.send.(interface {
		SetMaisakaHistoryAppender(services.MaisakaHistoryAppender) error
	})
	if !ok {
		return fmt.Errorf("发送服务不支持注入 Maisaka 历史 appender")
	}
	return setter.SetMaisakaHistoryAppender(appender)
}

// SetBotName 注入机器人昵称，保证已发送消息落库时 Nickname 非空
func (s *Sender) SetBotName(name string) error {
	if s == nil {
		return fmt.Errorf("发送端口未初始化")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("bot 昵称不能为空")
	}
	if s.send == nil {
		return fmt.Errorf("发送端口未接线：发送服务未初始化")
	}
	setter, ok := s.send.(interface {
		SetBotName(string) error
	})
	if !ok {
		return fmt.Errorf("发送服务不支持注入 bot 昵称")
	}
	if err := setter.SetBotName(name); err != nil {
		return err
	}
	s.botName = name
	return nil
}

// BotName 返回已注入的机器人昵称
func (s *Sender) BotName() string {
	if s == nil {
		return ""
	}
	return s.botName
}

// SendPluginMessage 实现 pluginruntime.PluginSendPort
func (s *Sender) SendPluginMessage(ctx context.Context, req pluginruntime.PluginSendRequest) (pluginruntime.PluginSendResult, error) {
	if s == nil || s.manager == nil {
		return pluginruntime.PluginSendResult{}, fmt.Errorf("发送端口未接线：Platform IO 未初始化")
	}
	if s.sessions == nil {
		return pluginruntime.PluginSendResult{}, fmt.Errorf("发送端口未接线：会话服务未初始化")
	}
	if s.send == nil {
		return pluginruntime.PluginSendResult{}, fmt.Errorf("发送端口未接线：发送服务未初始化")
	}
	if err := validateSendReply(req.SetReply, req.ReplyMessageID); err != nil {
		return pluginruntime.PluginSendResult{}, err
	}
	session, err := s.sessions.Record(ctx, req.StreamID)
	if err != nil {
		return pluginruntime.PluginSendResult{}, err
	}
	accountID, scope := "", ""
	if session.AccountID != nil {
		accountID = strings.TrimSpace(*session.AccountID)
	}
	if session.Scope != nil {
		scope = strings.TrimSpace(*session.Scope)
	}
	chatType := platformio.ChatTypePrivate
	targetID := ""
	if session.IsGroup() {
		chatType = platformio.ChatTypeGroup
		if session.GroupID != nil {
			targetID = strings.TrimSpace(*session.GroupID)
		}
	} else if session.UserID != nil {
		targetID = strings.TrimSpace(*session.UserID)
	}
	if targetID == "" {
		return pluginruntime.PluginSendResult{}, fmt.Errorf("会话 %s 缺少目标 ID", req.StreamID)
	}
	segments, err := s.segmentsForRequest(req)
	if err != nil {
		return pluginruntime.PluginSendResult{}, err
	}
	sessionID := strings.TrimSpace(session.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.StreamID)
	}
	typing := req.Typing != nil && *req.Typing
	storage := req.StorageMessage == nil || *req.StorageMessage
	syncHistory := req.SyncToMaisakaHistory != nil && *req.SyncToMaisakaHistory
	sourceKind := strings.TrimSpace(req.SourceKind)
	if sourceKind == "" {
		sourceKind = "plugin_send"
	}
	request := services.SendRequest{
		MessageID:        fmt.Sprintf("plugin_send_%s_%d", req.PluginID, time.Now().UnixNano()),
		SessionID:        sessionID,
		Platform:         session.Platform,
		AccountID:        accountID,
		Scope:            scope,
		ChatType:         chatType,
		TargetID:         targetID,
		Segments:         segments,
		PlainText:        plainTextForRequest(req),
		ReplyToMessageID: req.ReplyMessageID,
		Kind:             req.Kind,
	}
	options := services.SendOptions{
		Typing:               typing,
		SetReply:             req.SetReply,
		ReplyMessageID:       req.ReplyMessageID,
		StorageMessage:       storage,
		SyncToMaisakaHistory: syncHistory,
		MaisakaSourceKind:    sourceKind,
	}
	result, err := s.send.Send(ctx, request, options)
	if err != nil {
		return pluginruntime.PluginSendResult{}, err
	}
	return pluginruntime.PluginSendResult{Sent: true, MessageID: result.MessageID}, nil
}

// validateSendReply 校验引用回复参数；Python 侧发送前同样要求原消息 ID
func validateSendReply(setReply bool, replyMessageID string) error {
	if setReply && strings.TrimSpace(replyMessageID) == "" {
		return fmt.Errorf("set_reply=True 时必须提供 reply_message_id")
	}
	return nil
}

func (s *Sender) segmentsForRequest(req pluginruntime.PluginSendRequest) ([]platformio.Segment, error) {
	switch req.Kind {
	case "text":
		return []platformio.Segment{{Kind: platformio.SegmentText, Text: req.Text}}, nil
	case "image", "emoji":
		seg, err := s.imageSegment(req.Base64, req.Kind == "emoji")
		if err != nil {
			return nil, err
		}
		return []platformio.Segment{seg}, nil
	case "hybrid":
		return s.segmentsFromMaps(req.HybridNodes)
	case "forward":
		nodes, err := platformio.ForwardNodesFromPluginMessages(req.ForwardNodes, s.segmentFromMap)
		if err != nil {
			return nil, err
		}
		return []platformio.Segment{{Kind: platformio.SegmentForward, ForwardNodes: nodes}}, nil
	case "command":
		return []platformio.Segment{{Kind: platformio.SegmentDict, Data: map[string]any{"type": "command", "data": req.Command}}}, nil
	case "custom":
		seg, err := s.customSegment(req)
		if err != nil {
			return nil, err
		}
		return []platformio.Segment{seg}, nil
	default:
		return nil, fmt.Errorf("未知的发送类型: %s", req.Kind)
	}
}

func (s *Sender) imageSegment(encoded string, sticker bool) (platformio.Segment, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return platformio.Segment{}, fmt.Errorf("图片 Base64 解码失败: %w", err)
	}
	if err := os.MkdirAll(s.tempRoot, 0o755); err != nil {
		return platformio.Segment{}, err
	}
	file, err := os.CreateTemp(s.tempRoot, "plugin-*.png")
	if err != nil {
		return platformio.Segment{}, err
	}
	path := file.Name()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return platformio.Segment{}, err
	}
	if err := file.Close(); err != nil {
		return platformio.Segment{}, err
	}
	seg := platformio.Segment{Kind: platformio.SegmentImage, Extra: map[string]string{"file": path}}
	if sticker {
		seg.Extra["sub_type"] = "1"
	}
	return seg, nil
}

func (s *Sender) segmentsFromMaps(nodes []map[string]any) ([]platformio.Segment, error) {
	out := make([]platformio.Segment, 0, len(nodes))
	for _, node := range nodes {
		seg, err := s.segmentFromMap(node)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("segments 为空")
	}
	return out, nil
}

func (s *Sender) segmentFromMap(node map[string]any) (platformio.Segment, error) {
	kind := strings.ToLower(strings.TrimSpace(asString(node["type"])))
	switch kind {
	case "text":
		text := asString(node["data"])
		if text == "" {
			text = asString(node["content"])
		}
		return platformio.Segment{Kind: platformio.SegmentText, Text: text}, nil
	case "image", "emoji":
		encoded := asString(node["binary_data_base64"])
		if encoded == "" {
			encoded = asString(node["content"])
		}
		if encoded != "" {
			return s.imageSegment(encoded, kind == "emoji")
		}
		if url := asString(node["data"]); strings.HasPrefix(url, "http") {
			return platformio.Segment{Kind: platformio.SegmentImage, URL: url}, nil
		}
		return platformio.Segment{}, fmt.Errorf("图片段缺少 base64 或 URL")
	case "at":
		return platformio.Segment{Kind: platformio.SegmentAt, AtUserID: firstNonEmpty(asString(node["data"]), asString(node["user_id"]))}, nil
	case "reply":
		targetID := firstNonEmpty(asString(node["target_message_id"]), customDataValue(node["data"], "target_message_id"))
		if targetID == "" {
			return platformio.Segment{}, fmt.Errorf("reply 段缺少 target_message_id")
		}
		return platformio.Segment{Kind: platformio.SegmentReply, Text: targetID, Extra: map[string]string{"id": targetID}}, nil
	case "face":
		return platformio.Segment{Kind: platformio.SegmentFace, Text: asString(node["data"])}, nil
	default:
		return platformio.Segment{}, fmt.Errorf("hybrid 不支持的段类型: %s", kind)
	}
}

func plainTextForRequest(req pluginruntime.PluginSendRequest) string {
	if strings.TrimSpace(req.Text) != "" {
		return req.Text
	}
	if strings.TrimSpace(req.ProcessedPlainText) != "" {
		return req.ProcessedPlainText
	}
	if strings.TrimSpace(req.Command) != "" {
		return req.Command
	}
	if text, ok := req.Content.(string); ok {
		return text
	}
	return ""
}

// customSegment 对齐 _build_message_sequence_from_custom_message 的常用分支
func (s *Sender) customSegment(req pluginruntime.PluginSendRequest) (platformio.Segment, error) {
	messageType := strings.ToLower(strings.TrimSpace(req.MessageType))
	content := req.Content
	if content == nil {
		content = req.Data
	}
	switch messageType {
	case "text":
		return platformio.Segment{Kind: platformio.SegmentText, Text: asString(content)}, nil
	case "image", "emoji":
		encoded := asString(content)
		if encoded == "" {
			return platformio.Segment{}, fmt.Errorf("自定义 %s 消息缺少 base64 内容", messageType)
		}
		return s.imageSegment(encoded, messageType == "emoji")
	case "at":
		return platformio.Segment{Kind: platformio.SegmentAt, AtUserID: firstNonEmpty(customDataValue(content, "target_user_id"), asString(content))}, nil
	case "reply":
		targetID := firstNonEmpty(customDataValue(content, "target_message_id"), asString(content))
		return platformio.Segment{Kind: platformio.SegmentReply, Text: targetID, Extra: map[string]string{"id": targetID}}, nil
	case "dict":
		return platformio.Segment{Kind: platformio.SegmentDict, Data: content}, nil
	default:
		return platformio.Segment{Kind: platformio.SegmentDict, Data: map[string]any{"type": messageType, "data": content}}, nil
	}
}

func customDataValue(value any, key string) string {
	if data, ok := value.(map[string]any); ok {
		return asString(data[key])
	}
	return ""
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
