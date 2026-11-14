package chatwiring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/emoji_system"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/maim_bridge"
)

// ImageFileResolver 把图片 hash 解析为可发送的文件路径或 URL
type ImageFileResolver interface {
	ResolveImageFile(ctx context.Context, hash string) (string, error)
}

// PlatformSender 旧版平台发送端口，走 Platform IO 统一出站
type PlatformSender struct {
	Manager *platformio.Manager
	Images  ImageFileResolver
}

// SendMessage 实现 messagereceive.LegacySender
func (s *PlatformSender) SendMessage(ctx context.Context, message *messagereceive.SessionMessage) (bool, error) {
	if s == nil || s.Manager == nil {
		return false, errors.New("Platform IO 发送管理器未接线")
	}
	if message == nil {
		return false, errors.New("消息不能为空")
	}
	if err := s.Manager.EnsureSendPipelineReady(ctx); err != nil {
		return false, fmt.Errorf("平台发送管线未就绪: %w", err)
	}
	outbound, route, err := s.buildOutbound(ctx, message)
	if err != nil {
		return false, err
	}
	batch := s.Manager.SendMessage(ctx, outbound, route)
	if batch.HasSuccess() {
		s.recordSentEmojiUsage(ctx, message)
		return true, nil
	}
	failed := batch.FailedReceipts()
	if len(failed) == 0 {
		return false, fmt.Errorf("平台发送未命中任何路由: %s", route.Platform)
	}
	reasons := make([]string, 0, len(failed))
	for _, receipt := range failed {
		reason := strings.TrimSpace(receipt.Error)
		if reason == "" {
			reason = string(receipt.Status)
		}
		reasons = append(reasons, reason)
	}
	return false, fmt.Errorf("平台发送失败: %s", strings.Join(reasons, "; "))
}

// recordSentEmojiUsage 发送成功后记录顶层表情包使用次数，对齐 _record_sent_emoji_usage
func (s *PlatformSender) recordSentEmojiUsage(ctx context.Context, message *messagereceive.SessionMessage) {
	if message == nil || message.RawMessage == nil {
		return
	}
	registry := emojisystem.DefaultRegistry()
	for _, component := range message.RawMessage.Components {
		emoji, ok := component.(messagecomponent.EmojiComponent)
		if !ok {
			continue
		}
		hash := strings.TrimSpace(emoji.Hash)
		if hash == "" {
			continue
		}
		if _, err := registry.RecordUsageByHash(ctx, hash); err != nil {
			zap.L().Warn("记录表情包使用次数失败", zap.String("emoji_hash", hash), zap.Error(err))
		}
	}
}

func (s *PlatformSender) buildOutbound(ctx context.Context, message *messagereceive.SessionMessage) (platformio.OutboundMessageEnvelope, platformio.RouteKey, error) {
	route, err := (platformio.RouteKey{
		Platform:  message.Platform,
		AccountID: pointerText(message.AccountID),
		Scope:     pointerText(message.Scope),
	}).Normalize()
	if err != nil {
		return platformio.OutboundMessageEnvelope{}, platformio.RouteKey{}, fmt.Errorf("平台路由无效: %w", err)
	}
	targetID := strings.TrimSpace(message.TargetID())
	if targetID == "" {
		return platformio.OutboundMessageEnvelope{}, platformio.RouteKey{}, errors.New("消息缺少发送目标")
	}
	segments, err := s.segments(ctx, message)
	if err != nil {
		return platformio.OutboundMessageEnvelope{}, platformio.RouteKey{}, err
	}
	envelope := platformio.OutboundMessageEnvelope{
		InternalMessageID: message.MessageID,
		RouteKey:          route,
		ChatType:          platformio.ChatTypePrivate,
		TargetID:          targetID,
		Segments:          segments,
		PlainText:         message.PlainText(),
		Metadata:          map[string]any{"session_id": message.SessionID},
		Timestamp:         message.Timestamp,
		SessionID:         message.SessionID,
		MessageInfo: platformio.OutboundMessageInfo{
			UserInfo: platformio.OutboundUserInfo{
				UserID:       message.MessageInfo.UserInfo.UserID,
				UserNickname: message.MessageInfo.UserInfo.UserNickname,
				UserCardname: pointerText(message.MessageInfo.UserInfo.UserCardname),
			},
			AdditionalConfig: message.MessageInfo.AdditionalConfig,
		},
		IsMentioned: message.IsMentioned,
		IsAt:        message.IsAt,
		IsEmoji:     message.IsEmoji,
		IsPicture:   message.IsPicture,
		IsCommand:   message.IsCommand,
		IsNotify:    message.IsNotify,
	}
	if message.MessageInfo.GroupInfo != nil {
		envelope.MessageInfo.GroupInfo = &platformio.OutboundGroupInfo{
			GroupID:   message.MessageInfo.GroupInfo.GroupID,
			GroupName: message.MessageInfo.GroupInfo.GroupName,
		}
	}
	if !message.ReceivedAt.IsZero() {
		receivedAt := message.ReceivedAt
		envelope.ReceivedAt = &receivedAt
	}
	if message.IsGroup() {
		envelope.ChatType = platformio.ChatTypeGroup
	}
	if replyTo := pointerText(message.ReplyTo); replyTo != "" {
		envelope.ExternalReplyToMessageID = replyTo
		if _, err := strconv.ParseInt(replyTo, 10, 64); err == nil {
			envelope.ReplyToMessageID = replyTo
		}
	}
	return envelope, route, nil
}

