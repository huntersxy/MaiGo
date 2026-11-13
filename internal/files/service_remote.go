package filesvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// RemoteURLResolver 把平台适配器来源解析成可直接下载的 HTTP(S) 地址
//
// 对齐 MaiBot FileService.resolve_url：解析失败必须返回真实错误，
// 不能由 filesvc 猜测地址。
type RemoteURLResolver interface {
	ResolveFileURL(ctx context.Context, source map[string]any) (string, error)
}

// RemoteURLResolverFunc 让调用方用函数注入适配器解析能力
type RemoteURLResolverFunc func(ctx context.Context, source map[string]any) (string, error)

// ResolveFileURL 实现 RemoteURLResolver
func (f RemoteURLResolverFunc) ResolveFileURL(ctx context.Context, source map[string]any) (string, error) {
	return f(ctx, source)
}

// SetRemoteURLResolver 注入适配器文件解析器；nil 表示尚未接入
func (s *Service) SetRemoteURLResolver(resolver RemoteURLResolver) {
	s.resolverMu.Lock()
	s.remoteResolver = resolver
	s.resolverMu.Unlock()
}

// ResolveURL 调用已注入的适配器解析器
func (s *Service) ResolveURL(ctx context.Context, source map[string]any) (string, error) {
	if err := s.requireEnabled(); err != nil {
		return "", err
	}
	s.resolverMu.RLock()
	resolver := s.remoteResolver
	s.resolverMu.RUnlock()
	if resolver == nil {
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

// Ensure 确保远程附件已经落盘；本地原件未就绪时返回真实错误
func (s *Service) Ensure(ctx context.Context, fileRef, sessionID string, admin bool) (Asset, error) {
	if err := s.requireEnabled(); err != nil {
		return Asset{}, err
	}
	asset, err := s.store.Get(fileRef, sessionID, admin)
	if err != nil {
		return Asset{}, err
	}
	if asset.State == "ready" {
		_ = s.store.Touch(fileRef, sessionID, admin)
		return asset, nil
	}
	if asset.Origin != "remote" {
		return Asset{}, errors.New("本地原件不存在，无法从平台重新下载")
	}
	asset, err = s.store.BeginDownload(fileRef, sessionID, admin)
	if err != nil {
		return Asset{}, err
	}
	if asset.State == "ready" {
		return asset, nil
	}
	transfer := asset.TransferID
	var source map[string]any
	if strings.TrimSpace(asset.Source) != "" {
		if err := json.Unmarshal([]byte(asset.Source), &source); err != nil {
			_ = s.store.Fail(fileRef, transfer, "下载失败: JSONDecodeError")
			return Asset{}, fmt.Errorf("附件来源信息无效: %w", err)
		}
	}
	if len(source) == 0 {
		_ = s.store.Fail(fileRef, transfer, "下载失败: 来源缺失")
		return Asset{}, errors.New("附件来源信息缺失")
	}
	timeout := s.cfg.TransferTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rawURL, err := s.ResolveURL(ctx, source)
	if err != nil {
		_ = s.store.Fail(fileRef, transfer, "下载失败: "+remoteResolveErrorKind(err))
		return Asset{}, wrapRemoteResolveError(err)
	}
	var appendErr error
	downloadErr := downloadRemote(ctx, rawURL, s.cfg.TrustedDownloadOrigins, timeout, func(chunk []byte) error {
		if err := s.requireEnabled(); err != nil {
			appendErr = err
			return err
		}
		if err := s.store.Append(fileRef, transfer, chunk); err != nil {
			appendErr = err
			return err
		}
		return nil
	})
	if appendErr != nil {
		_ = s.store.Fail(fileRef, transfer, "下载失败: "+remoteErrorKind(appendErr))
		return Asset{}, appendErr
	}
	if downloadErr != nil {
		_ = s.store.Fail(fileRef, transfer, "下载失败: "+remoteErrorKind(downloadErr))
		return Asset{}, wrapRemoteDownloadError(downloadErr)
	}
	finished, err := s.store.Finish(fileRef, transfer)
	if err != nil {
		_ = s.store.Fail(fileRef, transfer, "下载失败: "+remoteErrorKind(err))
		return Asset{}, err
	}
	return finished, nil
}

// RemoteFileInput 是一次入站文件组件的 filesvc 侧投影
//
// 调用方负责从消息组件和 message.file_source 填入字段；filesvc 不反向依赖
// Agent 的消息模型。
type RemoteFileInput struct {
	SessionID string
	MessageID string
	GroupID   string
	UserID    string
	FileRef   string
	FileID    string
	Busid     int
	Name      string
	MimeType  string
	Size      int64
	Source    map[string]any
}

// IngestFile 登记入站远程文件；第二个返回值表示是否处理了文件组件
func (s *Service) IngestFile(ctx context.Context, input RemoteFileInput) (Asset, bool, error) {
	if !s.Enabled() {
		return Asset{}, false, nil
	}
	if err := s.requireEnabled(); err != nil {
		return Asset{}, false, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return Asset{}, false, errors.New("聊天流不存在")
	}
	if strings.TrimSpace(input.FileRef) != "" {
		asset, err := s.store.Get(input.FileRef, sessionID, false)
		return asset, true, err
	}
	source := cloneRemoteSource(input.Source)
	pluginID := remoteString(source["plugin_id"])
	fileID := strings.TrimSpace(input.FileID)
	if pluginID == "" || fileID == "" {
		return Asset{}, false, nil
	}
	rawFiles, ok := source["files"]
	if !ok || !sourceFilesContain(rawFiles, fileID, input.Busid) {
		return Asset{}, true, &remotePermissionError{message: "附件定位信息已被入站 Hook 修改"}
	}
	if remoteString(source["group_id"]) != input.GroupID || remoteString(source["user_id"]) != input.UserID {
		return Asset{}, true, &remotePermissionError{message: "附件来源聊天已被入站 Hook 修改"}
	}
	delete(source, "files")
	source["file_id"] = fileID
	source["busid"] = input.Busid
	key, err := SourceKey(source)
	if err != nil {
		return Asset{}, true, err
	}
	asset, err := s.store.RegisterRemote(sessionID, input.MessageID, input.Name, input.MimeType, input.Size, source, key)
	if err != nil {
		return Asset{}, true, err
	}
	return asset, true, nil
}

func cloneRemoteSource(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func sourceFilesContain(raw any, fileID string, busid int) bool {
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if sourceFileMatches(value, fileID, busid) {
				return true
			}
		}
	case []map[string]any:
		for _, value := range values {
			if sourceFileMatches(value, fileID, busid) {
				return true
			}
		}
	case []string:
		for _, value := range values {
			if value == fileID {
				return true
			}
		}
	}
	return false
}

func sourceFileMatches(value any, fileID string, busid int) bool {
	switch item := value.(type) {
	case map[string]any:
		return remoteString(item["file_id"]) == fileID && remoteInt(item["busid"]) == busid
	case []any:
		if len(item) < 2 {
			return false
		}
		return remoteString(item[0]) == fileID && remoteInt(item[1]) == busid
	default:
		return false
	}
}

func remoteString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func remoteInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	case string:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(typed), "%d", &n)
		return n
	default:
		return 0
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func remoteResolveErrorKind(err error) string {
	if err == nil {
		return "RuntimeError"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutError"
	}
	if errors.Is(err, ErrForbidden) || isRemotePermissionError(err) {
		return "PermissionError"
	}
	if errors.Is(err, errRemoteInvalidURL) {
		return "ValueError"
	}
	return "RuntimeError"
}

