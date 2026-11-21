package webuiapi

// 远端桌宠业务入口，对齐 webui/routers/desktop_pet_remote.py。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/desktop_pet"
)

const (
	remoteDesktopPetMaxRequestBytes = 48 << 20
	remoteDesktopPetHeartbeat       = 60 * time.Second
	remoteDesktopPetHandshake       = 10 * time.Second
	remoteRecentRequestTTL          = 30 * time.Minute
	remoteRecentRequestLimit        = 256
)

var (
	remoteDesktopPetConnections sync.Map // connection_id -> *desktoppet.RemotePeer
	remoteRecentMu              sync.Mutex
	remoteRecent                = map[string]*remoteRecentEntry{}
)

type remoteRecentEntry struct {
	digest  string
	done    chan struct{}
	result  any
	err     error
	created time.Time
}

func (d Deps) remoteDesktopPet() (*desktoppet.Service, error) {
	service := desktoppet.DefaultService()
	if service == nil {
		return nil, errors.New("桌宠服务未初始化")
	}
	return service, nil
}

func remotePeerByRequest(r *http.Request) (*desktoppet.RemotePeer, error) {
	connectionID := strings.TrimSpace(r.Header.Get("x-desktop-pet-connection"))
	if connectionID == "" {
		return nil, errors.New("桌宠连接已失效，请先连接远端桌宠")
	}
	value, ok := remoteDesktopPetConnections.Load(connectionID)
	if !ok {
		return nil, errors.New("桌宠连接已失效，请先连接远端桌宠")
	}
	peer, _ := value.(*desktoppet.RemotePeer)
	if peer == nil {
		return nil, errors.New("桌宠连接已失效，请先连接远端桌宠")
	}
	return peer, nil
}

func readRemoteBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	if r.ContentLength > remoteDesktopPetMaxRequestBytes {
		return nil, &remoteBodyError{status: http.StatusRequestEntityTooLarge, message: "桌宠请求不能超过 48 MiB"}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, remoteDesktopPetMaxRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > remoteDesktopPetMaxRequestBytes {
		return nil, &remoteBodyError{status: http.StatusRequestEntityTooLarge, message: "桌宠请求不能超过 48 MiB"}
	}
	var payload map[string]any
	if err := sonic.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, &remoteBodyError{status: http.StatusUnprocessableEntity, message: "请求必须是 JSON 对象"}
	}
	return payload, nil
}

type remoteBodyError struct {
	status  int
	message string
}

func (e *remoteBodyError) Error() string { return e.message }

