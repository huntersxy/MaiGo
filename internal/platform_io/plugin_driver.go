package platformio

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultPluginGatewayTimeoutMS 插件消息网关未声明超时时的默认 RPC 超时
const defaultPluginGatewayTimeoutMS int64 = 60000

// PluginGatewayInvoker 插件消息网关调用通道，由主程序 pluginruntime 注入
type PluginGatewayInvoker func(ctx context.Context, pluginID, componentName string, args map[string]any, timeoutMS int64) (map[string]any, error)

// PluginDriverOptions 插件消息网关驱动配置
type PluginDriverOptions struct {
	DriverID      string
	Platform      string
	AccountID     string
	Scope         string
	PluginID      string
	ComponentName string
	GatewayName   string
	SupportsSend  bool
	TimeoutMS     int64
	Invoker       PluginGatewayInvoker
	Metadata      map[string]any
	// FileResolver 出站前把 file 段的 file_ref 解析为可下载 URL
	// 对齐 Python plugin_driver.py:113-126 的 ensure + download_url
	FileResolver PluginFileResolver
	// FileURLInvoker 入站附件解析：调用插件 adapter.files.resolve v1
	// 对齐 Python files/service.py:127-150 的 resolve_url
	FileURLInvoker PluginFileURLInvoker
}

// PluginFileResolver 把附件引用解析为可下载地址，对齐 Python get_file_service().download_url
type PluginFileResolver func(ctx context.Context, fileRef, sessionID string) (string, error)

// PluginFileURLInvoker 调用插件 adapter.files.resolve v1，返回平台文件下载地址
type PluginFileURLInvoker func(ctx context.Context, pluginID string, source map[string]any, timeoutMS int64) (string, error)

// adapterFilesResolveTimeoutMS 对齐 Python files/service.py resolve_url 的 30000ms
const adapterFilesResolveTimeoutMS int64 = 30000

// PluginPlatformDriver 插件消息网关驱动，对齐 MaiBot PluginPlatformDriver
type PluginPlatformDriver struct {
	BaseAdapter
	driverID       string
	pluginID       string
	componentName  string
	supportsSend   bool
	timeoutMS      int64
	invoker        PluginGatewayInvoker
	fileResolver   PluginFileResolver
	fileURLInvoker PluginFileURLInvoker

	lifecycleMu sync.RWMutex
	started     bool
}

// NewPluginPlatformDriver 创建插件消息网关驱动
func NewPluginPlatformDriver(opts PluginDriverOptions) *PluginPlatformDriver {
	driverID := strings.TrimSpace(opts.DriverID)
	if driverID == "" {
		driverID = "plugin." + strings.TrimSpace(opts.PluginID)
	}
	componentName := strings.TrimSpace(opts.ComponentName)
	if componentName == "" {
		componentName = "message_gateway"
	}
	metadata := map[string]any{
		"source":         "plugin_message_gateway",
		"component_name": componentName,
	}
	if gateway := strings.TrimSpace(opts.GatewayName); gateway != "" {
		metadata["gateway_name"] = gateway
	}
	for key, value := range opts.Metadata {
		metadata[key] = value
	}
	desc := DriverDescriptor{
		DriverID:  driverID,
		Kind:      DriverKindPlugin,
		Platform:  strings.TrimSpace(opts.Platform),
		AccountID: strings.TrimSpace(opts.AccountID),
		Scope:     strings.TrimSpace(opts.Scope),
		PluginID:  strings.TrimSpace(opts.PluginID),
		Metadata:  metadata,
	}
	timeoutMS := opts.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = defaultPluginGatewayTimeoutMS
	}
	return &PluginPlatformDriver{
		BaseAdapter:    NewBaseAdapter(desc),
		driverID:       driverID,
		pluginID:       desc.PluginID,
		componentName:  componentName,
		supportsSend:   opts.SupportsSend,
		timeoutMS:      timeoutMS,
		invoker:        opts.Invoker,
		fileResolver:   opts.FileResolver,
		fileURLInvoker: opts.FileURLInvoker,
	}
}

