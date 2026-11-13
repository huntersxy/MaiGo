package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/onebot"

	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
)

type resolvedPlatformRoute struct {
	RouteKey   platformio.RouteKey
	Target     string
	External   bool
	HasAdapter bool
}

// resolvePlatformRoute 解析会话的出站平台路由；非 QQ 外部身份优先使用原始字符串目标。
// 第二个返回值为 false 表示既没有可用外部路由，也没有可回退的 OneBot QQ 客户端。
func (a *Agent) resolvePlatformRoute(conversationKind string, targetID int64) (resolvedPlatformRoute, bool) {
	if a == nil || targetID <= 0 {
		return resolvedPlatformRoute{}, false
	}
	route := platformio.RouteKey{Platform: platformio.PlatformQQ}
	if a.SelfID() > 0 {
		route.AccountID = strconv.FormatInt(a.SelfID(), 10)
	}
	target := strconv.FormatInt(targetID, 10)
	external := false
	if saved, ok := a.sessionIndex().route(conversationKind, targetID); ok {
		if saved.Platform != "" {
			route.Platform = saved.Platform
		}
		if saved.AccountID != "" {
			route.AccountID = saved.AccountID
		}
		if saved.Scope != "" {
			route.Scope = saved.Scope
		}
		if saved.TargetID != "" {
			target = saved.TargetID
		}
		external = route.Platform != platformio.PlatformQQ || cachedTargetDiffers(target, targetID)
	}
	if route.Platform == "" {
		route.Platform = platformio.PlatformQQ
	}
	normalized, err := route.Normalize()
	if err != nil {
		return resolvedPlatformRoute{}, false
	}
	resolved := resolvedPlatformRoute{RouteKey: normalized, Target: target, External: external}
	if a.platformIO != nil {
		resolved.HasAdapter = len(a.platformIO.ResolveSendAdapters(normalized)) > 0
	}
	return resolved, true
}

func cachedTargetDiffers(target string, targetID int64) bool {
	parsed, err := strconv.ParseInt(strings.TrimSpace(target), 10, 64)
	return err != nil || parsed != targetID
}

// sendConversationMessage 群聊/私聊文本出站：已绑定平台发送路由时走 Platform IO，否则 QQ 直调 OneBot
func (a *Agent) sendConversationMessage(
	ctx context.Context,
	conversationKind string,
	targetID int64,
	text string,
	replyTo int64,
	mentions []int64,
) (messageID int64, arrivalSeq uint64, err error) {
	if a.isInternalConversation(conversationKind, targetID) {
		messageID, err := a.webChatSender().SendBotRich(ctx, conversationKind, targetID, text, replyTo, mentions)
		if err != nil {
			return 0, 0, err
		}
		// WebUI 内聊没有协议端回传，必须自己落库，否则历史只剩用户消息、气泡全在右侧
		a.persistInternalBotMessage(ctx, conversationKind, targetID, text, messageID, replyTo)
		return messageID, 0, nil
	}
	if a.platformIO != nil {
		if resolved, ok := a.resolvePlatformRoute(conversationKind, targetID); ok {
			if resolved.HasAdapter {
				messageID, arrivalSeq, err = a.sendViaPlatformIO(ctx, resolved, conversationKind, targetID, text, replyTo, mentions)
				if err != nil {
					return 0, 0, err
				}
				return messageID, arrivalSeq, nil
			}
			if resolved.External {
				return 0, 0, platformRouteUnavailableError(resolved)
			}
		}
	}
	return 0, 0, fmt.Errorf("没有可用的平台发送适配器，请先在适配器管理中启用对应平台连接")
}