func (s *PlatformSender) segments(ctx context.Context, message *messagereceive.SessionMessage) ([]platformio.Segment, error) {
	if message.RawMessage == nil || len(message.RawMessage.Components) == 0 {
		text := strings.TrimSpace(message.PlainText())
		if text == "" {
			return nil, errors.New("消息没有可发送内容")
		}
		return []platformio.Segment{{Kind: platformio.SegmentText, Text: text}}, nil
	}
	segments := make([]platformio.Segment, 0, len(message.RawMessage.Components))
	for _, component := range message.RawMessage.Components {
		segment, err := s.segment(ctx, component)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return nil, errors.New("消息没有可发送消息段")
	}
	return segments, nil
}

func (s *PlatformSender) segment(ctx context.Context, component messagecomponent.Component) (platformio.Segment, error) {
	switch item := component.(type) {
	case messagecomponent.TextComponent:
		return platformio.Segment{Kind: platformio.SegmentText, Text: item.Text}, nil
	case messagecomponent.AtComponent:
		return platformio.Segment{Kind: platformio.SegmentAt, AtUserID: item.TargetUserID}, nil
	case messagecomponent.ReplyComponent:
		return platformio.Segment{Kind: platformio.SegmentReply, Extra: map[string]string{"id": item.TargetMessageID}}, nil
	case messagecomponent.ImageComponent:
		return s.mediaSegment(ctx, platformio.SegmentImage, item.Hash, item.Content)
	case messagecomponent.EmojiComponent:
		return s.mediaSegment(ctx, platformio.SegmentEmoji, item.Hash, item.Content)
	case messagecomponent.VoiceComponent:
		return s.mediaSegment(ctx, platformio.SegmentVoice, item.Hash, item.Content)
	case messagecomponent.FileComponent:
		file := strings.TrimSpace(item.URL)
		if file == "" {
			file = strings.TrimSpace(item.FileRef)
		}
		extra := map[string]string{}
		if file == "" && strings.TrimSpace(item.FileID) != "" {
			file = strings.TrimSpace(item.FileID)
			extra["file"] = file
		}
		if file == "" {
			return platformio.Segment{}, fmt.Errorf("文件 %q 缺少可发送地址", item.Name)
		}
		if name := strings.TrimSpace(item.Name); name != "" {
			extra["name"] = name
		}
		return platformio.Segment{Kind: platformio.SegmentFile, URL: file, Extra: extra}, nil
	case messagecomponent.DictComponent:
		return platformio.Segment{Kind: platformio.SegmentDict, Data: item.Data}, nil
	case messagecomponent.ForwardNodeComponent:
		return s.forwardSegment(ctx, item)
	case *messagecomponent.ForwardNodeComponent:
		if item == nil {
			return platformio.Segment{}, errors.New("合并转发节点为空")
		}
		return s.forwardSegment(ctx, *item)
	default:
		return platformio.Segment{}, fmt.Errorf("不支持的消息段类型 %s", component.FormatName())
	}
}

