package webuiapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/host"
)

// handleConfigBotGet 对应 GET /api/webui/config/bot
func (d Deps) handleConfigBotGet(w http.ResponseWriter, r *http.Request) {
	path, ok := d.botConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	doc, err := readTOMLDocument(path)
	if err != nil {
		writeConfigReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": doc})
}

// handleConfigModelGet 对应 GET /api/webui/config/model
func (d Deps) handleConfigModelGet(w http.ResponseWriter, r *http.Request) {
	path, ok := d.modelConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	doc, err := readTOMLDocument(path)
	if err != nil {
		writeConfigReadError(w, err)
		return
	}
	if catalogPath := maibotcfg.SharedCatalogPath(); catalogPath != "" {
		if merged, mergeErr := maibotcfg.MergeSharedCatalogDoc(doc); mergeErr == nil {
			doc = merged
		}
		doc["_shared_catalog"] = map[string]any{"revision": maibotcfg.CatalogRevision(catalogPath)}
	}
	doc["_warnings"] = modelConfigWarnings(doc)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": doc})
}

// handleConfigBotUpdate 对应 POST /api/webui/config/bot
func (d Deps) handleConfigBotUpdate(w http.ResponseWriter, r *http.Request) {
	path, ok := d.botConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	body, err := decodeConfigBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	if maibotcfg.SharedSettingsPath() != "" {
		maibotcfg.StripSharedSettingsDoc(body)
	}
	if err := validateBotConfigMap(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
		return
	}
	if err := writeTOMLDocument(path, body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存配置文件失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置已保存"})
}

// handleConfigModelUpdate 对应 POST /api/webui/config/model
func (d Deps) handleConfigModelUpdate(w http.ResponseWriter, r *http.Request) {
	path, ok := d.modelConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	body, err := decodeConfigBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	delete(body, "_warnings")
	if err := validateModelConfigMap(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
		return
	}
	if catalogPath := maibotcfg.SharedCatalogPath(); catalogPath != "" {
		revision := strings.TrimSpace(r.URL.Query().Get("catalog_revision"))
		if shared, ok := body["_shared_catalog"].(map[string]any); ok && revision == "" {
			revision = strings.TrimSpace(stringValue(shared["revision"]))
		}
		if _, err := host.SaveEffectiveConfig(catalogPath, path, body, revision); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置已保存",
			"catalog_revision": maibotcfg.CatalogRevision(catalogPath), "warnings": modelConfigWarnings(body)})
		return
	}
	if err := writeTOMLDocument(path, body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存配置文件失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置已保存", "warnings": modelConfigWarnings(body)})
}

// handleConfigBotRawGet 对应 GET /api/webui/config/bot/raw 与 GET /api/config/raw
func (d Deps) handleConfigBotRawGet(w http.ResponseWriter, r *http.Request) {
	path, ok := d.botConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		writeConfigReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "content": string(raw)})
}

type rawConfigBody struct {
	RawContent *string `json:"raw_content"`
	Content    *string `json:"content"`
}

// handleConfigBotRawUpdate 对应 POST /api/webui/config/bot/raw 与 POST /api/config/raw
func (d Deps) handleConfigBotRawUpdate(w http.ResponseWriter, r *http.Request) {
	path, ok := d.botConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "读取请求体失败: " + err.Error()})
		return
	}
	var body rawConfigBody
	text := string(raw)
	if err := jsonUnmarshalStrict(raw, &body); err == nil {
		if body.RawContent != nil {
			text = *body.RawContent
		} else if body.Content != nil {
			text = *body.Content
		}
	}
	if strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置内容不能为空"})
		return
	}
	var doc map[string]any
	if err := toml.Unmarshal([]byte(text), &doc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "TOML 格式错误: " + err.Error()})
		return
	}
	// 空文档或缺 inner.version 的内容会直接废掉实例配置（加载器会拒绝），这里提前拒绝
	if inner, ok := doc["inner"].(map[string]any); !ok || strings.TrimSpace(fmt.Sprint(inner["version"])) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置缺少 [inner] version，拒绝保存"})
		return
	}
	if err := validateBotConfigMap(doc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
		return
	}
	if maibotcfg.SharedSettingsPath() != "" {
		maibotcfg.StripSharedSettingsDoc(doc)
		if encoded, err := toml.Marshal(doc); err == nil {
			text = string(encoded)
		}
	}
	if err := writeFileAtomic(path, []byte(text), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存配置文件失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置已保存"})
}

// handleConfigBotSectionUpdate 对应 POST /api/webui/config/bot/section/{section_name}
func (d Deps) handleConfigBotSectionUpdate(w http.ResponseWriter, r *http.Request) {
	path, ok := d.botConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	section := strings.TrimSpace(chiURLParam(r, "section_name"))
	doc, err := readTOMLDocument(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置文件不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "更新配置节失败: " + err.Error()})
		return
	}
	if _, exists := doc[section]; !exists {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置节 '" + section + "' 不存在"})
		return
	}
	patch, err := decodeAnyBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	applySectionPatch(doc, section, patch)
	if err := validateBotConfigMap(doc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
		return
	}
	if maibotcfg.SharedSettingsPath() != "" {
		maibotcfg.StripSharedSettingsDoc(doc)
	}
	if err := writeTOMLDocument(path, doc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "更新配置节失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置节 '" + section + "' 已保存"})
}

// handleConfigModelSectionUpdate 对应 POST /api/webui/config/model/section/{section_name}
func (d Deps) handleConfigModelSectionUpdate(w http.ResponseWriter, r *http.Request) {
	path, ok := d.modelConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	section := strings.TrimSpace(chiURLParam(r, "section_name"))
	doc, err := readTOMLDocument(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置文件不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "更新配置节失败: " + err.Error()})
		return
	}
	catalogPath := maibotcfg.SharedCatalogPath()
	catalogRevision := ""
	if catalogPath != "" {
		if merged, mergeErr := maibotcfg.MergeSharedCatalogDoc(doc); mergeErr == nil {
			doc = merged
		}
		catalogRevision = maibotcfg.CatalogRevision(catalogPath)
	}
	patch, err := decodeAnyBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	if section == "web" {
		webSection, ok := patch.(map[string]any)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "联网配置必须是对象"})
			return
		}
		doc["web"] = webSection
	} else {
		if _, exists := doc[section]; !exists {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置节 '" + section + "' 不存在"})
			return
		}
		applySectionPatch(doc, section, patch)
	}
	if err := validateModelConfigMap(doc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
		return
	}
	if catalogPath != "" {
		if requested := strings.TrimSpace(r.URL.Query().Get("catalog_revision")); requested != "" {
			catalogRevision = requested
		}
		if _, err := host.SaveEffectiveConfig(catalogPath, path, doc, catalogRevision); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "配置数据验证失败: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置节 '" + section + "' 已保存", "warnings": modelConfigWarnings(doc)})
		return
	}
	if err := writeTOMLDocument(path, doc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "更新配置节失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置节 '" + section + "' 已保存", "warnings": modelConfigWarnings(doc)})
}

// applySectionPatch 对齐 Python：列表/标量直接替换，字典递归合并
func applySectionPatch(doc map[string]any, section string, patch any) {
	patchMap, isMap := patch.(map[string]any)
	existingMap, existingIsMap := doc[section].(map[string]any)
	if isMap && existingIsMap {
		deepMergeConfig(existingMap, patchMap)
		return
	}
	doc[section] = patch
}
