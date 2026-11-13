package filesvc

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"maibot-go/internal/webui/middleware"
)

// MountOpts 控制需管理员会话的上传路由
type MountOpts struct {
	AdminAuth *auth.Manager
	ChatHooks ChatHooks
}

// Mount 将授权下载、流式上传与管理路由挂到 chi
func Mount(r chi.Router, svc *Service, opts MountOpts) {
	if r == nil || svc == nil {
		return
	}
	r.Get("/api/files", svc.handleFileList(opts.AdminAuth, opts.ChatHooks))
	r.Get("/api/webui/files", svc.handleFileList(opts.AdminAuth, opts.ChatHooks))
	r.Route("/api/files", func(files chi.Router) {
		files.Get("/transfers/{token}", svc.handleDownload(false))
		files.Head("/transfers/{token}", svc.handleDownload(true))
		files.Put("/transfers/{token}", svc.handleUploadTransfer)
		files.Post("/uploads", svc.handleAdminUpload(opts.AdminAuth))
		files.Post("/upload", svc.handleUpload(opts.AdminAuth))
		files.Put("/upload", svc.handleUpload(opts.AdminAuth))
		files.Get("/usage", svc.handleFileUsage(opts.AdminAuth))
		files.Get("/{file_ref}/text", svc.handleReadText(opts.AdminAuth))
		files.Delete("/{file_ref}", svc.handleFileDelete(opts.AdminAuth))
	})
	r.Route("/api/webui/files", func(files chi.Router) {
		files.Get("/chats", svc.handleFileChats(opts.AdminAuth, opts.ChatHooks))
		files.Get("/usage", svc.handleFileUsage(opts.AdminAuth))
		files.Post("/uploads", svc.handleAdminUpload(opts.AdminAuth))
		files.Post("/chat-uploads", svc.handleChatUpload(opts.AdminAuth, opts.ChatHooks))
		files.Post("/{file_ref}/download", svc.handlePrepareDownload(opts.AdminAuth))
		files.Post("/{file_ref}/send", svc.handleSendFile(opts.AdminAuth, opts.ChatHooks))
		files.Get("/{file_ref}/text", svc.handleReadText(opts.AdminAuth))
		files.Delete("/{file_ref}", svc.handleFileDelete(opts.AdminAuth))
	})
}

func (s *Service) handleUploadTransfer(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	claims, err := s.Claims(token, "upload")
	if err != nil {
		writeFileHTTPError(w, err)
		return
	}
	if strings.TrimSpace(claims.FileRef) == "" {
		writeFileError(w, http.StatusForbidden, "上传凭证缺少文件引用")
		return
	}
	sessionID := strings.TrimSpace(claims.SessionID)
	if claims.Scope != "" {
		scope, scopeErr := s.Claims(claims.Scope, "plugin")
		if scopeErr != nil || scope.PluginID != claims.PluginID || strings.TrimSpace(scope.SessionID) == "" {
			writeFileError(w, http.StatusForbidden, "插件文件凭证不匹配")
			return
		}
		sessionID = scope.SessionID
	} else if claims.PluginID != "" {
		writeFileError(w, http.StatusForbidden, "插件文件凭证不匹配")
		return
	}
	s.Revoke(token)
	asset, err := s.store.Get(claims.FileRef, sessionID, false)
	if err != nil {
		writeFileHTTPError(w, err)
		return
	}
	if asset.State != "uploading" && asset.State != "downloading" {
		writeFileError(w, http.StatusConflict, "附件当前不可上传")
		return
	}
	s.receiveUpload(w, r, asset)
}