// sendConversationImage 群聊/私聊图片或表情包出站：已绑定 qq 发送路由时走 Platform IO
func (a *Agent) sendConversationImage(
	ctx context.Context,
	conversationKind string,
	targetID int64,
	filePath string,
	isSticker bool,
) (messageID int64, arrivalSeq uint64, err error) {
	if a.isInternalConversation(conversationKind, targetID) {
		messageID, err := a.webChatSender().SendBotImage(ctx, conversationKind, targetID, filePath, isSticker)
		if err != nil {
			return 0, 0, err
		}
		return messageID, 0, nil
	}
	if a.platformIO != nil {
		if resolved, ok := a.resolvePlatformRoute(conversationKind, targetID); ok {
			if resolved.HasAdapter {
				messageID, arrivalSeq, err = a.sendImageViaPlatformIO(ctx, resolved, conversationKind, filePath, isSticker)
				if err != nil {
					return 0, 0, err
				}
				return messageID, arrivalSeq, nil
			}
			if resolved.External {
				return 0, 0, platformRouteUnavailableError(resolved)
			}
		}
	}
	return 0, 0, fmt.Errorf("没有可用的平台发送适配器，请先在适配器管理中启用对应平台连接")
}

// SendFileToConversation 群聊/私聊文件出站：优先 Platform IO，否则直调 OneBot 上传接口
func (a *Agent) SendFileToConversation(
	ctx context.Context,
	conversationKind string,
	targetID int64,
	fileURL string,
	name string,
) (messageID int64, arrivalSeq uint64, err error) {
	fileURL = strings.TrimSpace(fileURL)
	name = strings.TrimSpace(name)
	if fileURL == "" || name == "" {
		return 0, 0, fmt.Errorf("文件发送参数不完整")
	}
	if a == nil || a.platformIO == nil {
		return 0, 0, fmt.Errorf("Platform IO 未就绪")
	}
	if a.platformIO != nil {
		if resolved, ok := a.resolvePlatformRoute(conversationKind, targetID); ok {
			if resolved.HasAdapter {
				messageID, arrivalSeq, err = a.sendFileViaPlatformIO(ctx, resolved, conversationKind, fileURL, name)
				if err != nil {
					return 0, 0, err
				}
				return messageID, arrivalSeq, nil
			}
			if resolved.External {
				return 0, 0, platformRouteUnavailableError(resolved)
			}
		}
	}
	return 0, 0, fmt.Errorf("没有可用的平台发送适配器，请先在适配器管理中启用对应平台连接")
}

func (a *Agent) sendFileViaPlatformIO(
	ctx context.Context,
	resolved resolvedPlatformRoute,
	conversationKind string,
	fileURL string,
	name string,
) (messageID int64, arrivalSeq uint64, err error) {
	chatType, err := chatTypeForKind(conversationKind)
	if err != nil {
		return 0, 0, err
	}
	routeKey := resolved.RouteKey
	outbound := platformio.OutboundMessageEnvelope{
		RouteKey:    routeKey,
		ChatType:    chatType,
		TargetID:    resolved.Target,
		Timestamp:   time.Now(),
		MessageInfo: outboundMessageInfoForRoute(routeKey, conversationKind, resolved.Target),
		Segments: []platformio.Segment{{
			Kind:  platformio.SegmentFile,
			URL:   fileURL,
			Extra: map[string]string{"name": name},
		}},
	}
	batch := a.platformIO.SendMessage(ctx, outbound, routeKey)
	if !batch.HasSuccess() {
		return 0, 0, platformIOSendError(batch)
	}
	receipt := batch.SentReceipts()[0]
	messageID, err = parseExternalMessageID(receipt.ExternalMessageID)
	if err != nil && receipt.ExternalMessageID != "" {
		zap.L().Warn("Platform IO 文件出站 message_id 解析失败",
			zap.String("driver_id", receipt.DriverID),
			zap.String("external_message_id", receipt.ExternalMessageID),
			zap.Error(err))
	}
	if messageID == 0 && resolved.External {
		messageID = syntheticOutboundMessageID(resolved)
	}
	arrivalSeq = arrivalSeqFromReceipt(receipt)
	return messageID, arrivalSeq, nil
}

var outboundMessageSeq atomic.Uint64

// syntheticOutboundMessageID 为非 QQ 出站生成代理消息 ID，供会话持久化与引用使用
func syntheticOutboundMessageID(route resolvedPlatformRoute) int64 {
	sequence := strconv.FormatUint(outboundMessageSeq.Add(1), 10)
	return onebot.SyntheticIdentityID("outbound", route.RouteKey.Platform, route.RouteKey.AccountID, route.RouteKey.Scope, route.Target, strconv.FormatInt(time.Now().UnixNano(), 10), sequence)
}

