package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	avatarTTL      = 600 * time.Second
	maxAvatarBytes = 5 * 1024 * 1024
	maxRelayBytes  = 20 * 1024 * 1024
)

func (a *Adapter) avatar(ctx context.Context, _, _ string, args map[string]any) (any, error) {
	if args["platform"] != PlatformTelegram {
		return map[string]any{"status": "unsupported"}, nil
	}
	kind := "user"
	if value, exists := args["target_type"]; exists {
		kind, _ = value.(string)
	}
	if kind != "user" && kind != "group" {
		return nil, fmt.Errorf("target_type 必须为 user 或 group")
	}
	account, _ := args["account_id"].(string)
	scope, _ := args["scope"].(string)
	if scope != "" || (account != "" && account != a.runtime.AccountID()) {
		return nil, fmt.Errorf("头像请求的路由身份与 Telegram 网关不一致")
	}
	target, _ := args["target_id"].(string)
	target = strings.TrimSpace(target)
	var chatID int64
	var err error
	if kind == "group" {
		chatID, _, err = ParseGroupID(target)
		if err != nil || chatID >= 0 {
			return nil, fmt.Errorf("Telegram 群 ID 必须为负整数")
		}
	} else {
		chatID, err = strconv.ParseInt(target, 10, 64)
		if err != nil || chatID <= 0 {
			return nil, fmt.Errorf("Telegram 用户 ID 必须为正整数")
		}
	}
	a.avatarMu.Lock()
	defer a.avatarMu.Unlock()
	a.mu.Lock()
	client, ready := a.client, a.runtime.Ready()
	a.mu.Unlock()
	if !ready || client == nil {
		return nil, fmt.Errorf("Telegram 网关尚未就绪")
	}
	fileID, err := avatarFileID(ctx, client, chatID, kind)
	if err != nil {
		return nil, err
	}
	if fileID == "" {
		return map[string]any{"status": "missing"}, nil
	}
	binary, err := client.Download(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if len(binary) == 0 || len(binary) > maxAvatarBytes {
		return nil, fmt.Errorf("头像必须非空且不能超过 5 MB")
	}
	_, format, err := image.Decode(bytes.NewReader(binary))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") {
		return nil, fmt.Errorf("Telegram 头像必须为 JPEG、PNG 或 WEBP")
	}
	address, err := a.avatars.publish(binary, "image/"+format)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "available", "url": address, "expires_in": int(avatarTTL.Seconds())}, nil
}

func avatarFileID(ctx context.Context, client *Client, chatID int64, kind string) (string, error) {
	if kind == "group" {
		response, err := client.Call(ctx, "getChat", map[string]any{"chat_id": chatID}, nil)
		if err != nil {
			return "", err
		}
		chat, _ := asMap(response)
		if chat["type"] != "group" && chat["type"] != "supergroup" {
			return "", fmt.Errorf("目标不是 Telegram 群聊")
		}
		photo, _ := asMap(chat["photo"])
		return asString(photo["big_file_id"]), nil
	}
	response, err := client.Call(ctx, "getUserProfilePhotos", map[string]any{"user_id": chatID, "limit": 1}, nil)
	if err != nil {
		return "", err
	}
	profile, _ := asMap(response)
	photos, ok := profile["photos"].([]any)
	if !ok {
		return "", fmt.Errorf("Telegram 用户头像响应缺少 photos")
	}
	if len(photos) == 0 {
		return "", nil
	}
	var largest int64 = -1
	var fileID string
	for _, photo := range entityList(photos[0]) {
		width, _ := asInt64(photo["width"])
		height, _ := asInt64(photo["height"])
		if area := width * height; area > largest {
			largest, fileID = area, asString(photo["file_id"])
		}
	}
	if fileID == "" {
		return "", fmt.Errorf("Telegram 用户头像响应缺少 file_id")
	}
	return fileID, nil
}

type avatarRelay struct {
	mu     sync.Mutex
	server *http.Server
	addr   string
	images map[string]relayImage
}

type relayImage struct {
	binary  []byte
	mime    string
	expires time.Time
}

func (r *avatarRelay) publish(binary []byte, mime string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server == nil {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /avatar/{key}", r.serve)
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
		r.server, r.addr, r.images = server, listener.Addr().String(), make(map[string]relayImage)
		go func() { _ = server.Serve(listener) }()
	}
	now := time.Now()
	total := 0
	for key, entry := range r.images {
		if !now.Before(entry.expires) {
			delete(r.images, key)
		} else {
			total += len(entry.binary)
		}
	}
	for total+len(binary) > maxRelayBytes {
		var oldest string
		var expiry time.Time
		for key, entry := range r.images {
			if expiry.IsZero() || entry.expires.Before(expiry) {
				oldest, expiry = key, entry.expires
			}
		}
		total -= len(r.images[oldest].binary)
		delete(r.images, oldest)
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(token[:])
	r.images[key] = relayImage{binary, mime, now.Add(avatarTTL)}
	return "http://" + r.addr + "/avatar/" + key, nil
}

func (r *avatarRelay) serve(w http.ResponseWriter, request *http.Request) {
	key := request.PathValue("key")
	r.mu.Lock()
	entry, ok := r.images[key]
	if ok && !time.Now().Before(entry.expires) {
		delete(r.images, key)
		ok = false
	}
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, request)
		return
	}
	w.Header().Set("Content-Type", entry.mime)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(entry.binary)
}

func (r *avatarRelay) close() {
	r.mu.Lock()
	server := r.server
	r.server, r.addr, r.images = nil, "", nil
	r.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}
