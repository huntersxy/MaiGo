package emoji

import (
	"errors"
	"fmt"
	"maibot-go/internal/webui/httputil"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"maibot-go/internal/emoji_system"
)

var emojiAllowedTypes = map[string]struct{}{
	"image/jpeg": {}, "image/png": {}, "image/gif": {}, "image/webp": {},
}

func (d Deps) emojiService() *emojisystem.WebUIService {
	return emojisystem.DefaultRegistry().WebUI()
}

// requireEmojiService 表情包服务未就绪时返回 503，与其它 WebUI 缺依赖口径一致
func (d Deps) requireEmojiService(w http.ResponseWriter, _ string, detail string) (*emojisystem.WebUIService, bool) {
	d.applyEmojiFiltration()
	service := d.emojiService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, detail)
		return nil, false
	}
	return service, true
}

func emojiToWebUI(item emojisystem.WebUIEmoji) map[string]any {
	return map[string]any{
		"id":             item.ID,
		"full_path":      item.FullPath,
		"format":         item.Format,
		"emoji_hash":     item.Hash,
		"description":    item.Description,
		"query_count":    item.QueryCount,
		"usage_count":    item.QueryCount,
		"is_registered":  item.IsRegistered,
		"is_banned":      item.IsBanned,
		"status":         item.Status,
		"emotion":        item.Emotion,
		"record_time":    item.RecordTime,
		"register_time":  item.RegisterTime,
		"last_used_time": item.LastUsedTime,
	}
}

