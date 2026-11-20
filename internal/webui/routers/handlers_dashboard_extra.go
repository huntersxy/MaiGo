package webuiapi

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/platform_io"
)

// 本文件对齐 ../MaiBot/src/webui/routers 的 avatar.py、bot_accounts.py、agent.py
// 头像：internal/webui 本地缓存 + Deps.Avatar 外部平台；bot-accounts/agent 依赖注入端口

const (
	maxAvatarBytes      = 5 * 1024 * 1024
	avatarBrowserMaxAge = 3 * 60 * 60 // 对齐 avatar.py AVATAR_BROWSER_MAX_AGE
	// Dashboard 会并行加载多个头像，限制每个远程头像占用浏览器 HTTP 连接的时间。
	avatarRequestTimeout = 2 * time.Second
)

var (
	webuiUserIDPattern = regexp.MustCompile("^webui_[A-Za-z0-9_-]+$")
	avatarSuffixes     = []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp"}
)

func (d Deps) avatarCacheRoot() string {
	return filepath.Join(d.InstanceDir, "data", "avatar")
}

func sanitizeAvatarSegment(raw string) string {
	var builder strings.Builder
	previousUnderscore := false
	for _, char := range strings.TrimSpace(raw) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			builder.WriteRune(char)
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			builder.WriteByte('_')
			previousUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func avatarSuffixSupported(suffix string) bool {
	lower := strings.ToLower(suffix)
	for _, candidate := range avatarSuffixes {
		if candidate == lower {
			return true
		}
	}
	return false
}

func detectAvatarSuffix(imageBytes []byte) string {
	switch {
	case len(imageBytes) >= 3 && imageBytes[0] == 0xff && imageBytes[1] == 0xd8 && imageBytes[2] == 0xff:
		return ".jpg"
	case len(imageBytes) >= 8 && string(imageBytes[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png"
	case len(imageBytes) >= 6 && (string(imageBytes[:6]) == "GIF87a" || string(imageBytes[:6]) == "GIF89a"):
		return ".gif"
	case len(imageBytes) >= 12 && string(imageBytes[:4]) == "RIFF" && string(imageBytes[8:12]) == "WEBP":
		return ".webp"
	case len(imageBytes) >= 2 && string(imageBytes[:2]) == "BM":
		return ".bmp"
	}
	return ""
}

func (d Deps) avatarCachePath(platform string, targetID string, suffix string, group bool) (string, error) {
	normalizedPlatform := sanitizeAvatarSegment(strings.ToLower(platform))
	cacheKey := strings.TrimSpace(targetID)
	if group {
		cacheKey = "group_" + cacheKey
	}
	normalizedTarget := sanitizeAvatarSegment(cacheKey)
	if normalizedPlatform == "" || normalizedTarget == "" {
		return "", newTransferError(http.StatusBadRequest, "头像参数不合法")
	}
	if !avatarSuffixSupported(suffix) {
		suffix = ".jpg"
	}
	return filepath.Join(d.avatarCacheRoot(), normalizedPlatform, normalizedTarget+suffix), nil
}

func (d Deps) findCachedAvatar(platform string, targetID string, group bool) string {
	basePath, err := d.avatarCachePath(platform, targetID, ".jpg", group)
	if err != nil {
		return ""
	}
	stem := strings.TrimSuffix(basePath, ".jpg")
	for _, suffix := range avatarSuffixes {
		candidate := stem + suffix
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func avatarMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	return "image/jpeg"
}

func writeNoAvatar(w http.ResponseWriter, status string, cacheControl string) {
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("X-Avatar-Status", status)
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusNoContent)
}

func avatarETag(info os.FileInfo) string {
	digest := sha1.Sum([]byte(strconv.FormatInt(info.ModTime().UnixNano(), 10) + "-" + strconv.FormatInt(info.Size(), 10)))
	return "\"" + hex.EncodeToString(digest[:]) + "\""
}

func avatarETagMatches(header string, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(candidate), "W/"))
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func (d Deps) handleAvatarUploadWebUIUser(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if err := r.ParseMultipartForm(maxAvatarBytes + 1024); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "上传文件解析失败: " + err.Error()})
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "缺少头像文件"})
		return
	}
	defer file.Close()
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "WebUI 用户 ID 不合法"})
		return
	}
	if userID != "owner" && !webuiUserIDPattern.MatchString(userID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "WebUI 用户 ID 不合法"})
		return
	}
	imageBytes, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "读取头像文件失败: " + err.Error()})
		return
	}
	if len(imageBytes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "头像文件为空"})
		return
	}
	if len(imageBytes) > maxAvatarBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "头像文件不能超过 5 MB"})
		return
	}
	contentType := ""
	if header != nil {
		contentType = header.Header.Get("Content-Type")
	}
	normalizedType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if normalizedType != "" && normalizedType != "application/octet-stream" && !strings.HasPrefix(normalizedType, "image/") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"detail": "头像文件必须是图片"})
		return
	}
	suffix := detectAvatarSuffix(imageBytes)
	if suffix == "" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"detail": "仅支持 JPG、PNG、WebP、GIF 或 BMP 图片"})
		return
	}
	cachePath, err := d.avatarCachePath("internal", userID, suffix, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "头像保存路径不合法"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存头像失败: " + err.Error()})
		return
	}
	for _, existingSuffix := range avatarSuffixes {
		existing := strings.TrimSuffix(cachePath, suffix) + existingSuffix
		if existing == cachePath {
			continue
		}
		_ = os.Remove(existing)
	}
	if err := os.WriteFile(cachePath, imageBytes, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存头像失败: " + err.Error()})
		return
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存头像失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"avatar_url": fmt.Sprintf("/api/webui/avatar?platform=internal&user_id=%s&v=%d",
			url.QueryEscape(userID), info.ModTime().UnixNano()),
	})
}

