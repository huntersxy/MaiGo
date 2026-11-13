package filesvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PluginSender 由主程序注入，把附件发送到指定聊天流
type PluginSender func(ctx context.Context, fileRef, sessionID string) (map[string]any, error)

// PluginScope 为插件工具调用签发文件作用域凭证
//
// 对齐 Python FileService.ticket("plugin", ...)
func (s *Service) PluginScope(pluginID, sessionID string, lifetime time.Duration) (string, error) {
	if err := s.requireEnabled(); err != nil {
		return "", err
	}
	pluginID = strings.TrimSpace(pluginID)
	sessionID = strings.TrimSpace(sessionID)
	if pluginID == "" {
		return "", fmt.Errorf("plugin_id 不能为空")
	}
	if sessionID == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	return s.Ticket("plugin", "", lifetime, map[string]string{
		"plugin_id": pluginID, "session_id": sessionID,
	})
}

// ResolvePluginScope 校验作用域凭证并返回聊天流 ID
func (s *Service) ResolvePluginScope(token, pluginID string) (string, error) {
	if err := s.requireEnabled(); err != nil {
		return "", err
	}
	claims, err := s.Claims(token, "plugin")
	if err != nil {
		return "", err
	}
	if claims.PluginID != strings.TrimSpace(pluginID) {
		return "", fmt.Errorf("文件作用域不属于当前插件")
	}
	if strings.TrimSpace(claims.SessionID) == "" {
		return "", fmt.Errorf("文件作用域缺少聊天流")
	}
	return claims.SessionID, nil
}

// PluginList 返回聊天流可见附件，对齐 files.list
func (s *Service) PluginList(sessionID string, offset int) ([]map[string]any, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, fmt.Errorf("offset 不能是负数")
	}
	return s.store.List(sessionID, false, offset, 50)
}

// PluginReadText 读取文本片段，对齐 files.read_text
func (s *Service) PluginReadText(fileRef, sessionID string, offset, limit int, encoding string) (map[string]any, error) {
	if _, err := s.Ensure(context.Background(), fileRef, sessionID, false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(encoding) == "" {
		encoding = "utf-8-sig"
	}
	return s.ReadText(fileRef, sessionID, false, offset, limit, encoding)
}

// PluginDownload 返回附件公开信息与限时下载地址，对齐 files.download
func (s *Service) PluginDownload(fileRef, sessionID, scope string) (map[string]any, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	if _, err := s.Ensure(context.Background(), fileRef, sessionID, false); err != nil {
		return nil, err
	}
	asset, err := s.store.Lease(fileRef, sessionID, false, int(s.ticketLifetime().Seconds()))
	if err != nil {
		return nil, err
	}
	token, err := s.Ticket("download", asset.FileRef, 0, map[string]string{"scope": scope})
	if err != nil {
		return nil, err
	}
	transferURL, err := s.TransferURL(token, "")
	if err != nil {
		s.Revoke(token)
		return nil, err
	}
	view := PublicView(asset)
	view["url"] = transferURL
	return view, nil
}

// PluginBeginUpload 预留配额并签发绑定附件的上传凭证，对齐 files.upload
func (s *Service) PluginBeginUpload(sessionID, name, mimeType, scope, pluginID string) (map[string]any, error) {
	asset, err := s.BeginUpload(sessionID, name, mimeType)
	if err != nil {
		return nil, err
	}
	token, err := s.Ticket("upload", asset.FileRef, 0, map[string]string{
		"session_id": sessionID, "scope": scope, "plugin_id": pluginID,
	})
	if err != nil {
		_ = s.store.Fail(asset.FileRef, asset.TransferID, "签发上传凭证失败")
		return nil, err
	}
	transferURL, err := s.TransferURL(token, "")
	if err != nil {
		s.Revoke(token)
		_ = s.store.Fail(asset.FileRef, asset.TransferID, "文件传输地址配置无效")
		return nil, err
	}
	return map[string]any{
		"file_ref": asset.FileRef, "max_bytes": asset.ReservedBytes,
		"upload_token": token, "url": transferURL,
	}, nil
}

// PluginCancelUpload 取消插件上传；凭证已被 HTTP 消费时保持幂等
func (s *Service) PluginCancelUpload(uploadToken, scope, pluginID string) error {
	claims, ok, err := s.lookupTicket(uploadToken)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if claims.Purpose != "upload" || claims.Scope != scope || claims.PluginID != pluginID {
		return fmt.Errorf("上传凭证不属于当前调用")
	}
	s.Revoke(uploadToken)
	asset, err := s.store.Get(claims.FileRef, "", true)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if asset.TransferID == "" {
		return nil
	}
	return s.store.Fail(asset.FileRef, asset.TransferID, "插件已取消上传")
}

func (s *Service) lookupTicket(token string) (TicketClaims, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(time.Now().Unix())
	claims, ok := s.tickets[token]
	return claims, ok, nil
}

// SetPluginSender 注入插件文件发送实现
func (s *Service) SetPluginSender(sender PluginSender) {
	s.senderMu.Lock()
	s.pluginSender = sender
	s.senderMu.Unlock()
}

// PluginSend 把附件发送到聊天流，对齐 files.send
func (s *Service) PluginSend(ctx context.Context, fileRef, sessionID string) (map[string]any, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	s.senderMu.RLock()
	sender := s.pluginSender
	s.senderMu.RUnlock()
	if sender == nil {
		return nil, fmt.Errorf("文件发送尚未接入")
	}
	if _, err := s.Ensure(ctx, fileRef, sessionID, false); err != nil {
		return nil, err
	}
	return sender(ctx, fileRef, sessionID)
}