func remoteErrorKind(err error) string {
	if err == nil {
		return "Error"
	}
	var statusErr *remoteHTTPStatusError
	if errors.As(err, &statusErr) {
		return "ClientResponseError"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutError"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "TimeoutError"
	}
	if errors.Is(err, ErrForbidden) {
		return "PermissionError"
	}
	if errors.Is(err, errRemoteInvalidURL) {
		return "ValueError"
	}
	if errors.Is(err, ErrLimit) {
		return "FileLimitError"
	}
	return "ClientError"
}

// wrapRemoteResolveError 对齐 Python ensure 对适配器解析错误的分类
//
// ValueError/PermissionError/FileLimitError 原样返回，其余 RuntimeError 在 HTTP 层映射为 502
func wrapRemoteResolveError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("附件下载失败: TimeoutError，可稍后重新请求")
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, errRemoteInvalidURL) ||
		errors.Is(err, ErrLimit) || errors.Is(err, ErrBusy) {
		return err
	}
	if isRemotePermissionError(err) {
		return &remotePermissionError{message: err.Error()}
	}
	return &remoteDownloadError{message: err.Error()}
}

// isRemotePermissionError 识别平台适配器返回的来源校验失败
func isRemotePermissionError(err error) bool {
	var tagged interface{ IsFilePermission() bool }
	return errors.As(err, &tagged) && tagged.IsFilePermission()
}

// remotePermissionError 标记适配器来源权限错误，HTTP 层映射为 403
type remotePermissionError struct {
	message string
}

func (e *remotePermissionError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// IsFilePermission 保留权限错误标记，便于二次包装后继续识别
func (e *remotePermissionError) IsFilePermission() bool { return true }

// Unwrap 让 ErrForbidden 分类和旧的 errors.Is 判断继续生效
func (e *remotePermissionError) Unwrap() error { return ErrForbidden }

// remoteDownloadError 标记已转换成用户文案的远程下载失败，HTTP 层映射为 502
type remoteDownloadError struct {
	message string
}

func (e *remoteDownloadError) Error() string { return e.message }

func wrapRemoteDownloadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, errRemoteInvalidURL) {
		return err
	}
	var statusErr *remoteHTTPStatusError
	if errors.As(err, &statusErr) {
		return &remoteDownloadError{message: "附件下载失败: ClientResponseError，可稍后重新请求"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &remoteDownloadError{message: "附件下载失败: TimeoutError，可稍后重新请求"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &remoteDownloadError{message: "附件下载失败: TimeoutError，可稍后重新请求"}
	}
	if errors.Is(err, ErrForbidden) {
		return err
	}
	if errors.Is(err, errRemoteInvalidURL) {
		return err
	}
	return &remoteDownloadError{message: "附件下载失败: ClientError，可稍后重新请求"}
}