func (d Deps) handleAvatarGet(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	query := r.URL.Query()
	platform := strings.TrimSpace(query.Get("platform"))
	userID := strings.TrimSpace(query.Get("user_id"))
	groupID := strings.TrimSpace(query.Get("group_id"))
	accountID := strings.TrimSpace(query.Get("account_id"))
	scope := strings.TrimSpace(query.Get("scope"))
	forceRefresh := query.Get("force_refresh") == "true" || query.Get("force_refresh") == "1"
	if (groupID != "") == (userID != "") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "必须且只能指定 user_id 或 group_id"})
		return
	}
	if platform == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "头像参数不合法"})
		return
	}
	targetID := userID
	targetType := "user"
	group := false
	if groupID != "" {
		targetID = groupID
		targetType = "group"
		group = true
	}
	normalizedPlatform, normalizedTarget, err := platformio.ValidateAvatarTarget(platform, targetID, targetType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if normalizedPlatform == "internal" || normalizedPlatform == "webui" {
		cachePath := d.findCachedAvatar(normalizedPlatform, normalizedTarget, group)
		if cachePath == "" {
			writeNoAvatar(w, "missing", "private, no-cache")
			return
		}
		serveAvatarFile(w, r, cachePath, "private, no-cache", forceRefresh)
		return
	}
	if d.Avatar == nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"detail": "头像获取失败：主程序头像服务未接线（platform=" + normalizedPlatform + "）",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), avatarRequestTimeout)
	defer cancel()
	result, err := d.Avatar.GetAvatar(ctx, normalizedPlatform, normalizedTarget, targetType, accountID, scope, forceRefresh)
	if errors.Is(err, context.DeadlineExceeded) {
		writeNoAvatar(w, string(platformio.AvatarStatusMissing), "private, no-cache")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"detail": fmt.Sprintf("头像获取失败：%T: %v", err, err),
		})
		return
	}
	if result.Status != platformio.AvatarStatusAvailable {
		writeNoAvatar(w, string(result.Status), "private, max-age=300")
		return
	}
	maxAge := avatarBrowserMaxAge
	remaining := int(result.ExpiresAt - float64(time.Now().Unix()))
	if remaining < maxAge {
		maxAge = remaining
	}
	if maxAge < 0 {
		maxAge = 0
	}
	cacheControl := fmt.Sprintf("private, max-age=%d", maxAge)
	if forceRefresh {
		cacheControl = "private, no-store"
	}
	serveAvatarFile(w, r, result.Path, cacheControl, forceRefresh)
}

func serveAvatarFile(w http.ResponseWriter, r *http.Request, cachePath, cacheControl string, forceRefresh bool) {
	info, err := os.Stat(cachePath)
	if err != nil {
		writeNoAvatar(w, "missing", "private, no-cache")
		return
	}
	etag := avatarETag(info)
	headers := w.Header()
	headers.Set("Cache-Control", cacheControl)
	headers.Set("X-Robots-Tag", "noindex, nofollow")
	headers.Set("ETag", etag)
	if !forceRefresh {
		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && avatarETagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	headers.Set("Content-Type", avatarMediaType(cachePath))
	http.ServeFile(w, r, cachePath)
}
