package emoji

import (
	"errors"
	"fmt"
	"MaiGo/internal/webui/httputil"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"MaiGo/internal/emoji_system"
)

// emojiUploadMaxBytes 单次上传请求体上限，对齐 Dashboard 表情包文件量级
const emojiUploadMaxBytes = 64 << 20

// parseFormBool 解析 multipart 布尔字段，缺省返回 fallback
func parseFormBool(r *http.Request, key string, fallback bool) (bool, bool) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return value, true
}

func (d Deps) handleEmojiUpload(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/upload"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(emojiUploadMaxBytes); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeValidationError(w, "缺少上传文件")
		return
	}
	defer file.Close()
	if _, ok := parseFormBool(r, "is_registered", true); !ok {
		writeValidationError(w, "is_registered 必须是布尔值")
		return
	}
	content, err := readMultipartFile(file, emojiUploadMaxBytes)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadRequest, "文件内容为空")
		return
	}
	declared := header.Header.Get("Content-Type")
	mimeType := detectUploadMime(declared, content)
	if _, allowed := emojiAllowedTypes[mimeType]; !allowed {
		httputil.WriteAPIError(w, http.StatusBadRequest,
			fmt.Sprintf("不支持的文件类型: %s，支持: image/jpeg, image/png, image/gif, image/webp", mimeType))
		return
	}
	item, message, _, err := service.Upload(r.Context(), emojisystem.WebUIEmojiUpload{
		Content:     content,
		FileName:    header.Filename,
		Description: r.FormValue("description"),
		Emotion:     r.FormValue("emotion"),
	})
	if err != nil {
		if errors.Is(err, emojisystem.ErrEmojiInvalidImage) {
			httputil.WriteAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		httputil.WriteAPIError(w, http.StatusInternalServerError, "上传失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": message,
		"data":    emojiToWebUI(item),
	})
}

func (d Deps) handleEmojiBatchUpload(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/batch/upload"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(emojiUploadMaxBytes); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	files := r.MultipartForm.File["files"]
	if _, ok := parseFormBool(r, "is_registered", true); !ok {
		writeValidationError(w, "is_registered 必须是布尔值")
		return
	}
	emotion := r.FormValue("emotion")
	details := make([]map[string]any, 0, len(files))
	uploaded, failed := 0, 0
	for _, header := range files {
		detail, ok := d.uploadOneBatchFile(r, service, header, emotion)
		if ok {
			uploaded++
		} else {
			failed++
		}
		details = append(details, detail)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"total":    len(files),
		"uploaded": uploaded,
		"failed":   failed,
		"details":  details,
		"message":  fmt.Sprintf("成功上传 %d 个，失败 %d 个", uploaded, failed),
	})
}

func (d Deps) uploadOneBatchFile(r *http.Request, service *emojisystem.WebUIService, header *multipart.FileHeader, emotion string) (map[string]any, bool) {
	detail := map[string]any{"filename": header.Filename}
	fail := func(message string) (map[string]any, bool) {
		detail["success"] = false
		detail["error"] = message
		return detail, false
	}
	file, err := header.Open()
	if err != nil {
		return fail("无法读取上传文件: " + err.Error())
	}
	defer file.Close()
	content, err := readMultipartFile(file, emojiUploadMaxBytes)
	if err != nil || len(content) == 0 {
		return fail("文件内容为空")
	}
	mimeType := detectUploadMime(header.Header.Get("Content-Type"), content)
	if _, allowed := emojiAllowedTypes[mimeType]; !allowed {
		return fail("不支持的文件类型: " + mimeType)
	}
	item, _, existed, err := service.Upload(r.Context(), emojisystem.WebUIEmojiUpload{
		Content:     content,
		FileName:    header.Filename,
		Description: "",
		Emotion:     emotion,
	})
	if err != nil {
		if errors.Is(err, emojisystem.ErrEmojiInvalidImage) {
			return fail("无效的图片: " + err.Error())
		}
		return fail(err.Error())
	}
	detail["success"] = true
	detail["id"] = item.ID
	if existed {
		detail["existed"] = true
	}
	return detail, true
}

// readMultipartFile 读取上传文件内容，超过上限报错
func readMultipartFile(file multipart.File, limit int64) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	var total int64
	for {
		n, err := file.Read(chunk)
		total += int64(n)
		if total > limit {
			return nil, errors.New("文件过大")
		}
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}