func platformRouteUnavailableError(route resolvedPlatformRoute) error {
	return fmt.Errorf("未找到平台发送适配器: platform=%s account=%s scope=%s target=%s",
		route.RouteKey.Platform, route.RouteKey.AccountID, route.RouteKey.Scope, route.Target)
}

func (a *Agent) sendViaPlatformIO(
	ctx context.Context,
	resolved resolvedPlatformRoute,
	conversationKind string,
	targetID int64,
	text string,
	replyTo int64,
	mentions []int64,
) (messageID int64, arrivalSeq uint64, err error) {
	chatType, err := chatTypeForKind(conversationKind)
	if err != nil {
		return 0, 0, err
	}
	routeKey := resolved.RouteKey
	outbound := platformio.OutboundMessageEnvelope{
		RouteKey:    routeKey,
		ChatType:    chatType,
		TargetID:    resolved.Target,
		PlainText:   text,
		Timestamp:   time.Now(),
		MessageInfo: outboundMessageInfoForRoute(routeKey, conversationKind, resolved.Target),
		Segments:    a.outboundSegmentsForText(conversationKind, targetID, text, mentions),
	}
	if replyTo != 0 {
		outbound.ReplyToMessageID = strconv.FormatInt(replyTo, 10)
		outbound.ExternalReplyToMessageID = a.externalIdentityFor(conversationKind, targetID, "reply", replyTo)
	}
	batch := a.platformIO.SendMessage(ctx, outbound, routeKey)
	if !batch.HasSuccess() {
		return 0, 0, platformIOSendError(batch)
	}
	receipt := batch.SentReceipts()[0]
	messageID, err = parseExternalMessageID(receipt.ExternalMessageID)
	if err != nil && receipt.ExternalMessageID != "" {
		zap.L().Warn("Platform IO 出站 message_id 解析失败",
			zap.String("driver_id", receipt.DriverID),
			zap.String("external_message_id", receipt.ExternalMessageID),
			zap.Error(err))
	}
	if messageID == 0 && resolved.External {
		messageID = syntheticOutboundMessageID(resolved)
	}
	arrivalSeq = arrivalSeqFromReceipt(receipt)
	return messageID, arrivalSeq, nil
}

func (a *Agent) sendImageViaPlatformIO(
	ctx context.Context,
	resolved resolvedPlatformRoute,
	conversationKind string,
	filePath string,
	isSticker bool,
) (messageID int64, arrivalSeq uint64, err error) {
	chatType, err := chatTypeForKind(conversationKind)
	if err != nil {
		return 0, 0, err
	}
	routeKey := resolved.RouteKey
	seg := outboundImageSegment(filePath, isSticker)
	outbound := platformio.OutboundMessageEnvelope{
		RouteKey:    routeKey,
		ChatType:    chatType,
		TargetID:    resolved.Target,
		Timestamp:   time.Now(),
		MessageInfo: outboundMessageInfoForRoute(routeKey, conversationKind, resolved.Target),
		Segments:    []platformio.Segment{seg},
	}
	batch := a.platformIO.SendMessage(ctx, outbound, routeKey)
	if !batch.HasSuccess() {
		return 0, 0, platformIOSendError(batch)
	}
	receipt := batch.SentReceipts()[0]
	messageID, err = parseExternalMessageID(receipt.ExternalMessageID)
	if err != nil && receipt.ExternalMessageID != "" {
		zap.L().Warn("Platform IO 图片出站 message_id 解析失败",
			zap.String("driver_id", receipt.DriverID),
			zap.String("external_message_id", receipt.ExternalMessageID),
			zap.Error(err))
	}
	if messageID == 0 && resolved.External {
		messageID = syntheticOutboundMessageID(resolved)
	}
	arrivalSeq = arrivalSeqFromReceipt(receipt)
	return messageID, arrivalSeq, nil
}

func outboundImageSegment(filePath string, isSticker bool) platformio.Segment {
	trimmed := strings.ToLower(strings.TrimSpace(filePath))
	seg := platformio.Segment{Kind: platformio.SegmentImage}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		seg.URL = filePath
	} else {
		seg.Extra = map[string]string{"file": filePath}
	}
	if isSticker {
		if seg.Extra == nil {
			seg.Extra = map[string]string{}
		}
		seg.Extra["sub_type"] = "1"
	}
	return seg
}