// SetFileResolver 注入附件解析器，用于出站 file 段的 file_ref 解析
func (d *PluginPlatformDriver) SetFileResolver(resolver PluginFileResolver) {
	d.fileResolver = resolver
}

// SetFileURLInvoker 注入插件 API 文件解析通道
func (d *PluginPlatformDriver) SetFileURLInvoker(invoker PluginFileURLInvoker) {
	d.fileURLInvoker = invoker
}

// FileURLInvokerConfigured 是否已注入插件 API 文件解析通道
func (d *PluginPlatformDriver) FileURLInvokerConfigured() bool {
	return d.fileURLInvoker != nil
}

// ResolveFileURL 对齐 Python FileService.resolve_url 的 adapter.files.resolve 分支
func (d *PluginPlatformDriver) ResolveFileURL(ctx context.Context, source map[string]any) (string, error) {
	if d == nil || d.fileURLInvoker == nil {
		return "", errors.New("适配器尚未实现文件解析能力")
	}
	rawURL, err := d.fileURLInvoker(ctx, d.pluginID, source, adapterFilesResolveTimeoutMS)
	if err != nil {
		return "", err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("适配器没有返回有效下载地址")
	}
	return rawURL, nil
}

// resolveFileSegments 递归展开 forward 内嵌段，把 file 段的 file_ref 解析为 url
// 对齐 Python plugin_driver.py:116-126：pending_segments 栈式遍历，forward 的 data 为列表时展开
func (d *PluginPlatformDriver) resolveFileSegments(ctx context.Context, segments []map[string]any, sessionID string) {
	if d.fileResolver == nil || len(segments) == 0 {
		return
	}
	pending := make([]map[string]any, 0, len(segments))
	pending = append(pending, segments...)
	for len(pending) > 0 {
		segment := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if segment == nil {
			continue
		}
		kind, _ := segment["type"].(string)
		switch kind {
		case "forward":
			nodes, ok := segment["data"].([]any)
			if !ok {
				continue
			}
			for _, raw := range nodes {
				node, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				// content 由 segmentsToMaps 产出，类型是 []map[string]any
				switch content := node["content"].(type) {
				case []map[string]any:
					pending = append(pending, content...)
				case []any:
					for _, item := range content {
						if child, ok := item.(map[string]any); ok {
							pending = append(pending, child)
						}
					}
				}
			}
		case "file":
			data, ok := segment["data"].(map[string]any)
			if !ok {
				continue
			}
			fileRef, _ := data["file_ref"].(string)
			fileRef = strings.TrimSpace(fileRef)
			if fileRef == "" {
				continue
			}
			url, err := d.fileResolver(ctx, fileRef, sessionID)
			if err != nil {
				// 解析失败保留原段，由插件侧按 file_ref 自行处理
				continue
			}
			if strings.TrimSpace(url) != "" {
				data["url"] = url
			}
		}
	}
}

// SetInvoker 注入/替换插件网关调用通道
func (d *PluginPlatformDriver) SetInvoker(invoker PluginGatewayInvoker) {
	d.invoker = invoker
}

// InvokerConfigured 是否已注入插件网关调用通道
func (d *PluginPlatformDriver) InvokerConfigured() bool { return d.invoker != nil }

// Start 标记驱动进入运行态；重复调用幂等，插件进程生命周期仍由 pluginruntime 管理
func (d *PluginPlatformDriver) Start(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	d.lifecycleMu.Lock()
	d.started = true
	d.lifecycleMu.Unlock()
	return nil
}

// Stop 标记驱动退出运行态；重复调用幂等
func (d *PluginPlatformDriver) Stop(ctx context.Context) error {
	_ = ctx
	d.lifecycleMu.Lock()
	d.started = false
	d.lifecycleMu.Unlock()
	return nil
}