// forwardSegment 对齐 Python ForwardNodeComponent.to_seg：Seg(type="forward", data=节点消息列表)
func (s *PlatformSender) forwardSegment(ctx context.Context, item messagecomponent.ForwardNodeComponent) (platformio.Segment, error) {
	if len(item.ForwardComponents) == 0 {
		return platformio.Segment{}, errors.New("合并转发缺少消息节点")
	}
	nodes := make([]platformio.ForwardNode, 0, len(item.ForwardComponents))
	for index, node := range item.ForwardComponents {
		if len(node.Content) == 0 {
			return platformio.Segment{}, fmt.Errorf("合并转发第 %d 个节点缺少消息段", index+1)
		}
		segments := make([]platformio.Segment, 0, len(node.Content))
		for _, inner := range node.Content {
			if _, ok := inner.(messagecomponent.DictComponent); ok {
				continue
			}
			if ptr, ok := inner.(*messagecomponent.DictComponent); ok && ptr != nil {
				continue
			}
			segment, err := s.segment(ctx, inner)
			if err != nil {
				return platformio.Segment{}, fmt.Errorf("合并转发第 %d 个节点消息段无效: %w", index+1, err)
			}
			segments = append(segments, segment)
		}
		if len(segments) == 0 {
			return platformio.Segment{}, fmt.Errorf("合并转发第 %d 个节点缺少消息段", index+1)
		}
		nodes = append(nodes, platformio.ForwardNode{
			UserID:    pointerText(node.UserID),
			Nickname:  strings.TrimSpace(node.UserNickname),
			Cardname:  pointerText(node.UserCardname),
			MessageID: strings.TrimSpace(node.MessageID),
			Segments:  segments,
		})
	}
	return platformio.Segment{Kind: platformio.SegmentForward, ForwardNodes: nodes}, nil
}

func (s *PlatformSender) mediaSegment(ctx context.Context, kind platformio.SegmentKind, hash, content string) (platformio.Segment, error) {
	content = strings.TrimSpace(content)
	if looksLikeReference(content) {
		return platformio.Segment{Kind: kind, URL: content}, nil
	}
	if s.Images != nil && strings.TrimSpace(hash) != "" {
		resolved, err := s.Images.ResolveImageFile(ctx, strings.TrimSpace(hash))
		if err != nil {
			return platformio.Segment{}, fmt.Errorf("解析媒体文件失败: %w", err)
		}
		if strings.TrimSpace(resolved) != "" {
			return platformio.Segment{Kind: kind, URL: resolved}, nil
		}
	}
	return platformio.Segment{}, fmt.Errorf("媒体消息缺少可发送 URL（%s）", kind)
}

// APIServerFallback 旧 maim_message API Server 兜底发送端口
type APIServerFallback struct {
	Hub         maimbridge.Hub
	EnabledFunc func() bool
	RunningFunc func() bool
}

// Enabled 实现 messagereceive.APIServerFallback
func (f *APIServerFallback) Enabled() bool {
	if f == nil {
		return false
	}
	if f.EnabledFunc != nil {
		return f.EnabledFunc()
	}
	return f.Hub != nil
}

// Running 实现 messagereceive.APIServerFallback
func (f *APIServerFallback) Running() bool {
	if f == nil {
		return false
	}
	if f.RunningFunc != nil {
		return f.RunningFunc()
	}
	return f.Hub != nil
}

// HasPlatform 实现 messagereceive.APIServerFallback
func (f *APIServerFallback) HasPlatform(platform string) bool {
	if f == nil || f.Hub == nil {
		return false
	}
	return f.Hub.HasPlatform(platform)
}

