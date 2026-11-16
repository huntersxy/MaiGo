package webuiapi

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"MaiGo/internal/prompt"

	"github.com/bytedance/sonic"
)

type promptUpdateRequest struct {
	Content       string  `json:"content"`
	VersionID     *string `json:"version_id"`
	Label         string  `json:"label"`
	CreateVersion bool    `json:"create_version"`
}

// handleConfigPromptsList 对应 GET /api/webui/config/prompts
func (d Deps) handleConfigPromptsList(w http.ResponseWriter, r *http.Request) {
	root := d.promptResource()
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "列出 Prompt 文件失败: " + err.Error()})
		return
	}
	languageDirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			languageDirs = append(languageDirs, entry.Name())
		}
	}
	sort.Strings(languageDirs)
	customRoot, _ := d.customPromptsRoot()
	languages := make([]string, 0, len(languageDirs))
	files := make(map[string]any, len(languageDirs))
	for _, language := range languageDirs {
		promptEntries, err := fs.ReadDir(root, language)
		if err != nil {
			continue
		}
		names := make([]string, 0)
		for _, entry := range promptEntries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".prompt") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		metadata := readPromptMetadata(root, language)
		infos := make([]any, 0, len(names))
		for _, name := range names {
			defaultPath := path.Join(language, name)
			customPath, err := safePromptPath(customRoot, language, name)
			if err != nil {
				continue
			}
			customized := pathExists(customPath)
			stat, err := fs.Stat(root, defaultPath)
			if err != nil {
				continue
			}
			defaultContent, err := fs.ReadFile(root, defaultPath)
			if err != nil {
				continue
			}
			effectiveContent := defaultContent
			if customized {
				if raw, err := os.ReadFile(customPath); err == nil {
					effectiveContent = raw
				}
			}
			versions, err := listPromptVersions(customRoot, language, name)
			if err != nil {
				versions = nil
			}
			meta := metadata[strings.TrimSuffix(name, ".prompt")]
			// 内嵌模板没有真实 mtime，零值时间转 epoch 会得到负数，按 0 上报
			modifiedAt := float64(stat.ModTime().UnixNano()) / 1e9
			if stat.ModTime().IsZero() {
				modifiedAt = 0
			}
			infos = append(infos, map[string]any{
				"name": name, "size": stat.Size(),
				"modified_at":  modifiedAt,
				"display_name": meta.DisplayName, "advanced": meta.Advanced, "description": meta.Description,
				"customized": customized, "custom_version_count": len(versions),
				"validation": buildPromptValidation(string(defaultContent), string(effectiveContent)),
			})
		}
		languages = append(languages, language)
		files[language] = infos
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "languages": languages, "files": files})
}

type promptFilePayload struct {
	path        string
	language    string
	filename    string
	customPath  string
	customRoot  string
	defaultText string
	effective   string
	customized  bool
}

func (d Deps) loadPromptFile(language, filename string) (*promptFilePayload, int, error) {
	if err := safePromptSegment(language, filename); err != nil {
		return nil, http.StatusBadRequest, err
	}
	customRoot, _ := d.customPromptsRoot()
	builtinFilename := prompt.CanonicalPromptName(strings.TrimSuffix(strings.TrimSpace(filename), ".prompt")) + ".prompt"
	resourcePath := path.Join(strings.TrimSpace(language), builtinFilename)
	defaultRaw, err := fs.ReadFile(d.promptResource(), resourcePath)
	if err != nil {
		return nil, http.StatusNotFound, errors.New("Prompt 文件不存在")
	}
	customPath, err := safePromptPath(customRoot, language, filename)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	effective := defaultRaw
	customized := false
	if pathExists(customPath) {
		raw, err := os.ReadFile(customPath)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		effective = raw
		customized = true
	}
	return &promptFilePayload{
		path: resourcePath, language: language, filename: filename,
		customPath: customPath, customRoot: customRoot,
		defaultText: string(defaultRaw), effective: string(effective), customized: customized,
	}, 0, nil
}

func (d Deps) promptFileResponse(payload *promptFilePayload, includeVersionID string) (map[string]any, error) {
	versions, err := listPromptVersions(payload.customRoot, payload.language, payload.filename)
	if err != nil {
		return nil, err
	}
	versionList := make([]any, 0, len(versions))
	for _, item := range versions {
		versionList = append(versionList, item)
	}
	active, err := activePromptVersionID(payload.customRoot, payload.language, payload.filename)
	if err != nil {
		return nil, err
	}
	response := map[string]any{
		"success": true, "language": payload.language, "filename": payload.filename,
		"content": payload.effective, "customized": payload.customized,
		"active_version_id": nullableString(active), "versions": versionList,
		"validation": buildPromptValidation(payload.defaultText, payload.effective),
	}
	if includeVersionID != "" {
		response["version_id"] = includeVersionID
	}
	return response, nil
}

