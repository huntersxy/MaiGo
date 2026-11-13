package platformio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FileURLResolverAdapter 由真正实现文件解析的驱动实现
//
// 对齐 MaiBot 插件 API adapter.files.resolve v1：驱动自己解析平台文件定位，
// 返回可直接下载的 HTTP(S) 地址；平台层不猜测地址
type FileURLResolverAdapter interface {
	ResolveFileURL(ctx context.Context, source map[string]any) (string, error)
}

// FilePermissionError 标记适配器文件来源校验失败，filesvc 据此映射 403
type FilePermissionError struct {
	message string
}

// Error 实现 error
func (e *FilePermissionError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// IsFilePermission 供 filesvc 通过接口识别权限错误，避免跨包依赖
func (e *FilePermissionError) IsFilePermission() bool { return true }

// IsFilePermissionError 判断错误是否来自文件来源权限校验
func IsFilePermissionError(err error) bool {
	var permission *FilePermissionError
	if errors.As(err, &permission) {
		return true
	}
	var tagged interface{ IsFilePermission() bool }
	return errors.As(err, &tagged) && tagged.IsFilePermission()
}

// AdapterByID 返回已注册驱动；不存在返回 nil
func (m *Manager) AdapterByID(driverID string) Adapter {
	if m == nil {
		return nil
	}
	driverID = strings.TrimSpace(driverID)
	if driverID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registry.Get(driverID)
}

// ResolveFileURL 按来源描述定位驱动并解析文件下载地址
//
// 对齐 MaiBot FileService.resolve_url：校验 driver/plugin/platform/account/scope
// 后调用驱动实现，缺少能力或地址无效都返回真实错误
func (m *Manager) ResolveFileURL(ctx context.Context, source map[string]any) (string, error) {
	if m == nil {
		return "", errors.New("接收此文件的适配器不可用")
	}
	driverID := sourceString(source, "driver_id")
	pluginID := sourceString(source, "plugin_id")
	adapter := m.AdapterByID(driverID)
	if adapter == nil || strings.TrimSpace(adapter.Descriptor().PluginID) != pluginID {
		return "", errors.New("接收此文件的适配器不可用")
	}
	descriptor := adapter.Descriptor()
	platform := sourceString(source, "platform")
	accountID := sourceString(source, "account_id")
	scope := sourceString(source, "scope")
	if strings.TrimSpace(descriptor.AccountID) != accountID ||
		strings.TrimSpace(descriptor.Scope) != scope ||
		strings.TrimSpace(descriptor.Platform) != platform {
		return "", &FilePermissionError{message: "文件来源账号、平台或作用域与适配器不一致"}
	}
	resolver, ok := adapter.(FileURLResolverAdapter)
	if !ok {
		return "", errors.New("适配器尚未实现文件解析能力")
	}
	rawURL, err := resolver.ResolveFileURL(ctx, source)
	if err != nil {
		return "", err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("适配器没有返回有效下载地址")
	}
	return rawURL, nil
}

func sourceString(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	return stringValue(source[key])
}

// FileSendTarget 描述文件应发送到的平台路由与会话
type FileSendTarget struct {
	RouteKey RouteKey
	ChatType ChatType
	TargetID string
	Name     string
}

// FileSendTargetResolver 由宿主把内部聊天流解析成平台路由
type FileSendTargetResolver interface {
	ResolveFileSendTarget(ctx context.Context, sessionID string) (FileSendTarget, error)
}

// FileSendTargetResolverFunc 让调用方用函数注入目标解析
type FileSendTargetResolverFunc func(ctx context.Context, sessionID string) (FileSendTarget, error)

// ResolveFileSendTarget 实现 FileSendTargetResolver
func (f FileSendTargetResolverFunc) ResolveFileSendTarget(ctx context.Context, sessionID string) (FileSendTarget, error) {
	return f(ctx, sessionID)
}

// FileSender 把文件服务里的附件发送到由宿主解析出的平台路由
//
// 首次适配器命中多账号/多平台路由时由 RouteKey 精确选择，不做跨平台广播
type FileSender struct {
	Manager        *Manager
	TargetResolver FileSendTargetResolver
	ResolveURL     func(ctx context.Context, fileRef, sessionID string) (string, error)
}

// NewFileSender 创建多平台文件发送器
func NewFileSender(manager *Manager, resolver FileSendTargetResolver, resolveURL func(ctx context.Context, fileRef, sessionID string) (string, error)) *FileSender {
	return &FileSender{Manager: manager, TargetResolver: resolver, ResolveURL: resolveURL}
}

// Send 解析目标、取得下载地址并通过 Platform IO 发送文件段
//
// 返回字段对齐 FileService.send，但只有真实 sent 回执才返回 sent
func (s *FileSender) Send(ctx context.Context, fileRef, sessionID string) (map[string]any, error) {
	if s == nil || s.Manager == nil {
		return nil, errors.New("Platform IO 未接线")
	}
	if s.TargetResolver == nil {
		return nil, errors.New("文件发送目标解析器未接线")
	}
	if s.ResolveURL == nil {
		return nil, errors.New("文件下载地址解析器未接线")
	}
	target, err := s.TargetResolver.ResolveFileSendTarget(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	routeKey, err := target.RouteKey.Normalize()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(target.TargetID) == "" {
		return nil, errors.New("文件发送目标不能为空")
	}
	chatType := target.ChatType
	if chatType == "" {
		chatType = ChatTypePrivate
	}
	rawURL, err := s.ResolveURL(ctx, fileRef, sessionID)
	if err != nil {
		return nil, err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("文件下载地址不能为空")
	}
	name := strings.TrimSpace(target.Name)
	if name == "" {
		name = "file"
	}
	internalID := fmt.Sprintf("file_%d", timeNowUnixNano())
	batch := s.Manager.SendMessage(ctx, OutboundMessageEnvelope{
		InternalMessageID: internalID,
		RouteKey:          routeKey,
		ChatType:          chatType,
		TargetID:          strings.TrimSpace(target.TargetID),
		Segments: []Segment{{
			Kind:  SegmentFile,
			URL:   rawURL,
			Extra: map[string]string{"name": name},
		}},
	}, routeKey)
	if len(batch.Receipts) == 0 {
		return nil, fmt.Errorf("未找到平台发送适配器: platform=%s account=%s scope=%s target=%s", routeKey.Platform, routeKey.AccountID, routeKey.Scope, target.TargetID)
	}
	var externalID, lastErr string
	for _, receipt := range batch.Receipts {
		if receipt.Status == DeliveryStatusSent {
			externalID = receipt.ExternalMessageID
			break
		}
		if receipt.Error != "" {
			lastErr = receipt.Error
		}
	}
	if externalID == "" && lastErr == "" && len(batch.FailedReceipts()) > 0 {
		lastErr = "文件发送失败"
	}
	if externalID == "" {
		if lastErr == "" {
			lastErr = "文件发送失败"
		}
		return nil, errors.New(lastErr)
	}
	return map[string]any{
		"file_ref":            fileRef,
		"status":              "sent",
		"message_id":          externalID,
		"retry_automatically": false,
	}, nil
}

// timeNowUnixNano 单独抽出便于测试替换
var timeNowUnixNano = func() int64 { return time.Now().UnixNano() }
