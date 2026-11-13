package webuiapi

import (
	"io"
	"net/http"
	"os"
	"strings"

	"maibot-go/internal/desktop_pet"

	"github.com/go-chi/chi/v5"
)

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