func (d Deps) handleEmojiList(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/list"
	if !d.requireAuth(w, r) {
		return
	}
	page, pageSize, ok := queryPagination(w, r, 20)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !emojisystem.IsValidEmojiStatus(status) {
		httputil.WriteAPIError(w, http.StatusBadRequest, "无效的表情包状态")
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	query := emojisystem.WebUIEmojiQuery{
		Page:      page,
		PageSize:  pageSize,
		Search:    strings.TrimSpace(r.URL.Query().Get("search")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Format:    strings.TrimSpace(r.URL.Query().Get("format")),
		SortBy:    strings.TrimSpace(r.URL.Query().Get("sort_by")),
		SortOrder: strings.TrimSpace(r.URL.Query().Get("sort_order")),
	}
	if query.SortBy == "" {
		query.SortBy = "register_time"
	}
	if query.SortOrder == "" {
		query.SortOrder = "desc"
	}
	isRegistered, ok := httputil.OptionalQueryBoolPtr(w, r, "is_registered")
	if !ok {
		return
	}
	query.IsRegistered = isRegistered
	isBanned, ok := httputil.OptionalQueryBoolPtr(w, r, "is_banned")
	if !ok {
		return
	}
	query.IsBanned = isBanned

	pageResult, err := service.List(r.Context(), query)
	if err != nil {
		if errors.Is(err, emojisystem.ErrEmojiStatusInvalid) {
			httputil.WriteAPIError(w, http.StatusBadRequest, "无效的表情包状态")
			return
		}
		httputil.WriteAPIError(w, http.StatusInternalServerError, "获取表情包列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(pageResult.Items))
	for _, item := range pageResult.Items {
		data = append(data, emojiToWebUI(item))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"total":     pageResult.Total,
		"page":      page,
		"page_size": pageSize,
		"data":      data,
	})
}

func (d Deps) handleEmojiDetail(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/{emoji_id}"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	id, ok := httputil.ParsePathID(w, chi.URLParam(r, "emoji_id"))
	if !ok {
		return
	}
	item, err := service.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, emojisystem.ErrEmojiNotFound) {
			httputil.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表情包", id))
			return
		}
		httputil.WriteAPIError(w, http.StatusInternalServerError, "获取表情包详情失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "data": emojiToWebUI(item)})
}

func (d Deps) handleEmojiUpdate(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/{emoji_id}"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	id, ok := httputil.ParsePathID(w, chi.URLParam(r, "emoji_id"))
	if !ok {
		return
	}
	var body struct {
		Description  *string `json:"description"`
		IsRegistered *bool   `json:"is_registered"`
		IsBanned     *bool   `json:"is_banned"`
		Emotion      *string `json:"emotion"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	update := emojisystem.WebUIEmojiUpdate{
		Description:  body.Description,
		Emotion:      body.Emotion,
		IsRegistered: body.IsRegistered,
		IsBanned:     body.IsBanned,
	}
	if update.Empty() {
		httputil.WriteAPIError(w, http.StatusBadRequest, "未提供任何需要更新的字段")
		return
	}
	item, fieldCount, err := service.Update(r.Context(), id, update)
	if err != nil {
		switch {
		case errors.Is(err, emojisystem.ErrEmojiNotFound):
			httputil.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表情包", id))
		case errors.Is(err, emojisystem.ErrEmojiReviewRejected), errors.Is(err, emojisystem.ErrEmojiFileMissing):
			httputil.WriteAPIError(w, http.StatusBadRequest, "表情包未通过内容审核，无法注册")
		default:
			httputil.WriteAPIError(w, http.StatusInternalServerError, "更新表情包失败: "+err.Error())
		}
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("成功更新 %d 个字段", fieldCount),
		"data":    emojiToWebUI(item),
	})
}

func (d Deps) handleEmojiDelete(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/{emoji_id}"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	id, ok := httputil.ParsePathID(w, chi.URLParam(r, "emoji_id"))
	if !ok {
		return
	}
	hash, err := service.Delete(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, emojisystem.ErrEmojiNotFound):
			httputil.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表情包", id))
		case errors.Is(err, emojisystem.ErrEmojiFileMissing):
			httputil.WriteAPIError(w, http.StatusInternalServerError, "删除表情包文件失败")
		default:
			httputil.WriteAPIError(w, http.StatusInternalServerError, "删除表情包失败: "+err.Error())
		}
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "成功删除表情包: " + hash,
	})
}

func (d Deps) handleEmojiRegister(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/{emoji_id}/register"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	id, ok := httputil.ParsePathID(w, chi.URLParam(r, "emoji_id"))
	if !ok {
		return
	}
	item, message, err := service.Register(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, emojisystem.ErrEmojiNotFound):
			httputil.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表情包", id))
		case errors.Is(err, emojisystem.ErrEmojiReviewRejected), errors.Is(err, emojisystem.ErrEmojiFileMissing):
			httputil.WriteAPIError(w, http.StatusBadRequest, "表情包未通过内容审核，无法注册")
		default:
			httputil.WriteAPIError(w, http.StatusInternalServerError, "注册表情包失败: "+err.Error())
		}
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": message,
		"data":    emojiToWebUI(item),
	})
}

func (d Deps) handleEmojiBan(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/{emoji_id}/ban"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	id, ok := httputil.ParsePathID(w, chi.URLParam(r, "emoji_id"))
	if !ok {
		return
	}
	item, err := service.Ban(r.Context(), id)
	if err != nil {
		if errors.Is(err, emojisystem.ErrEmojiNotFound) {
			httputil.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表情包", id))
			return
		}
		httputil.WriteAPIError(w, http.StatusInternalServerError, "禁用表情包失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "表情包禁用成功",
		"data":    emojiToWebUI(item),
	})
}

func (d Deps) handleEmojiThumbnail(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/{emoji_id}/thumbnail"
	if !d.emojiThumbnailAuthorized(r) {
		httputil.WriteAPIError(w, http.StatusUnauthorized, "Token 无效或已过期")
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	id, ok := httputil.ParsePathID(w, chi.URLParam(r, "emoji_id"))
	if !ok {
		return
	}
	item, err := service.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, emojisystem.ErrEmojiNotFound) {
			httputil.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表情包", id))
			return
		}
		httputil.WriteAPIError(w, http.StatusInternalServerError, "获取表情包缩略图失败: "+err.Error())
		return
	}
	fileName := filepath.Base(item.FullPath)
	sourcePath, err := service.ResolveStickerPath(fileName)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusNotFound, "表情包文件不存在")
		return
	}
	original, _, boolErr := optionalQueryBool(r, "original")
	if boolErr != nil {
		writeRequestValidationError(w, boolErr.Error())
		return
	}
	if original {
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(sourcePath)), ".")
		w.Header().Set("Content-Type", emojisystem.MimeTypeByFormat(format))
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", item.Hash+"."+format))
		http.ServeFile(w, r, sourcePath)
		return
	}
	cachePath := emojisystem.ThumbnailCachePath(item.Hash)
	if info, err := os.Stat(cachePath); err == nil && !info.IsDir() {
		w.Header().Set("Content-Type", emojisystem.ThumbnailContentType())
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", item.Hash+"_thumb.webp"))
		http.ServeFile(w, r, cachePath)
		return
	}
	emojisystem.StartBackgroundThumbnail(sourcePath, item.Hash)
	w.Header().Set("Retry-After", "1")
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"status":   "generating",
		"message":  "缩略图正在生成中，请稍后重试",
		"emoji_id": id,
	})
}

// emojiThumbnailAuthorized 缩略图允许 Cookie 会话或 token 查询参数
func (d Deps) emojiThumbnailAuthorized(r *http.Request) bool {
	if d.authenticated(r) {
		return true
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	return token != "" && d.validSessionToken(token)
}

func (d Deps) handleEmojiBatchDelete(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/batch/delete"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	var body struct {
		EmojiIDs []uint `json:"emoji_ids"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if len(body.EmojiIDs) == 0 {
		httputil.WriteAPIError(w, http.StatusBadRequest, "未提供要删除的表情包ID")
		return
	}
	deleted, failedIDs := service.DeleteBatch(r.Context(), body.EmojiIDs)
	if failedIDs == nil {
		failedIDs = []uint{}
	}
	message := fmt.Sprintf("成功删除 %d 个表情包", deleted)
	if len(failedIDs) > 0 {
		message += fmt.Sprintf("，%d 个失败", len(failedIDs))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       message,
		"deleted_count": deleted,
		"failed_count":  len(failedIDs),
		"failed_ids":    failedIDs,
	})
}

func (d Deps) handleEmojiStats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/stats/summary"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	stats, err := service.Stats(r.Context())
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "获取统计数据失败: "+err.Error())
		return
	}
	formats := stats.Formats
	if formats == nil {
		formats = map[string]int{}
	}
	topUsed := stats.TopUsed
	if topUsed == nil {
		topUsed = []emojisystem.WebUIEmojiUsage{}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"total":              stats.Total,
			"recent_count":       stats.RecentCount,
			"collecting_enabled": d.stealEmojiEnabled(),
			"registered":         stats.Registered,
			"banned":             stats.Banned,
			"unregistered":       stats.Total - stats.Registered,
			"known":              stats.Known,
			"unknown":            stats.Unknown,
			"adopted":            stats.Registered,
			"discarded":          stats.Banned,
			"formats":            formats,
			"top_used":           topUsed,
		},
	})
}

