package emoji

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"maibot-go/internal/webui/httputil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 本文件对齐 ../MaiBot/src/webui/routers/user_emoji.py 的 WebUI 本地用户表情包

const (
	maxUserEmojiBytes = 2 * 1024 * 1024
	maxUserEmojiCount = 100
)

var (
	userEmojiIDPattern       = regexp.MustCompile("^[a-f0-9]{32}$")
	supportedUserEmojiSuffix = map[string]bool{".gif": true, ".jpg": true, ".png": true, ".webp": true}
)

func (d Deps) userEmojiRoot() string {
	return filepath.Join(d.InstanceDir, "data", "webui_user_emojis")
}

func (d Deps) resolveUserEmojiDirectory(userID string) (string, error) {
	normalized := strings.TrimSpace(userID)
	if !webuiUserIDPattern.MatchString(normalized) {
		return "", newTransferError(http.StatusBadRequest, "WebUI 用户 ID 不合法")
	}
	root, err := filepath.Abs(d.userEmojiRoot())
	if err != nil {
		return "", newTransferError(http.StatusBadRequest, "用户表情包目录不合法")
	}
	directory := filepath.Join(root, normalized)
	if relative, relErr := filepath.Rel(root, directory); relErr != nil || strings.HasPrefix(relative, "..") {
		return "", newTransferError(http.StatusBadRequest, "用户表情包目录不合法")
	}
	return directory, nil
}

func (d Deps) resolveUserEmojiPath(userID string, emojiID string) (string, error) {
	normalizedID := strings.ToLower(strings.TrimSpace(emojiID))
	if !userEmojiIDPattern.MatchString(normalizedID) {
		return "", newTransferError(http.StatusBadRequest, "表情包 ID 不合法")
	}
	directory, err := d.resolveUserEmojiDirectory(userID)
	if err != nil {
		return "", err
	}
	suffixes := make([]string, 0, len(supportedUserEmojiSuffix))
	for suffix := range supportedUserEmojiSuffix {
		suffixes = append(suffixes, suffix)
	}
	sort.Strings(suffixes)
	for _, suffix := range suffixes {
		candidate := filepath.Join(directory, normalizedID+suffix)
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", newTransferError(http.StatusNotFound, "表情包不存在")
}

func userEmojiContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gif":
		return "image/gif"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	return "image/png"
}

func (d Deps) buildUserEmojiRecord(userID string, path string) map[string]any {
	info, err := os.Stat(path)
	modified := time.Now()
	if err == nil {
		modified = info.ModTime()
	}
	emojiID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return map[string]any{
		"id":           emojiID,
		"content_type": userEmojiContentType(path),
		"content_url":  "/api/webui/user-emojis/" + emojiID + "/content?user_id=" + userID + "&v=" + itoa64(modified.UnixNano()),
		"created_at":   float64(modified.UnixNano()) / float64(time.Second),
	}
}

func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := make([]byte, 0, 20)
	for value > 0 {
		buf = append(buf, byte('0'+value%10))
		value /= 10
	}
	if negative {
		buf = append(buf, '-')
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

func (d Deps) listUserEmojiPaths(userID string) ([]string, error) {
	directory, err := d.resolveUserEmojiDirectory(userID)
	if err != nil {
		return nil, err
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return []string{}, nil
		}
		return nil, readErr
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		suffix := strings.ToLower(filepath.Ext(entry.Name()))
		if !supportedUserEmojiSuffix[suffix] {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), suffix)
		if !userEmojiIDPattern.MatchString(stem) {
			continue
		}
		paths = append(paths, filepath.Join(directory, entry.Name()))
	}
	sort.SliceStable(paths, func(i, j int) bool {
		left, leftErr := os.Stat(paths[i])
		right, rightErr := os.Stat(paths[j])
		if leftErr != nil || rightErr != nil {
			return paths[i] > paths[j]
		}
		return left.ModTime().UnixNano() > right.ModTime().UnixNano()
	})
	return paths, nil
}