func chatTypeForKind(kind string) (platformio.ChatType, error) {
	switch kind {
	case memory.ConversationKindGroup:
		return platformio.ChatTypeGroup, nil
	case memory.ConversationKindPrivate:
		return platformio.ChatTypePrivate, nil
	default:
		return "", fmt.Errorf("未知会话类型: %s", kind)
	}
}

func outboundMessageInfoForRoute(route platformio.RouteKey, conversationKind, target string) platformio.OutboundMessageInfo {
	info := platformio.OutboundMessageInfo{
		UserInfo: platformio.OutboundUserInfo{UserID: route.AccountID},
	}
	if conversationKind == memory.ConversationKindGroup {
		info.GroupInfo = &platformio.OutboundGroupInfo{GroupID: target}
	}
	return info
}

func (a *Agent) outboundSegmentsForText(conversationKind string, targetID int64, text string, mentions []int64) []platformio.Segment {
	capacity := 1
	if len(mentions) > 0 {
		capacity += len(mentions)
	}
	segments := make([]platformio.Segment, 0, capacity)
	for _, uid := range mentions {
		if uid <= 0 {
			continue
		}
		atID := strconv.FormatInt(uid, 10)
		if external := a.externalIdentityFor(conversationKind, targetID, "at", uid); external != "" {
			atID = external
		}
		segments = append(segments, platformio.Segment{
			Kind:     platformio.SegmentAt,
			AtUserID: atID,
		})
	}
	if text != "" {
		segments = append(segments, platformio.Segment{Kind: platformio.SegmentText, Text: text})
	}
	return segments
}

// rememberExternalIdentities 记录入站数字代理 ID 与平台原始字符串 ID 的对应关系，供出站 @/回复还原
func (a *Agent) rememberExternalIdentities(msg *onebot.ConversationMessage) {
	if a == nil || a.externalIdentities == nil || msg == nil || msg.TargetID <= 0 {
		return
	}
	for i, uid := range msg.AtList {
		if uid <= 0 || i >= len(msg.ExternalAtList) {
			continue
		}
		if external := strings.TrimSpace(msg.ExternalAtList[i]); external != "" {
			a.externalIdentities.Set(externalIdentityCacheKey(msg.ConversationKind, msg.TargetID, "at", uid), external, ttlcache.DefaultTTL)
		}
	}
	if msg.Reply != nil && msg.Reply.MessageID > 0 {
		if external := strings.TrimSpace(msg.Reply.ExternalMessageID); external != "" {
			a.externalIdentities.Set(externalIdentityCacheKey(msg.ConversationKind, msg.TargetID, "reply", msg.Reply.MessageID), external, ttlcache.DefaultTTL)
		}
	}
}

func (a *Agent) externalIdentityFor(conversationKind string, targetID int64, role string, id int64) string {
	if a == nil || a.externalIdentities == nil || targetID <= 0 || id <= 0 {
		return ""
	}
	if item := a.externalIdentities.Get(externalIdentityCacheKey(conversationKind, targetID, role, id)); item != nil {
		return item.Value()
	}
	return ""
}

func externalIdentityCacheKey(conversationKind string, targetID int64, role string, id int64) string {
	return chatsession.ConversationKey(conversationKind, targetID) + "|" + role + "|" + strconv.FormatInt(id, 10)
}

func parseExternalMessageID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func arrivalSeqFromReceipt(receipt platformio.DeliveryReceipt) uint64 {
	if receipt.Metadata == nil {
		return 0
	}
	switch v := receipt.Metadata["arrival_seq"].(type) {
	case uint64:
		return v
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case int:
		if v > 0 {
			return uint64(v)
		}
	case float64:
		if v > 0 {
			return uint64(v)
		}
	}
	return 0
}

func platformIOSendError(batch platformio.DeliveryBatch) error {
	if len(batch.Receipts) == 0 {
		return fmt.Errorf("Platform IO 出站无适配器回执")
	}
	last := batch.Receipts[len(batch.Receipts)-1]
	if last.Error != "" {
		return fmt.Errorf("Platform IO 出站失败: %s", last.Error)
	}
	return fmt.Errorf("Platform IO 出站失败")
}