// IsStarted 返回驱动当前是否处于运行态
func (d *PluginPlatformDriver) IsStarted() bool {
	d.lifecycleMu.RLock()
	defer d.lifecycleMu.RUnlock()
	return d.started
}

// DeliverInbound 校验并转交插件网关产出的入站消息，未接线时返回真实错误
func (d *PluginPlatformDriver) DeliverInbound(ctx context.Context, envelope InboundMessageEnvelope) error {
	normalized, err := d.normalizeInboundEnvelope(envelope)
	if err != nil {
		return err
	}
	return d.EmitInbound(ctx, normalized)
}

func (d *PluginPlatformDriver) normalizeInboundEnvelope(envelope InboundMessageEnvelope) (InboundMessageEnvelope, error) {
	desc := d.Descriptor()
	if strings.TrimSpace(envelope.DriverID) == "" {
		envelope.DriverID = d.driverID
	}
	if envelope.DriverKind == "" {
		envelope.DriverKind = DriverKindPlugin
	}
	if strings.TrimSpace(envelope.RouteKey.Platform) == "" {
		envelope.RouteKey.Platform = desc.Platform
	}
	if strings.TrimSpace(envelope.RouteKey.AccountID) == "" {
		envelope.RouteKey.AccountID = desc.AccountID
	}
	if strings.TrimSpace(envelope.RouteKey.Scope) == "" {
		envelope.RouteKey.Scope = desc.Scope
	}
	if envelope.DriverID != d.driverID {
		return envelope, fmt.Errorf("%w: driver_id=%s", errPluginInboundDriverMismatch, envelope.DriverID)
	}
	if envelope.DriverKind != DriverKindPlugin {
		return envelope, fmt.Errorf("%w: driver_kind=%s", errPluginInboundKindMismatch, envelope.DriverKind)
	}
	if envelope.RouteKey.Platform != desc.Platform {
		return envelope, fmt.Errorf("%w: platform=%s", errPluginInboundPlatformMismatch, envelope.RouteKey.Platform)
	}
	if desc.AccountID != "" && envelope.RouteKey.AccountID != "" && envelope.RouteKey.AccountID != desc.AccountID {
		return envelope, fmt.Errorf("%w: account_id=%s", errPluginInboundRouteMismatch, envelope.RouteKey.AccountID)
	}
	if desc.Scope != "" && envelope.RouteKey.Scope != "" && envelope.RouteKey.Scope != desc.Scope {
		return envelope, fmt.Errorf("%w: scope=%s", errPluginInboundRouteMismatch, envelope.RouteKey.Scope)
	}
	return envelope, nil
}

// Send 通过插件消息网关发送消息
func (d *PluginPlatformDriver) Send(ctx context.Context, outbound OutboundMessageEnvelope, routeKey RouteKey) (DeliveryReceipt, error) {
	receipt := DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            DeliveryStatusPending,
		DriverID:          d.driverID,
		DriverKind:        DriverKindPlugin,
	}
	if !d.supportsSend {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = "当前消息网关仅支持接收，不支持发送"
		return receipt, fmt.Errorf("plugin: %s", receipt.Error)
	}
	if d.pluginID == "" {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = "插件消息网关驱动缺少 plugin_id"
		return receipt, fmt.Errorf("plugin: %s", receipt.Error)
	}
	if d.invoker == nil {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = "插件消息网关调用通道未接线"
		return receipt, fmt.Errorf("plugin: %s", receipt.Error)
	}

	metadata := cloneStringAnyMap(outbound.Metadata)
	// raw_message 对齐 Python plugin_runtime 的 [{type,data}]；segments 旧字段保留一个版本
	rawSegments := segmentsToMaps(outbound.Segments)
	// 对齐 Python plugin_driver.py:113-126：出站前把 file 段的 file_ref 解析为可下载 URL
	d.resolveFileSegments(ctx, rawSegments, outbound.InternalMessageID)
	args := map[string]any{
		"message": outboundPluginMessage(outbound, routeKey, rawSegments),
		"route": map[string]any{
			"platform":   routeKey.Platform,
			"account_id": routeKey.AccountID,
			"scope":      routeKey.Scope,
		},
		"metadata": metadata,
	}
	response, err := d.invoker(ctx, d.pluginID, d.componentName, args, d.timeoutMS)
	if err != nil {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	status, externalID, resultMetadata, errMessage := pluginGatewayReceipt(response)
	if errMessage != "" {
		receipt.Status = DeliveryStatusFailed
		receipt.Error = errMessage
		return receipt, fmt.Errorf("plugin: %s", errMessage)
	}
	receipt.Status = status
	receipt.ExternalMessageID = externalID
	if len(resultMetadata) > 0 {
		receipt.Metadata = resultMetadata
	}
	if status == DeliveryStatusPending {
		receipt.Status = DeliveryStatusSent
	}
	return receipt, nil
}