// SendMessage 实现 messagereceive.APIServerFallback
func (f *APIServerFallback) SendMessage(ctx context.Context, message *messagereceive.SessionMessage) (map[string]bool, error) {
	if f == nil || f.Hub == nil {
		return map[string]bool{}, errors.New("API Server Hub 未接线")
	}
	if message == nil {
		return map[string]bool{}, errors.New("消息不能为空")
	}
	route, err := (platformio.RouteKey{
		Platform:  message.Platform,
		AccountID: pointerText(message.AccountID),
		Scope:     pointerText(message.Scope),
	}).Normalize()
	if err != nil {
		return map[string]bool{}, fmt.Errorf("API Server 路由无效: %w", err)
	}
	targetID := strings.TrimSpace(message.TargetID())
	if targetID == "" {
		return map[string]bool{}, errors.New("消息缺少发送目标")
	}
	segments, err := segmentsForFallback(message)
	if err != nil {
		return map[string]bool{}, err
	}
	envelope := platformio.OutboundMessageEnvelope{
		InternalMessageID: message.MessageID,
		RouteKey:          route,
		ChatType:          platformio.ChatTypePrivate,
		TargetID:          targetID,
		Segments:          segments,
		PlainText:         message.PlainText(),
		Timestamp:         message.Timestamp,
		MessageInfo: platformio.OutboundMessageInfo{
			UserInfo: platformio.OutboundUserInfo{
				UserID:       message.MessageInfo.UserInfo.UserID,
				UserNickname: message.MessageInfo.UserInfo.UserNickname,
				UserCardname: pointerText(message.MessageInfo.UserInfo.UserCardname),
			},
			AdditionalConfig: message.MessageInfo.AdditionalConfig,
		},
	}
	if message.IsGroup() {
		envelope.ChatType = platformio.ChatTypeGroup
		if message.MessageInfo.GroupInfo != nil {
			envelope.MessageInfo.GroupInfo = &platformio.OutboundGroupInfo{
				GroupID:   message.MessageInfo.GroupInfo.GroupID,
				GroupName: message.MessageInfo.GroupInfo.GroupName,
			}
		}
	}
	baseMessage, err := maimbridge.OutboundToMessageBase(ctx, envelope, route)
	if err != nil {
		return map[string]bool{}, fmt.Errorf("序列化 API Server 消息失败: %w", err)
	}
	payload, err := json.Marshal(baseMessage)
	if err != nil {
		return map[string]bool{}, fmt.Errorf("序列化 API Server 消息失败: %w", err)
	}
	results := map[string]bool{route.Platform: false}
	if routeHub, ok := f.Hub.(maimbridge.RouteHub); ok && (route.AccountID != "" || route.Scope != "") && routeHub.HasRoute(route) {
		if err := routeHub.SendToRoute(ctx, route, payload); err != nil {
			return results, fmt.Errorf("API Server 路由发送失败: %w", err)
		}
		results[route.Platform] = true
		return results, nil
	}
	if !f.Hub.HasPlatform(route.Platform) {
		return results, fmt.Errorf("API Server 没有平台 %s 的连接", route.Platform)
	}
	if err := f.Hub.SendToPlatform(ctx, route.Platform, payload); err != nil {
		return results, fmt.Errorf("API Server 平台发送失败: %w", err)
	}
	results[route.Platform] = true
	return results, nil
}

func segmentsForFallback(message *messagereceive.SessionMessage) ([]platformio.Segment, error) {
	if message.RawMessage == nil || len(message.RawMessage.Components) == 0 {
		text := strings.TrimSpace(message.PlainText())
		if text == "" {
			return nil, errors.New("消息没有可发送内容")
		}
		return []platformio.Segment{{Kind: platformio.SegmentText, Text: text}}, nil
	}
	segments := make([]platformio.Segment, 0, len(message.RawMessage.Components))
	for _, component := range message.RawMessage.Components {
		switch item := component.(type) {
		case messagecomponent.TextComponent:
			segments = append(segments, platformio.Segment{Kind: platformio.SegmentText, Text: item.Text})
		case messagecomponent.AtComponent:
			segments = append(segments, platformio.Segment{Kind: platformio.SegmentAt, AtUserID: item.TargetUserID})
		case messagecomponent.ReplyComponent:
			segments = append(segments, platformio.Segment{Kind: platformio.SegmentReply, Extra: map[string]string{"id": item.TargetMessageID}})
		case messagecomponent.ImageComponent:
			if !looksLikeReference(item.Content) {
				return nil, errors.New("API Server 图片缺少可发送 URL")
			}
			segments = append(segments, platformio.Segment{Kind: platformio.SegmentImage, URL: item.Content})
		case messagecomponent.FileComponent:
			url := strings.TrimSpace(item.URL)
			if url == "" {
				url = strings.TrimSpace(item.FileRef)
			}
			if url == "" {
				return nil, fmt.Errorf("API Server 文件 %q 缺少可发送地址", item.Name)
			}
			segments = append(segments, platformio.Segment{Kind: platformio.SegmentFile, URL: url, Extra: map[string]string{"name": item.Name}})
		case messagecomponent.ForwardNodeComponent:
			forward, err := fallbackForwardSegment(item)
			if err != nil {
				return nil, err
			}
			segments = append(segments, forward)
		case *messagecomponent.ForwardNodeComponent:
			if item == nil {
				return nil, errors.New("合并转发节点为空")
			}
			forward, err := fallbackForwardSegment(*item)
			if err != nil {
				return nil, err
			}
			segments = append(segments, forward)
		default:
			return nil, fmt.Errorf("API Server 不支持消息段 %s", component.FormatName())
		}
	}
	return segments, nil
}

func fallbackForwardSegment(item messagecomponent.ForwardNodeComponent) (platformio.Segment, error) {
	return (&PlatformSender{}).forwardSegment(context.Background(), item)
}