func (s *Service) handleUpload(adminAuth *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}

		sessionID, uploadToken, viaUploadTicket, fileRef, err := resolveUploadAuth(r, adminAuth, s)
		if err != nil {
			writeFileError(w, http.StatusUnauthorized, err.Error())
			return
		}

		mimeType := strings.TrimSpace(r.Header.Get("Content-Type"))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		var asset Asset
		if fileRef != "" {
			asset, err = s.store.Get(fileRef, sessionID, false)
		} else {
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				writeFileError(w, http.StatusBadRequest, "name 不能为空")
				return
			}
			asset, err = s.BeginUpload(sessionID, name, mimeType)
		}
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		if viaUploadTicket {
			s.Revoke(uploadToken)
		}
		s.receiveUpload(w, r, asset)
	}
}

func (s *Service) receiveUpload(w http.ResponseWriter, r *http.Request, asset Asset) {
	transfer := asset.TransferID
	failed := true
	var failErr error
	defer func() {
		if failed {
			_ = s.store.Fail(asset.FileRef, transfer, "上传失败: "+uploadErrorKind(failErr))
		}
	}()

	if s.cfg.TransferTimeout > 0 {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(s.cfg.TransferTimeout))
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	}
	buffer := make([]byte, transferChunkSize)
	for {
		n, readErr := r.Body.Read(buffer)
		if n > 0 {
			if err := s.requireEnabled(); err != nil {
				failErr = err
				writeFileHTTPError(w, err)
				return
			}
			if err := s.store.Append(asset.FileRef, transfer, buffer[:n]); err != nil {
				failErr = err
				writeFileHTTPError(w, err)
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			failErr = readErr
			if isTimeoutError(readErr) {
				writeFileError(w, http.StatusGatewayTimeout, "文件传输超时")
				return
			}
			writeFileError(w, http.StatusBadRequest, "读取请求体失败")
			return
		}
	}
	finished, err := s.store.Finish(asset.FileRef, transfer)
	if err != nil {
		failErr = err
		writeFileHTTPError(w, err)
		return
	}
	failed = false
	writeFileJSON(w, http.StatusOK, PublicView(finished))
}

// uploadErrorKind 对齐 Python type(exc).__name__，供 file_assets.error 展示
func uploadErrorKind(err error) string {
	if err == nil {
		return "Error"
	}
	if isTimeoutError(err) {
		return "TimeoutError"
	}
	if errors.Is(err, ErrDisabled) {
		return "PermissionError"
	}
	if errors.Is(err, ErrLimit) {
		return "FileLimitError"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return "IncompleteReadError"
	}
	return "Error"
}

func (s *Service) handleAdminUpload(adminAuth *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if sessionID == "" {
			writeFileError(w, http.StatusBadRequest, "session_id 不能为空")
			return
		}
		if name == "" {
			writeFileError(w, http.StatusBadRequest, "name 不能为空")
			return
		}
		s.startSessionUpload(w, r, sessionID, name)
	}
}

func (s *Service) handleFileList(adminAuth *auth.Manager, hooks ChatHooks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		offset, limit := 0, 50
		if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeFileError(w, http.StatusBadRequest, "分页参数无效")
				return
			}
			offset = parsed
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeFileError(w, http.StatusBadRequest, "分页参数无效")
				return
			}
			limit = parsed
		}
		items, err := s.store.List(strings.TrimSpace(r.URL.Query().Get("session_id")), true, offset, limit)
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		appendFileChatNames(r.Context(), items, hooks)
		usage, err := s.store.Usage()
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		writeFileJSON(w, http.StatusOK, map[string]any{"items": items, "usage": usage})
	}
}

func (s *Service) handleFileUsage(adminAuth *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		usage, err := s.store.Usage()
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		writeFileJSON(w, http.StatusOK, usage)
	}
}

func (s *Service) handlePrepareDownload(adminAuth *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		fileRef := chi.URLParam(r, "file_ref")
		if _, err := s.Ensure(r.Context(), fileRef, "", true); err != nil {
			writeFileHTTPError(w, err)
			return
		}
		downloadURL, err := s.DownloadURL(fileRef, "", true, "")
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		parsed, err := url.Parse(downloadURL)
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		writeFileJSON(w, http.StatusOK, map[string]any{"url": path.Join("/api/files/transfers", path.Base(parsed.Path))})
	}
}

