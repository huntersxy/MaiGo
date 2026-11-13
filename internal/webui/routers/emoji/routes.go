package emoji

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Mount 注册 /api/webui/emoji 全部表情包端点
//
// 路由表对齐 MaiBot src/webui/routers/emoji/routes.py。
func Mount(er chi.Router, d Deps) {
	er.Get("/list", d.handleEmojiList)
	er.Post("/upload", d.handleEmojiUpload)
	er.Post("/batch/upload", d.handleEmojiBatchUpload)
	er.Post("/batch/delete", d.handleEmojiBatchDelete)
	er.Get("/stats/summary", d.handleEmojiStats)
	er.Get("/thumbnail-cache/stats", d.handleEmojiThumbnailCacheStats)
	er.Post("/thumbnail-cache/cleanup", d.handleEmojiThumbnailCleanup)
	er.Post("/thumbnail-cache/preheat", d.handleEmojiThumbnailPreheat)
	er.Delete("/thumbnail-cache/clear", d.handleEmojiThumbnailClear)
	er.Get("/{emoji_id}", d.handleEmojiDetail)
	er.Patch("/{emoji_id}", d.handleEmojiUpdate)
	er.Delete("/{emoji_id}", d.handleEmojiDelete)
	er.Post("/{emoji_id}/register", d.handleEmojiRegister)
	er.Post("/{emoji_id}/ban", d.handleEmojiBan)
	er.Get("/{emoji_id}/thumbnail", d.handleEmojiThumbnail)
}

// MountUser 注册 /api/webui/user-emojis 用户表情路由
func MountUser(ur chi.Router, d Deps) {
	ur.Get("/", d.handleUserEmojisList)
	ur.Post("/", d.handleUserEmojisAdd)
	ur.Get("/{emoji_id}/content", d.handleUserEmojiContent)
	ur.Delete("/{emoji_id}", d.handleUserEmojiDelete)
}

// HandleUserEmojisList 处理 GET /user-emojis（无尾斜杠）
func (d Deps) HandleUserEmojisList(w http.ResponseWriter, r *http.Request) {
	d.handleUserEmojisList(w, r)
}

// HandleUserEmojisAdd 处理 POST /user-emojis（无尾斜杠）
func (d Deps) HandleUserEmojisAdd(w http.ResponseWriter, r *http.Request) {
	d.handleUserEmojisAdd(w, r)
}