// handleConfigPromptGet 对应 GET /api/webui/config/prompts/{language}/{filename}
func (d Deps) handleConfigPromptGet(w http.ResponseWriter, r *http.Request) {
	payload, status, err := d.loadPromptFile(chiURLParam(r, "language"), chiURLParam(r, "filename"))
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	response, err := d.promptFileResponse(payload, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取 Prompt 文件失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigPromptDefault 对应 GET .../default
func (d Deps) handleConfigPromptDefault(w http.ResponseWriter, r *http.Request) {
	payload, status, err := d.loadPromptFile(chiURLParam(r, "language"), chiURLParam(r, "filename"))
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	payload.effective = payload.defaultText
	payload.customized = false
	response, err := d.promptFileResponse(payload, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取默认 Prompt 文件失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigPromptVersions 对应 GET .../versions
func (d Deps) handleConfigPromptVersions(w http.ResponseWriter, r *http.Request) {
	language, filename := chiURLParam(r, "language"), chiURLParam(r, "filename")
	if err := safePromptSegment(language, filename); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	builtinFilename := prompt.CanonicalPromptName(strings.TrimSuffix(strings.TrimSpace(filename), ".prompt")) + ".prompt"
	if _, err := fs.Stat(d.promptResource(), path.Join(strings.TrimSpace(language), builtinFilename)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Prompt 文件不存在"})
		return
	}
	customRoot, _ := d.customPromptsRoot()
	versions, err := listPromptVersions(customRoot, language, filename)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	active, err := activePromptVersionID(customRoot, language, filename)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "language": language, "filename": filename,
		"active_version_id": nullableString(active), "versions": versions,
	})
}

func (d Deps) promptVersionContent(language, filename, versionID string) (string, string, int, error) {
	customRoot, _ := d.customPromptsRoot()
	normalized, err := safePromptVersionID(versionID)
	if err != nil {
		return "", "", http.StatusBadRequest, err
	}
	if normalized == legacyCustomPromptVersionID {
		customPath, err := safePromptPath(customRoot, language, filename)
		if err != nil {
			return "", "", http.StatusBadRequest, err
		}
		if !pathExists(customPath) {
			return "", "", http.StatusNotFound, errors.New("Prompt 自定义版本不存在")
		}
		raw, err := os.ReadFile(customPath)
		return string(raw), normalized, 0, err
	}
	versionPath, err := promptVersionFilePath(customRoot, language, filename, normalized)
	if err != nil {
		return "", "", http.StatusBadRequest, err
	}
	if !pathExists(versionPath) {
		return "", "", http.StatusNotFound, errors.New("Prompt 自定义版本不存在")
	}
	raw, err := os.ReadFile(versionPath)
	return string(raw), normalized, 0, err
}

// handleConfigPromptVersionGet 对应 GET .../versions/{version_id}
func (d Deps) handleConfigPromptVersionGet(w http.ResponseWriter, r *http.Request) {
	language, filename := chiURLParam(r, "language"), chiURLParam(r, "filename")
	payload, status, err := d.loadPromptFile(language, filename)
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	content, normalized, status, err := d.promptVersionContent(language, filename, chiURLParam(r, "version_id"))
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	payload.effective = content
	payload.customized = true
	response, err := d.promptFileResponse(payload, normalized)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取 Prompt 版本失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigPromptVersionActivate 对应 POST .../versions/{version_id}/activate
func (d Deps) handleConfigPromptVersionActivate(w http.ResponseWriter, r *http.Request) {
	language, filename := chiURLParam(r, "language"), chiURLParam(r, "filename")
	payload, status, err := d.loadPromptFile(language, filename)
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	content, normalized, status, err := d.promptVersionContent(language, filename, chiURLParam(r, "version_id"))
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := ensurePromptParametersMatch(d.promptResource(), payload.path, content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.MkdirAll(filepath.Dir(payload.customPath), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.WriteFile(payload.customPath, []byte(content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if normalized != legacyCustomPromptVersionID {
		if err := setActivePromptVersion(payload.customRoot, language, filename, normalized); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
	}
	payload.effective = content
	payload.customized = true
	response, err := d.promptFileResponse(payload, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigPromptVersionDelete 对应 DELETE .../versions/{version_id}
func (d Deps) handleConfigPromptVersionDelete(w http.ResponseWriter, r *http.Request) {
	language, filename := chiURLParam(r, "language"), chiURLParam(r, "filename")
	payload, status, err := d.loadPromptFile(language, filename)
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	normalized, err := safePromptVersionID(chiURLParam(r, "version_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	manifest, err := readPromptVersionManifest(payload.customRoot, language, filename)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	active, _ := activePromptVersionID(payload.customRoot, language, filename)
	if normalized == legacyCustomPromptVersionID {
		if !pathExists(payload.customPath) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Prompt 自定义版本不存在"})
			return
		}
		if err := os.Remove(payload.customPath); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		if active == normalized {
			manifest.ActiveVersionID = nil
			if err := writePromptVersionManifest(payload.customRoot, language, filename, manifest); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
				return
			}
		}
	} else {
		versionPath, err := promptVersionFilePath(payload.customRoot, language, filename, normalized)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		exists := false
		for _, item := range manifest.Versions {
			if item["id"] == normalized {
				exists = true
			}
		}
		if !exists || !pathExists(versionPath) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Prompt 自定义版本不存在"})
			return
		}
		if err := os.Remove(versionPath); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		filtered := make([]map[string]any, 0, len(manifest.Versions))
		for _, item := range manifest.Versions {
			if item["id"] != normalized {
				filtered = append(filtered, item)
			}
		}
		manifest.Versions = filtered
		if active == normalized {
			manifest.ActiveVersionID = nil
			if pathExists(payload.customPath) {
				_ = os.Remove(payload.customPath)
			}
		}
		if err := writePromptVersionManifest(payload.customRoot, language, filename, manifest); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
	}
	refreshed, status, err := d.loadPromptFile(language, filename)
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	response, err := d.promptFileResponse(refreshed, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigPromptUpdate 对应 PUT /api/webui/config/prompts/{language}/{filename}
func (d Deps) handleConfigPromptUpdate(w http.ResponseWriter, r *http.Request) {
	language, filename := chiURLParam(r, "language"), chiURLParam(r, "filename")
	payload, status, err := d.loadPromptFile(language, filename)
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	var request promptUpdateRequest
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	if _, err := ensurePromptParametersMatch(d.promptResource(), payload.path, request.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	versionID := ""
	if request.VersionID != nil {
		versionID = *request.VersionID
	}
	if _, err := savePromptVersion(payload.customRoot, language, filename, request.Content, versionID, request.Label, request.CreateVersion); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.MkdirAll(filepath.Dir(payload.customPath), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.WriteFile(payload.customPath, []byte(request.Content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "保存 Prompt 文件失败: " + err.Error()})
		return
	}
	payload.effective = request.Content
	payload.customized = true
	response, err := d.promptFileResponse(payload, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigPromptReset 对应 DELETE /api/webui/config/prompts/{language}/{filename}
func (d Deps) handleConfigPromptReset(w http.ResponseWriter, r *http.Request) {
	language, filename := chiURLParam(r, "language"), chiURLParam(r, "filename")
	payload, status, err := d.loadPromptFile(language, filename)
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	if pathExists(payload.customPath) {
		if err := os.Remove(payload.customPath); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "恢复 Prompt 默认模板失败: " + err.Error()})
			return
		}
		if err := setActivePromptVersion(payload.customRoot, language, filename, ""); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "恢复 Prompt 默认模板失败: " + err.Error()})
			return
		}
	}
	payload.effective = payload.defaultText
	payload.customized = false
	response, err := d.promptFileResponse(payload, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "恢复 Prompt 默认模板失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleConfigMaisakaPromptPreview 对应 GET /api/webui/config/maisaka-prompt-preview
func (d Deps) handleConfigMaisakaPromptPreview(w http.ResponseWriter, r *http.Request) {
	instanceDir := strings.TrimSpace(d.InstanceDir)
	if instanceDir == "" {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	relative := strings.ReplaceAll(strings.TrimSpace(r.URL.Query().Get("path")), "\\", "/")
	if relative == "" || strings.HasPrefix(relative, "/") || strings.Contains("/"+relative+"/", "/../") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "无效的 Prompt 预览路径"})
		return
	}
	root, err := filepath.Abs(filepath.Join(filepath.Clean(instanceDir), "logs", "maisaka_prompt"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "Prompt 预览路径越界"})
		return
	}
	if extension := strings.ToLower(filepath.Ext(target)); extension != ".html" && extension != ".json" && extension != ".txt" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "只允许打开 Prompt 预览文件"})
		return
	}
	if !pathExists(target) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Prompt 预览文件不存在"})
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(target))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	http.ServeFile(w, r, target)
}