// outboundPluginMessage 构造插件网关的 message 字典
//
// 必需字段对齐 Python PluginMessageUtils._session_message_to_dict；
// internal_message_id/chat_type/target_id/segments/plain_text 等是 Go 旧版
// 兼容字段，保留为超集，Python 插件会忽略未知键。
func outboundPluginMessage(outbound OutboundMessageEnvelope, routeKey RouteKey, rawSegments []map[string]any) map[string]any {
	timestamp := outbound.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	message := map[string]any{
		"internal_message_id":          outbound.InternalMessageID,
		"message_id":                   outbound.InternalMessageID,
		"timestamp":                    pluginTimestampString(timestamp),
		"platform":                     routeKey.Platform,
		"account_id":                   optionalPluginString(routeKey.AccountID),
		"scope":                        optionalPluginString(routeKey.Scope),
		"chat_type":                    string(outbound.ChatType),
		"target_id":                    outbound.TargetID,
		"content":                      outbound.PlainText,
		"segments":                     rawSegments,
		"raw_message":                  rawSegments,
		"message_info":                 outboundMessageInfoMap(outbound.MessageInfo),
		"plain_text":                   outbound.PlainText,
		"processed_plain_text":         outbound.PlainText,
		"is_mentioned":                 outbound.IsMentioned,
		"is_at":                        outbound.IsAt,
		"is_emoji":                     outbound.IsEmoji,
		"is_picture":                   outbound.IsPicture,
		"is_command":                   outbound.IsCommand,
		"is_notify":                    outbound.IsNotify,
		"session_id":                   outbound.SessionID,
		"reply_to":                     outbound.ReplyToMessageID,
		"reply_to_message_id":          outbound.ReplyToMessageID,
		"external_reply_to_message_id": outbound.ExternalReplyToMessageID,
	}
	if outbound.ReceivedAt != nil {
		message["received_at"] = outbound.ReceivedAt.Format("2006-01-02T15:04:05.000000")
	}
	return message
}

func outboundMessageInfoMap(info OutboundMessageInfo) map[string]any {
	userInfo := map[string]any{
		"user_id":       info.UserInfo.UserID,
		"user_nickname": info.UserInfo.UserNickname,
		"user_cardname": optionalPluginString(info.UserInfo.UserCardname),
	}
	var groupInfo any
	if info.GroupInfo != nil {
		groupInfo = map[string]any{
			"group_id":   info.GroupInfo.GroupID,
			"group_name": info.GroupInfo.GroupName,
		}
	}
	additional := info.AdditionalConfig
	if additional == nil {
		additional = map[string]any{}
	}
	return map[string]any{
		"user_info":         userInfo,
		"group_info":        groupInfo,
		"additional_config": additional,
	}
}

// pluginTimestampString 对齐 Python str(datetime.timestamp())，保留微秒
func pluginTimestampString(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixMicro())/1e6, 'f', -1, 64)
}

func optionalPluginString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

