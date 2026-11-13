package webuiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type adapterPathRequest struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func (d Deps) adapterProjectRoot() (string, bool) {
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return "", false
	}
	root, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", false
	}
	return root, true
}

func (d Deps) allowedAdapterRoots() []string {
	root, ok := d.adapterProjectRoot()
	if !ok {
		return nil
	}
	return []string{
		root,
		filepath.Join(filepath.Dir(root), "MaiBot-Napcat-Adapter"),
		filepath.FromSlash("/MaiMBot/adapters-config"),
	}
}

func (d Deps) webuiDataPath() (string, bool) {
	root, ok := d.adapterProjectRoot()
	if !ok {
		return "", false
	}
	return filepath.Join(root, "data", "webui.json"), true
}

func (d Deps) resolveAdapterConfigPath(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return "", errors.New("路径不能为空")
	}
	if !filepath.IsAbs(normalized) {
		root, ok := d.adapterProjectRoot()
		if !ok {
			return "", errors.New("需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录")
		}
		normalized = filepath.Join(root, filepath.FromSlash(normalized))
	}
	candidate, err := filepath.Abs(filepath.Clean(normalized))
	if err != nil {
		return "", err
	}
	if strings.ToLower(filepath.Ext(candidate)) != ".toml" {
		return "", errors.New("只支持 .toml 格式的配置文件")
	}
	for _, allowedRoot := range d.allowedAdapterRoots() {
		absRoot, err := filepath.Abs(allowedRoot)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(absRoot, candidate); err == nil && !strings.HasPrefix(rel, "..") {
			return candidate, nil
		}
	}
	return "", errors.New("适配器配置路径超出允许范围")
}

func (d Deps) toRelativeAdapterPath(path string) string {
	root, ok := d.adapterProjectRoot()
	if !ok || !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}

func readWebUIData(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return map[string]any{}
	}
	return data
}

// handleConfigAdapterPathGet 对应 GET /api/webui/config/adapter-config/path
func (d Deps) handleConfigAdapterPathGet(w http.ResponseWriter, r *http.Request) {
	webuiPath, ok := d.webuiDataPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	data := readWebUIData(webuiPath)
	stored, _ := data["adapter_config_path"].(string)
	if strings.TrimSpace(stored) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "path": nil})
		return
	}
	absPath, err := d.resolveAdapterConfigPath(stored)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "path": nil})
		return
	}
	if stat, err := os.Stat(absPath); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "path": d.toRelativeAdapterPath(absPath),
			"lastModified": stat.ModTime().Format("2006-01-02T15:04:05.999999"),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "path": stored, "lastModified": nil})
}

// handleConfigAdapterPathSave 对应 POST /api/webui/config/adapter-config/path
func (d Deps) handleConfigAdapterPathSave(w http.ResponseWriter, r *http.Request) {
	webuiPath, ok := d.webuiDataPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	var request adapterPathRequest
	if err := json.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	absPath, err := d.resolveAdapterConfigPath(request.Path)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "INSTANCE_DIR") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	data := readWebUIData(webuiPath)
	data["adapter_config_path"] = d.toRelativeAdapterPath(absPath)
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存路径失败: " + err.Error()})
		return
	}
	if err := writeFileAtomic(webuiPath, append(raw, '\n'), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存路径失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "路径已保存"})
}

// handleConfigAdapterGet 对应 GET /api/webui/config/adapter-config?path=...
func (d Deps) handleConfigAdapterGet(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "路径参数不能为空"})
		return
	}
	absPath, err := d.resolveAdapterConfigPath(raw)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "INSTANCE_DIR") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置文件不存在: " + raw})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取配置失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "content": string(content)})
}

// handleConfigAdapterSave 对应 POST /api/webui/config/adapter-config
func (d Deps) handleConfigAdapterSave(w http.ResponseWriter, r *http.Request) {
	var request adapterPathRequest
	if err := json.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	if strings.TrimSpace(request.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "路径不能为空"})
		return
	}
	if request.Content == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置内容不能为空"})
		return
	}
	absPath, err := d.resolveAdapterConfigPath(request.Path)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "INSTANCE_DIR") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	var doc map[string]any
	if err := toml.Unmarshal([]byte(*request.Content), &doc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "TOML 格式错误: " + err.Error()})
		return
	}
	if err := writeFileAtomic(absPath, []byte(*request.Content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存配置失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置已保存"})
}
