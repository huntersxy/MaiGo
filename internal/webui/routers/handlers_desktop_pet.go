package webuiapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"MaiGo/internal/desktop_pet"
	"MaiGo/internal/services/schedules"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
)

// DesktopPetControl 桌宠窗口状态与控制端口
type DesktopPetControl interface {
	Status() desktoppet.Status
	Control(ctx context.Context, action, text string, active bool) (desktoppet.Status, error)
}

// desktopPetMaxBytes 皮肤包体积上限，对齐 skins.MAX_BYTES
const desktopPetMaxBytes = 32 * 1024 * 1024

func (d Deps) handleDesktopPetSkins(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	writeJSON(w, http.StatusOK, desktoppet.ListSkins(d.desktopPetRoot()))
}

func (d Deps) handleDesktopPetSkin(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	skinID := strings.TrimSpace(chi.URLParam(r, "skin_id"))
	skin, err := desktoppet.FindSkin(d.desktopPetRoot(), skinID, true)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, skin)
}

func (d Deps) handleDesktopPetInstall(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, desktopPetMaxBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "读取皮肤包失败"})
		return
	}
	if len(payload) > desktopPetMaxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "皮肤包不能超过 32 MiB"})
		return
	}
	skin, err := desktoppet.InstallSkinZip(d.desktopPetRoot(), payload)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, skin)
}

// desktopPetRoot 实例根目录，对齐 get_instance_root：优先 MAIBOT_INSTANCE_DIR，否则工作目录
func (d Deps) desktopPetRoot() string {
	if root := strings.TrimSpace(d.InstanceDir); root != "" {
		return root
	}
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// desktopPetTodoSummary 读取未完成待办，只读不写
func (d Deps) desktopPetTodoSummary() (map[string]any, error) {
	items, err := schedules.ReadScheduleItems(schedules.Directory(d.desktopPetRoot()), "", false)
	if err != nil {
		return nil, err
	}
	preview := make([]map[string]any, 0, len(items))
	for index, item := range items {
		if index >= 20 {
			break
		}
		preview = append(preview, map[string]any{
			"id": item["id"], "text": item["text"], "due_at": item["due_at"],
		})
	}
	return map[string]any{"pending": len(items), "items": preview}, nil
}

func (d Deps) handleDesktopPetStatus(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	status := desktoppet.Status{}
	if d.DesktopPet != nil {
		status = d.DesktopPet.Status()
	}
	todos, err := d.desktopPetTodoSummary()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": status.Enabled, "supported": status.Supported, "running": status.Running,
		"ready": status.Ready, "visible": status.Visible, "error": status.Error,
		"skin_id": status.SkinID, "pid": status.PID, "thinking": status.Thinking, "bubble": status.Bubble,
		"todos": todos,
	})
}

func (d Deps) handleDesktopPetVisibility(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	var body struct {
		Visible bool `json:"visible"`
	}
	raw, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if len(raw) == 0 || sonic.Unmarshal(raw, &body) != nil {
		writeValidationError(w, "visible 必须是布尔值")
		return
	}
	if d.DesktopPet == nil || !d.DesktopPet.Status().Enabled {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "桌宠没有开启"})
		return
	}
	if !d.DesktopPet.Status().Ready {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "桌宠窗口还没有就绪"})
		return
	}
	action := "hide"
	if body.Visible {
		action = "show"
	}
	status, err := d.DesktopPet.Control(r.Context(), action, "", false)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (d Deps) handleDesktopPetChat(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	raw, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if len(raw) == 0 || sonic.Unmarshal(raw, &body) != nil {
		writeValidationError(w, "消息不能为空")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeValidationError(w, "消息不能为空")
		return
	}
	if utf8.RuneCountInString(text) > 2000 {
		writeValidationError(w, "消息不能超过 2000 字")
		return
	}
	if d.DesktopPet == nil || !d.DesktopPet.Status().Enabled {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "桌宠没有开启"})
		return
	}
	if d.DesktopPetChat == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "桌宠聊天通道未接线"})
		return
	}
	messageID, err := d.DesktopPetChat(r.Context(), text)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message_id": messageID})
}