func remoteRequestDigest(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func remoteRecentExecute(clientID, domain, requestID string, payload map[string]any, operation func() (any, error)) (any, error) {
	digest := remoteRequestDigest(payload)
	key := clientID + "\x00" + domain + "\x00" + requestID
	now := time.Now()
	remoteRecentMu.Lock()
	for entryKey, entry := range remoteRecent {
		select {
		case <-entry.done:
			if entry.created.Add(remoteRecentRequestTTL).Before(now) {
				delete(remoteRecent, entryKey)
			}
		default:
		}
	}
	if entry := remoteRecent[key]; entry != nil {
		if entry.digest != digest {
			remoteRecentMu.Unlock()
			return nil, errors.New("同一个请求 ID 不能用于不同内容")
		}
		remoteRecentMu.Unlock()
		<-entry.done
		return entry.result, entry.err
	}
	if len(remoteRecent) >= remoteRecentRequestLimit {
		remoteRecentMu.Unlock()
		return nil, errors.New("近期桌宠请求过多，请稍后重试")
	}
	entry := &remoteRecentEntry{digest: digest, done: make(chan struct{}), created: now}
	remoteRecent[key] = entry
	remoteRecentMu.Unlock()
	entry.result, entry.err = operation()
	close(entry.done)
	return entry.result, entry.err
}

// handleDesktopPetRemoteWS 接收远端桌宠 WebSocket
func (d Deps) handleDesktopPetRemoteWS(w http.ResponseWriter, r *http.Request) {
	service, err := d.remoteDesktopPet()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(24 << 20)
	var peer *desktoppet.RemotePeer
	defer func() {
		if peer != nil {
			remoteDesktopPetConnections.Delete(peer.ConnectionID())
			peer.MarkDisconnected()
			service.DetachRemote(peer)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()
	hello, err := readRemoteEvent(conn, remoteDesktopPetHandshake)
	if err != nil {
		_ = writeRemoteError(conn, err)
		return
	}
	clientID, capabilities, err := desktoppet.ValidateRemoteHello(hello)
	if err != nil {
		_ = writeRemoteError(conn, err)
		return
	}
	peer = desktoppet.NewRemotePeer(conn, clientID, capabilities)
	if err := service.AttachRemote(peer); err != nil {
		_ = peer.Send(r.Context(), map[string]any{"type": "error", "error": err.Error()})
		peer.Close("远端桌宠连接结束")
		return
	}
	remoteDesktopPetConnections.Store(peer.ConnectionID(), peer)
	if err := peer.Send(r.Context(), map[string]any{
		"type": "hello", "version": desktoppet.RemoteProtocolVersion,
		"connection_id": peer.ConnectionID(), "heartbeat_interval": 20, "heartbeat_timeout": 60,
	}); err != nil {
		return
	}
	for {
		event, readErr := readRemoteEvent(conn, remoteDesktopPetHeartbeat)
		if readErr != nil {
			if !errors.Is(readErr, context.Canceled) {
				_ = peer.Send(context.Background(), map[string]any{"type": "error", "error": readErr.Error()})
			}
			return
		}
		kind, _ := event["type"].(string)
		switch kind {
		case "ping":
			if err := peer.Send(r.Context(), map[string]any{"type": "pong"}); err != nil {
				return
			}
			continue
		case "pong":
			continue
		case "hello", "chat":
			_ = peer.Send(r.Context(), map[string]any{"type": "error", "error": "重复握手或未通过 HTTP 上传的聊天请求"})
			return
		}
		if err := desktoppet.ValidateRemoteEvent(event); err != nil {
			_ = peer.Send(r.Context(), map[string]any{"type": "error", "error": err.Error()})
			return
		}
		if err := service.ReceiveRemoteEvent(r.Context(), peer, event); err != nil {
			_ = peer.Send(r.Context(), map[string]any{"type": "error", "error": err.Error()})
			return
		}
	}
}

func readRemoteEvent(conn *websocket.Conn, timeout time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if messageType != websocket.MessageText {
		return nil, errors.New("桌宠 WebSocket 仅接受 JSON 文本消息")
	}
	var event map[string]any
	if err := sonic.Unmarshal(data, &event); err != nil || event == nil {
		return nil, errors.New("请求必须是 JSON 对象")
	}
	if _, ok := event["type"].(string); !ok {
		return nil, errors.New("桌宠事件缺少 type")
	}
	return event, nil
}

func writeRemoteError(conn *websocket.Conn, err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	encoded, marshalErr := sonic.Marshal(map[string]any{"type": "error", "error": err.Error()})
	if marshalErr != nil {
		return marshalErr
	}
	if writeErr := conn.Write(ctx, websocket.MessageText, encoded); writeErr != nil {
		return writeErr
	}
	return conn.Close(websocket.StatusPolicyViolation, "远端桌宠连接结束")
}

// handleDesktopPetRemoteChat 接收远端图文消息
func (d Deps) handleDesktopPetRemoteChat(w http.ResponseWriter, r *http.Request) {
	service, err := d.remoteDesktopPet()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	peer, err := remotePeerByRequest(r)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	requestID := chi.URLParam(r, "request_id")
	if err := desktoppet.ValidateRemoteRequestID(requestID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	if err := peer.BeginRequest(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	defer peer.EndRequest()
	body, err := readRemoteBody(w, r)
	if err != nil {
		writeRemoteBodyFailure(w, err)
		return
	}
	payload, err := desktoppet.ValidateRemoteChatRequest(body)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	text, _ := payload["text"].(string)
	images := desktopPetChatImages(payload)
	result, err := remoteRecentExecute(peer.ClientID(), "chat", requestID, payload, func() (any, error) {
		messageID, err := service.RemoteChat(r.Context(), peer, text, images)
		if err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "request_id": requestID, "message_id": messageID}, nil
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func desktopPetChatImages(payload map[string]any) []desktoppet.ChatRequestImage {
	rawImages, _ := payload["images"].([]any)
	images := make([]desktoppet.ChatRequestImage, 0, len(rawImages))
	for _, raw := range rawImages {
		if item, ok := raw.(map[string]any); ok {
			images = append(images, desktoppet.ChatRequestImage{
				Base64: asRemoteString(item["base64"]), MimeType: asRemoteString(item["mime_type"]),
			})
		}
	}
	return images
}

func asRemoteString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// handleDesktopPetRemoteTodos 接收远端待办请求
func (d Deps) handleDesktopPetRemoteTodos(w http.ResponseWriter, r *http.Request) {
	service, err := d.remoteDesktopPet()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	peer, err := remotePeerByRequest(r)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	if err := peer.BeginRequest(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	defer peer.EndRequest()
	body, err := readRemoteBody(w, r)
	if err != nil {
		writeRemoteBodyFailure(w, err)
		return
	}
	action := asRemoteString(body["action"])
	switch action {
	case "list", "add", "update", "complete", "delete":
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "不支持的待办操作"})
		return
	}
	payload := make(map[string]any, len(body))
	for key, value := range body {
		if key != "action" && key != "request_id" {
			payload[key] = value
		}
	}
	if err := desktoppet.ValidateRemoteTodoPayload(action, payload); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	operation := func() (any, error) {
		return service.RemoteTodo(r.Context(), action, payload)
	}
	if action == "list" {
		result, err := operation()
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	requestID := asRemoteString(body["request_id"])
	if err := desktoppet.ValidateRemoteRequestID(requestID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	result, err := remoteRecentExecute(peer.ClientID(), "todo", requestID, map[string]any{"action": action, "payload": payload}, operation)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleDesktopPetRemoteTodoImage 读取远端待办图片
func (d Deps) handleDesktopPetRemoteTodoImage(w http.ResponseWriter, r *http.Request) {
	service, err := d.remoteDesktopPet()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	peer, err := remotePeerByRequest(r)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	if err := peer.BeginImageRequest(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	defer peer.EndImageRequest()
	result, err := service.RemoteTodo(r.Context(), "image", map[string]any{"image_id": chi.URLParam(r, "image_id")})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeRemoteBodyFailure(w http.ResponseWriter, err error) {
	var bodyErr *remoteBodyError
	if errors.As(err, &bodyErr) {
		writeJSON(w, bodyErr.status, map[string]any{"detail": bodyErr.message})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
}