// pluginGatewayReceipt 解析插件网关返回值，对齐 Python _build_receipt 的主要分支
func pluginGatewayReceipt(response map[string]any) (DeliveryStatus, string, map[string]any, string) {
	if response == nil {
		return DeliveryStatusSent, "", nil, ""
	}
	if errorPayload, ok := response["error"].(map[string]any); ok {
		if message := stringValue(errorPayload["message"]); message != "" {
			return DeliveryStatusFailed, "", nil, message
		}
	}
	if errText := stringValue(response["error"]); errText != "" {
		return DeliveryStatusFailed, "", nil, errText
	}
	payload, _ := response["payload"].(map[string]any)
	success, hasSuccess := payload["success"].(bool)
	if !hasSuccess || !success {
		return DeliveryStatusFailed, "", nil, firstNonEmptyString(stringValue(payload["result"]), "消息网关发送失败")
	}
	switch result := payload["result"].(type) {
	case map[string]any:
		if value, ok := result["success"].(bool); ok && !value {
			return DeliveryStatusFailed, "", nil, firstNonEmptyString(stringValue(result["error"]), stringValue(result["message"]), "消息网关发送失败")
		}
		metadata, _ := result["metadata"].(map[string]any)
		externalID := firstNonEmptyString(stringValue(result["external_message_id"]), stringValue(result["message_id"]))
		return DeliveryStatusSent, externalID, metadata, ""
	case string:
		return DeliveryStatusSent, strings.TrimSpace(result), nil, ""
	case nil:
		return DeliveryStatusSent, "", nil, ""
	default:
		return DeliveryStatusSent, stringValue(result), nil, ""
	}
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// segmentsToMaps 生成插件运行时 raw_message 段 [{type,data}]
// text/at_user_id/url/extra 作为旧扁平字段保留一个版本，方便未升级插件继续读取
func segmentsToMaps(segments []Segment) []map[string]any {
	out := make([]map[string]any, 0, len(segments))
	for _, seg := range segments {
		out = append(out, segmentToMap(seg))
	}
	return out
}

func segmentToMap(seg Segment) map[string]any {
	item := map[string]any{
		"type": string(seg.Kind),
		"data": segmentRawData(seg),
	}
	if seg.Text != "" {
		item["text"] = seg.Text
	}
	if seg.AtUserID != "" {
		item["at_user_id"] = seg.AtUserID
	}
	if seg.URL != "" {
		item["url"] = seg.URL
	}
	switch seg.Kind {
	case SegmentImage, SegmentEmoji, SegmentVoice:
		if hash := extraValue(seg.Extra, "hash", "binary_hash"); hash != "" {
			item["hash"] = hash
		}
		if binary := extraValue(seg.Extra, "binary_data_base64", "base64"); binary != "" {
			item["binary_data_base64"] = binary
		}
	}
	if len(seg.Extra) > 0 {
		extra := make(map[string]any, len(seg.Extra))
		for key, value := range seg.Extra {
			extra[key] = value
		}
		item["extra"] = extra
	}
	return item
}

// segmentRawData 输出 Python PluginMessageUtils._component_to_dict 期望的 data 形状
func segmentRawData(seg Segment) any {
	switch seg.Kind {
	case SegmentText:
		return seg.Text
	case SegmentAt:
		return atSegmentData(seg)
	case SegmentReply:
		return replySegmentData(seg)
	case SegmentImage, SegmentEmoji, SegmentVoice:
		return firstNonEmptyString(
			strings.TrimSpace(seg.URL),
			extraValue(seg.Extra, "url", "file"),
			extraValue(seg.Extra, "base64"),
			strings.TrimSpace(seg.Text),
		)
	case SegmentFile:
		return fileSegmentData(seg)
	case SegmentFace:
		return faceSegmentData(seg)
	case SegmentForward:
		return forwardSegmentData(seg.ForwardNodes)
	default:
		if seg.Data != nil {
			return seg.Data
		}
		payload := map[string]any{}
		setPayloadString(payload, "text", strings.TrimSpace(seg.Text))
		setPayloadString(payload, "url", strings.TrimSpace(seg.URL))
		setPayloadString(payload, "file", extraValue(seg.Extra, "file"))
		for key, value := range seg.Extra {
			if _, exists := payload[key]; !exists {
				payload[key] = value
			}
		}
		return payload
	}
}

func atSegmentData(seg Segment) map[string]any {
	payload := map[string]any{}
	setPayloadString(payload, "target_user_id", strings.TrimSpace(seg.AtUserID), extraValue(seg.Extra, "target_user_id", "user_id"))
	setPayloadString(payload, "target_user_nickname", extraValue(seg.Extra, "name", "nickname", "target_user_nickname"))
	setPayloadString(payload, "target_user_cardname", extraValue(seg.Extra, "cardname", "card", "target_user_cardname"))
	if extraValue(seg.Extra, "uses_configured_bot_nickname") == "1" {
		payload["uses_configured_bot_nickname"] = true
	}
	return payload
}

func replySegmentData(seg Segment) map[string]any {
	payload := map[string]any{}
	setPayloadString(payload, "target_message_id",
		extraValue(seg.Extra, "id", "message_id"),
		strings.TrimSpace(seg.Text),
	)
	setPayloadString(payload, "target_message_content",
		extraValue(seg.Extra, "content", "target_message_content"),
	)
	setPayloadString(payload, "target_message_sender_id",
		extraValue(seg.Extra, "sender_id", "target_message_sender_id"),
	)
	setPayloadString(payload, "target_message_sender_nickname",
		extraValue(seg.Extra, "sender_nickname", "target_message_sender_nickname", "nickname"),
	)
	setPayloadString(payload, "target_message_sender_cardname",
		extraValue(seg.Extra, "sender_cardname", "target_message_sender_cardname", "cardname"),
	)
	return payload
}

func fileSegmentData(seg Segment) map[string]any {
	payload := map[string]any{}
	setPayloadString(payload, "name", extraValue(seg.Extra, "name", "file_name", "filename"), strings.TrimSpace(seg.Text))
	setPayloadString(payload, "size", extraValue(seg.Extra, "size"))
	setPayloadString(payload, "url", strings.TrimSpace(seg.URL), extraValue(seg.Extra, "url", "file"))
	setPayloadString(payload, "file_id", extraValue(seg.Extra, "file_id", "id"))
	setPayloadString(payload, "mime_type", extraValue(seg.Extra, "mime_type", "mimeType"))
	setPayloadString(payload, "base64", extraValue(seg.Extra, "base64"))
	setPayloadString(payload, "file_ref", extraValue(seg.Extra, "file_ref"))
	setPayloadString(payload, "busid", extraValue(seg.Extra, "busid"))
	return payload
}

func faceSegmentData(seg Segment) map[string]any {
	payload := map[string]any{}
	setPayloadString(payload, "id", extraValue(seg.Extra, "id"))
	setPayloadString(payload, "name", extraValue(seg.Extra, "name"), strings.TrimSpace(seg.Text))
	return payload
}

func forwardSegmentData(nodes []ForwardNode) []any {
	out := make([]any, 0, len(nodes))
	for _, node := range nodes {
		item := map[string]any{
			"user_nickname": node.Nickname,
			"message_id":    node.MessageID,
			"content":       segmentsToMaps(node.Segments),
		}
		setPayloadString(item, "user_id", node.UserID)
		setPayloadString(item, "user_cardname", node.Cardname)
		out = append(out, item)
	}
	return out
}

func extraValue(extra map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(extra[key]); value != "" {
			return value
		}
	}
	return ""
}

func setPayloadString(payload map[string]any, key string, values ...string) {
	if value := firstNonEmptyString(values...); value != "" {
		payload[key] = value
	}
}

var _ Adapter = (*PluginPlatformDriver)(nil)