func (s *Service) handleReadText(adminAuth *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		offset, limit := 0, 12000
		var err error
		if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
			offset, err = strconv.Atoi(raw)
			if err != nil {
				writeFileError(w, http.StatusBadRequest, "文本分页参数无效")
				return
			}
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil {
				writeFileError(w, http.StatusBadRequest, "文本分页参数无效")
				return
			}
		}
		encoding := strings.TrimSpace(r.URL.Query().Get("encoding"))
		if encoding == "" {
			encoding = "utf-8-sig"
		}
		fileRef := chi.URLParam(r, "file_ref")
		if _, err := s.Ensure(r.Context(), fileRef, "", true); err != nil {
			writeFileHTTPError(w, err)
			return
		}
		result, err := s.ReadText(fileRef, "", true, offset, limit, encoding)
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		writeFileJSON(w, http.StatusOK, result)
	}
}

func (s *Service) handleFileDelete(adminAuth *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		cacheOnly := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("cache_only")), "true") ||
			strings.TrimSpace(r.URL.Query().Get("cache_only")) == "1"
		if err := s.store.Remove(chi.URLParam(r, "file_ref"), cacheOnly); err != nil {
			writeFileHTTPError(w, err)
			return
		}
		writeFileJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func resolveUploadAuth(r *http.Request, adminAuth *auth.Manager, svc *Service) (sessionID, uploadToken string, viaTicket bool, fileRef string, err error) {
	uploadToken = strings.TrimSpace(r.URL.Query().Get("upload_token"))
	if uploadToken == "" {
		uploadToken = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if uploadToken == "" {
		if header := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(header), "bearer ") {
			uploadToken = strings.TrimSpace(header[7:])
		}
	}
	if uploadToken != "" {
		claims, claimErr := svc.Claims(uploadToken, "upload")
		if claimErr != nil {
			return "", "", false, "", claimErr
		}
		sessionID = strings.TrimSpace(claims.SessionID)
		if claims.Scope != "" {
			scope, scopeErr := svc.Claims(claims.Scope, "plugin")
			if scopeErr != nil || scope.PluginID != claims.PluginID || strings.TrimSpace(scope.SessionID) == "" {
				return "", "", false, "", errUploadAuth
			}
			sessionID = scope.SessionID
		} else if claims.PluginID != "" {
			return "", "", false, "", errUploadAuth
		}
		if sessionID == "" {
			return "", "", false, "", errUploadAuth
		}
		return sessionID, uploadToken, true, strings.TrimSpace(claims.FileRef), nil
	}

	if !adminSessionOK(r, adminAuth) {
		return "", "", false, "", errUploadAuth
	}
	sessionID = strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		return "", "", false, "", errMissingSession
	}
	return sessionID, "", false, "", nil
}

var (
	errUploadAuth     = errText("未授权的上传请求")
	errMissingSession = errText("session_id 不能为空")
)

type errText string

func (e errText) Error() string { return string(e) }

func adminSessionOK(r *http.Request, adminAuth *auth.Manager) bool {
	if adminAuth == nil {
		return false
	}
	cookie, err := r.Cookie(auth.WebUISessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	return adminAuth.ValidateToken(cookie.Value)
}

func requireFileAdmin(w http.ResponseWriter, r *http.Request, adminAuth *auth.Manager) bool {
	if adminSessionOK(r, adminAuth) {
		return true
	}
	writeFileError(w, http.StatusUnauthorized, "未认证")
	return false
}

func writeFileJSON(w http.ResponseWriter, status int, value any) {
	body, err := sonic.Marshal(value)
	if err != nil {
		writeFileError(w, http.StatusInternalServerError, "序列化响应失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func requestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if proto := strings.TrimSpace(parts[0]); proto == "http" || proto == "https" {
			scheme = proto
		}
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
