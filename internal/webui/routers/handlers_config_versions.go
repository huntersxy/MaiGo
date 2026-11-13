package webuiapi

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/bytedance/sonic"
)

type modelVersionCreateRequest struct {
	Label string `json:"label"`
}

type modelVersionUpdateRequest struct {
	Label string `json:"label"`
}

type modelVersionSwitchRequest struct {
	ArchiveCurrent bool   `json:"archive_current"`
	ArchiveLabel   string `json:"archive_label"`
}

// handleConfigModelVersionsList 对应 GET /api/webui/config/model/versions
func (d Deps) handleConfigModelVersionsList(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.configDir()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	activePath, _ := d.modelConfigPath()
	if _, err := os.Stat(activePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "当前模型配置文件不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	manifest, err := readModelVersionManifest(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	activeInfo, err := buildModelVersionInfo("active", manifest.ActiveLabel, activePath, nil, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	versions, err := listModelVersions(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active_version": activeInfo, "versions": versions})
}

// handleConfigModelVersionCreate 对应 POST /api/webui/config/model/versions
func (d Deps) handleConfigModelVersionCreate(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.configDir()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	var request modelVersionCreateRequest
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	activePath, _ := d.modelConfigPath()
	info, err := copyModelConfigToVersion(dir, activePath, request.Label)
	if err != nil {
		writeModelVersionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": info, "message": "模型配置副本已创建"})
}

// handleConfigModelVersionUpdate 对应 PATCH /api/webui/config/model/versions/{version_id}
func (d Deps) handleConfigModelVersionUpdate(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.configDir()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	versionID, err := safeModelVersionID(chiURLParam(r, "version_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	versionPath, err := modelVersionPath(dir, versionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := os.Stat(versionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "模型配置副本不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	var request modelVersionUpdateRequest
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	manifest, err := readModelVersionManifest(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	manifest.upsert(versionID, strings.TrimSpace(request.Label), 0)
	if err := writeModelVersionManifest(dir, manifest); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	label := strings.TrimSpace(request.Label)
	if label == "" {
		label = defaultModelVersionLabel()
	}
	info, err := buildModelVersionInfo(versionID, label, versionPath, nil, false)
	if err != nil {
		writeModelVersionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": info, "message": "模型配置副本已更新"})
}

// handleConfigModelVersionDelete 对应 DELETE /api/webui/config/model/versions/{version_id}
func (d Deps) handleConfigModelVersionDelete(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.configDir()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	versionID, err := safeModelVersionID(chiURLParam(r, "version_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	versionPath, err := modelVersionPath(dir, versionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := os.Stat(versionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "模型配置副本不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.Remove(versionPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	manifest, err := readModelVersionManifest(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	manifest.remove(versionID)
	if err := writeModelVersionManifest(dir, manifest); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "模型配置副本已删除"})
}

// handleConfigModelVersionActivate 对应 POST /api/webui/config/model/versions/{version_id}/activate
func (d Deps) handleConfigModelVersionActivate(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.configDir()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	versionID, err := safeModelVersionID(chiURLParam(r, "version_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	var request modelVersionSwitchRequest
	// archive_current 允许为空请求体
	if raw := readBody(r); len(strings.TrimSpace(string(raw))) > 0 {
		if err := sonic.Unmarshal(raw, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
			return
		}
	}
	activePath, _ := d.modelConfigPath()
	versionPath, err := modelVersionPath(dir, versionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := os.Stat(versionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "模型配置副本不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := os.Stat(activePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "当前模型配置文件不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	manifest, err := readModelVersionManifest(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	selectedLabel := versionID
	if meta, ok := manifest.metadataByID()[versionID]; ok {
		if label, ok := meta["label"].(string); ok && strings.TrimSpace(label) != "" {
			selectedLabel = strings.TrimSpace(label)
		}
	}
	if err := validateModelConfigFile(versionPath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if request.ArchiveCurrent {
		archiveLabel := strings.TrimSpace(request.ArchiveLabel)
		if archiveLabel == "" {
			archiveLabel = manifest.ActiveLabel
		}
		if _, err := copyModelConfigToVersion(dir, activePath, archiveLabel); err != nil {
			writeModelVersionError(w, err)
			return
		}
	}
	raw, err := os.ReadFile(versionPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := writeFileAtomic(activePath, raw, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.Remove(versionPath); err != nil {
		// Python 仅 warning，不阻断切换
		_ = err
	}
	manifest, err = readModelVersionManifest(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	manifest.remove(versionID)
	manifest.ActiveLabel = selectedLabel
	if err := writeModelVersionManifest(dir, manifest); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	info, err := buildModelVersionInfo("active", manifest.ActiveLabel, activePath, nil, true)
	if err != nil {
		writeModelVersionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": info, "message": "模型配置副本已切换"})
}

func writeModelVersionError(w http.ResponseWriter, err error) {
	message := err.Error()
	switch {
	case strings.Contains(message, "不存在"):
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": message})
	case strings.Contains(message, "无效"):
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": message})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": message})
	}
}
