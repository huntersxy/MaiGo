package webuiapi

import (
	"context"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"MaiGo/internal/chat/image_system"
	voicesystem "MaiGo/internal/chat/voice_system"
	"MaiGo/internal/common/utils"
)

// monitorMediaStore 只读访问 images 元数据表；测试可注入替身
type monitorMediaStore interface {
	GetByHash(ctx context.Context, imageHash, imageType string) (*imagesystem.ImageRecord, error)
}

// resolveMonitorMediaStore 优先使用显式注入的存储，否则基于主程序 memory 的 images 表只读查询
func (d Deps) resolveMonitorMediaStore() monitorMediaStore {
	if d.mediaMetadata != nil {
		return d.mediaMetadata
	}
	if d.Memory == nil || d.Memory.GetDB() == nil {
		return nil
	}
	return imagesystem.NewGORMStore(d.Memory.GetDB())
}

// handleSystemMaisakaMonitorMedia 对齐 system.py 的 get_maisaka_monitor_media
func (d Deps) handleSystemMaisakaMonitorMedia(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	mediaKind := strings.TrimSpace(chi.URLParam(r, "media_kind"))
	mediaHash := strings.TrimSpace(chi.URLParam(r, "media_hash"))
	if mediaHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "媒体 hash 不能为空"})
		return
	}

	root := strings.TrimSpace(d.InstanceDir)
	if root == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "实例目录未配置，无法解析监控媒体"})
		return
	}

	if mediaKind == "voice" {
		filePath, err := voicesystem.VoicePathByHash(root, mediaHash)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到语音原件"})
			return
		}
		if rel, err := filepath.Rel(filepath.Join(root, "data", "voices"), filePath); err != nil || strings.HasPrefix(rel, "..") {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到语音原件"})
			return
		}
		info, err := os.Stat(filePath)
		if err != nil || !info.Mode().IsRegular() {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到语音原件"})
			return
		}
		file, err := os.Open(filePath)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到语音原件"})
			return
		}
		defer file.Close()
		header := make([]byte, 16)
		_, _ = file.Read(header)
		_, contentType := utils.DetectAudioFormat(header)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if _, err := file.Seek(0, 0); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取语音原件失败"})
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filePath)}))
		http.ServeContent(w, r, filepath.Base(filePath), info.ModTime(), file)
		return
	}

	var imageType string
	var label string
	switch mediaKind {
	case "image":
		imageType = imagesystem.ImageTypeImage
		label = "图片"
	case "emoji":
		imageType = imagesystem.ImageTypeEmoji
		label = "表情包"
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "媒体类型必须是 image、emoji 或 voice"})
		return
	}

	store := d.resolveMonitorMediaStore()
	if store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "图片元数据存储未初始化"})
		return
	}
	record, err := store.GetByHash(r.Context(), mediaHash, imageType)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "查询监控媒体失败: " + err.Error()})
		return
	}
	if record == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定" + label + "记录"})
		return
	}

	filePath, err := imagesystem.ResolveStoredImagePath(root, record.FullPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "无法解析指定" + label + "文件"})
		return
	}
	info, err := os.Stat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定" + label + "文件"})
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到指定" + label + "文件"})
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", monitorMediaContentType(filePath))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filePath)}))
	http.ServeContent(w, r, filepath.Base(filePath), info.ModTime(), file)
}

// monitorMediaContentType 按扩展名返回媒体类型，未知类型回退 octet-stream
func monitorMediaContentType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}