func (d Deps) handleEmojiThumbnailCacheStats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/thumbnail-cache/stats"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	stats, err := service.Stats(r.Context())
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "获取统计失败: "+err.Error())
		return
	}
	dir, count, sizeMB, coverage, err := emojisystem.ThumbnailCacheStats(stats.Total)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "获取统计失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"cache_dir":        dir,
		"total_count":      count,
		"total_size_mb":    sizeMB,
		"emoji_count":      stats.Total,
		"coverage_percent": coverage,
	})
}

func (d Deps) handleEmojiThumbnailCleanup(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/thumbnail-cache/cleanup"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	cleaned, kept, err := service.CleanupOrphanedThumbnails(r.Context())
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "清理失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       fmt.Sprintf("清理完成：删除 %d 个孤立缓存，保留 %d 个有效缓存", cleaned, kept),
		"cleaned_count": cleaned,
		"kept_count":    kept,
	})
}

func (d Deps) handleEmojiThumbnailClear(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	cleaned, err := emojisystem.ClearThumbnailCache()
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "清空失败: "+err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       fmt.Sprintf("已清空所有缩略图缓存：删除 %d 个文件", cleaned),
		"cleaned_count": cleaned,
		"kept_count":    0,
	})
}

func (d Deps) handleEmojiThumbnailPreheat(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/emoji/thumbnail-cache/preheat"
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireEmojiService(w, path, "表情包服务未初始化")
	if !ok {
		return
	}
	limit, hasLimit, err := parseQueryIntStrict(r, "limit")
	if err != nil {
		writeValidationError(w, err.Error())
		return
	}
	if !hasLimit {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		writeValidationError(w, "limit 必须在 1 到 1000 之间")
		return
	}
	candidates, err := service.PreheatingCandidates(r.Context(), limit*2)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "预热失败: "+err.Error())
		return
	}
	generated, skipped, failed := 0, 0, 0
	for _, row := range candidates {
		if generated >= limit {
			break
		}
		cachePath := emojisystem.ThumbnailCachePath(row.ImageHash)
		if info, err := os.Stat(cachePath); err == nil && !info.IsDir() {
			skipped++
			continue
		}
		sourcePath, err := service.ResolveStickerPath(row.FullPath)
		if err != nil {
			failed++
			continue
		}
		if _, err := emojisystem.GenerateThumbnail(sourcePath, row.ImageHash); err != nil {
			failed++
			continue
		}
		generated++
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"message":         fmt.Sprintf("预热完成：生成 %d 个，跳过 %d 个已缓存，失败 %d 个", generated, skipped, failed),
		"generated_count": generated,
		"skipped_count":   skipped,
		"failed_count":    failed,
	})
}

// detectUploadMime 优先使用 multipart 声明类型，缺失时按内容嗅探
func detectUploadMime(declared string, content []byte) string {
	if mediaType, _, err := mime.ParseMediaType(declared); err == nil && mediaType != "" {
		return mediaType
	}
	return http.DetectContentType(content)
}

// emojiRegistry 返回全局表情包注册表
func emojiRegistry() *emojisystem.Registry {
	return emojisystem.DefaultRegistry()
}