func writeUserEmojiError(w http.ResponseWriter, err error) {
	if typed, ok := err.(*transferError); ok {
		httputil.WriteJSON(w, typed.Status, map[string]any{"detail": typed.Detail})
		return
	}
	httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + err.Error()})
}

func (d Deps) handleUserEmojisList(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "user_id 为必填查询参数"})
		return
	}
	paths, err := d.listUserEmojiPaths(userID)
	if err != nil {
		writeUserEmojiError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		items = append(items, d.buildUserEmojiRecord(userID, path))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": maxUserEmojiCount})
}

func generateUserEmojiID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func (d Deps) handleUserEmojisAdd(w http.ResponseWriter, r *http.Request) {
	// 认证由父包路由中间件统一处理，此处不再重复校验，
	// 否则直接挂载 MountUser 的调用方会拿到 401 而不是业务错误。
	if err := r.ParseMultipartForm(maxUserEmojiBytes + 1024); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "上传文件解析失败: " + err.Error()})
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "缺少表情包文件"})
		return
	}
	defer file.Close()
	imageBytes, err := io.ReadAll(io.LimitReader(file, maxUserEmojiBytes+1))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "读取表情包失败: " + err.Error()})
		return
	}
	if len(imageBytes) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "表情包文件为空"})
		return
	}
	if len(imageBytes) > maxUserEmojiBytes {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "单个表情包不能超过 2 MB"})
		return
	}
	contentType := ""
	if header != nil {
		contentType = header.Header.Get("Content-Type")
	}
	normalizedType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if normalizedType != "" && normalizedType != "application/octet-stream" && !strings.HasPrefix(normalizedType, "image/") {
		httputil.WriteJSON(w, http.StatusUnsupportedMediaType, map[string]any{"detail": "表情包文件必须是图片"})
		return
	}
	suffix := detectAvatarSuffix(imageBytes)
	if !supportedUserEmojiSuffix[suffix] {
		httputil.WriteJSON(w, http.StatusUnsupportedMediaType, map[string]any{"detail": "仅支持 JPG、PNG、WebP 或 GIF 表情包"})
		return
	}
	directory, err := d.resolveUserEmojiDirectory(userID)
	if err != nil {
		writeUserEmojiError(w, err)
		return
	}
	existing, err := d.listUserEmojiPaths(userID)
	if err != nil {
		writeUserEmojiError(w, err)
		return
	}
	if len(existing) >= maxUserEmojiCount {
		httputil.WriteJSON(w, http.StatusConflict, map[string]any{"detail": "用户表情包数量已达到 100 个上限"})
		return
	}
	emojiID, err := generateUserEmojiID()
	if err != nil {
		writeUserEmojiError(w, err)
		return
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		writeUserEmojiError(w, err)
		return
	}
	emojiPath := filepath.Join(directory, emojiID+suffix)
	if err := os.WriteFile(emojiPath, imageBytes, 0o644); err != nil {
		writeUserEmojiError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"item": d.buildUserEmojiRecord(strings.TrimSpace(userID), emojiPath)})
}

func (d Deps) handleUserEmojiContent(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	emojiID := chiURLParam(r, "emoji_id")
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "user_id 为必填查询参数"})
		return
	}
	emojiPath, err := d.resolveUserEmojiPath(userID, emojiID)
	if err != nil {
		writeUserEmojiError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", userEmojiContentType(emojiPath))
	http.ServeFile(w, r, emojiPath)
}

func (d Deps) handleUserEmojiDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	emojiID := chiURLParam(r, "emoji_id")
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "user_id 为必填查询参数"})
		return
	}
	emojiPath, err := d.resolveUserEmojiPath(userID, emojiID)
	if err != nil {
		writeUserEmojiError(w, err)
		return
	}
	if err := os.Remove(emojiPath); err != nil {
		writeUserEmojiError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "id": emojiID})
}
