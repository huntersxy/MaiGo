package webuiapi

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

type promptUpdateRequest struct {
	Content       string  `json:"content"`
	VersionID     *string `json:"version_id"`
	Label         string  `json:"label"`
	CreateVersion bool    `json:"create_version"`
}

func (d Deps) handleConfigPromptBlocks(w http.ResponseWriter, r *http.Request) {
	language := chiURLParam(r, "language")
	if err := safePromptSegment(language, "reply_group_system.prompt"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, resourcePath, err := d.resolveDefaultPrompt(language, "reply_group_system")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if resourcePath == "" {
		writeAPIError(w, http.StatusNotFound, "Prompt 文件不存在")
		return
	}
	isGroup := true
	switch r.URL.Query().Get("filename") {
	case "reply_private_system.prompt", "reply_private.prompt", "reply_private_followup.prompt",
		"reply_private_mai_replier.prompt", "reply_private_followup_mai_replier.prompt", "reply_mai_replier_private_system.prompt":
		isGroup = false
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply_guidance": replyer.ReplyGuidance(language, isGroup)})
}

func defaultPromptRelativePath(resourcePath string) (string, error) {
	relative, err := filepath.Localize(resourcePath)
	if err != nil || relative == "." || !strings.HasSuffix(relative, ".prompt") {
		return "", errors.New("无效的默认 Prompt 路径")
	}
	return relative, nil
}

func (d Deps) readDefaultPrompt(resource fs.FS, resourcePath string) ([]byte, fs.FileInfo, error) {
	relative, err := defaultPromptRelativePath(resourcePath)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(prompt.DefaultPromptsRoot(d.desktopPetRoot()))
	if err == nil {
		defer root.Close()
		if info, err := root.Stat(relative); err == nil {
			content, readErr := root.ReadFile(relative)
			return content, info, readErr
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	content, err := fs.ReadFile(resource, resourcePath)
	if err != nil {
		return nil, nil, err
	}
	info, err := fs.Stat(resource, resourcePath)
	return content, info, err
}

func (d Deps) handleConfigPromptDefaultUpdate(w http.ResponseWriter, r *http.Request) {
	payload, status, err := d.loadPromptFile(chiURLParam(r, "language"), chiURLParam(r, "filename"))
	if err != nil {
		writeJSON(w, status, map[string]any{"detail": err.Error()})
		return
	}
	var request struct {
		Content *string `json:"content"`
	}
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	if request.Content == nil {
		writeValidationError(w, "缺少 content")
		return
	}
	content := *request.Content
	validation := buildPromptValidation(payload.defaultText, content)
	if !validation.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": validation.Message})
		return
	}
	if err := d.writeDefaultPrompt(payload.path, content); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "保存默认 Prompt 文件失败: "+err.Error())
		return
	}
	payload.defaultText = content
	payload.effective = content
	payload.customized = false
	response, err := d.promptFileResponse(payload, "")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (d Deps) writeDefaultPrompt(resourcePath, content string) error {
	relative, err := defaultPromptRelativePath(resourcePath)
	if err != nil {
		return err
	}
	rootDir := prompt.DefaultPromptsRoot(d.desktopPetRoot())
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	directory := filepath.Dir(relative)
	if err := root.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temp := filepath.Join(directory, fmt.Sprintf(".prompt-%s.tmp", uuid.NewString()))
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, writeErr := file.WriteString(content)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return root.Rename(temp, relative)
}

// handleConfigPromptsList 对应 GET /api/webui/config/prompts
func (d Deps) handleConfigPromptsList(w http.ResponseWriter, r *http.Request) {
	type resourcePrompt struct {
		promptFileEntry
		resource fs.FS
	}
	byLanguage := map[string]map[string]resourcePrompt{}
	for _, resource := range d.promptResources() {
		entries, err := fs.ReadDir(resource, ".")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "列出 Prompt 文件失败: " + err.Error()})
			return
		}
		for _, entry := range entries {
			language := entry.Name()
			locale, err := prompt.NormalizeLocale(language)
			if !entry.IsDir() || err != nil || locale != language {
				continue
			}
			promptFiles, err := walkPromptFiles(resource, language)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "列出 Prompt 文件失败: " + err.Error()})
				return
			}
			if byLanguage[language] == nil {
				byLanguage[language] = map[string]resourcePrompt{}
			}
			for _, file := range promptFiles {
				byLanguage[language][path.Base(file.Path)] = resourcePrompt{file, resource}
			}
		}
	}
	languages := slices.Sorted(maps.Keys(byLanguage))
	customRoot, _ := d.customPromptsRoot()
	files := make(map[string]any, len(languages))
	for _, language := range languages {
		promptFiles := byLanguage[language]
		infos := make([]any, 0, len(promptFiles))
		for _, name := range slices.Sorted(maps.Keys(promptFiles)) {
			file := promptFiles[name]
			defaultPath := file.Path
			customPath, err := safePromptPath(customRoot, language, name)
			if err != nil {
				continue
			}
			customized := pathExists(customPath)
			defaultContent, stat, err := d.readDefaultPrompt(file.resource, defaultPath)
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
			meta := prompt.ReadTemplateMetadata(file.resource, defaultPath)
			kind := "main"
			if isInstructionPromptCategory(file.Category) {
				kind = "instruction"
			}
			// 内嵌模板没有真实 mtime，零值时间转 epoch 会得到负数，按 0 上报
			modifiedAt := float64(stat.ModTime().UnixNano()) / 1e9
			if stat.ModTime().IsZero() {
				modifiedAt = 0
			}
			infos = append(infos, map[string]any{
				"name": name, "relative_path": strings.TrimPrefix(defaultPath, language+"/"), "size": stat.Size(),
				"modified_at":  modifiedAt,
				"display_name": meta.DisplayName, "advanced": meta.Advanced, "kind": kind,
				"description": meta.Description,
				"customized":  customized, "custom_version_count": len(versions),
				"validation": buildPromptValidation(string(defaultContent), string(effectiveContent)),
			})
		}
		files[language] = infos
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "languages": languages, "files": files})
}

// isInstructionPromptCategory 判断模板相对目录是否属于 instructions（对齐 PromptFileInfo.kind）
func isInstructionPromptCategory(category string) bool {
	for _, part := range strings.Split(category, "/") {
		if part == "instructions" {
			return true
		}
	}
	return false
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
	builtinName := prompt.CanonicalPromptName(strings.TrimSuffix(strings.TrimSpace(filename), ".prompt"))
	resource, resourcePath, err := d.resolveDefaultPrompt(language, builtinName)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if resourcePath == "" {
		return nil, http.StatusNotFound, errors.New("Prompt 文件不存在")
	}
	defaultRaw, _, err := d.readDefaultPrompt(resource, resourcePath)
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
	builtinName := prompt.CanonicalPromptName(strings.TrimSuffix(strings.TrimSpace(filename), ".prompt"))
	_, resourcePath, err := d.resolveDefaultPrompt(language, builtinName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if resourcePath == "" {
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
		if err != nil {
			return "", "", http.StatusInternalServerError, err
		}
		return string(raw), normalized, 0, nil
	}
	versionPath, err := promptVersionFilePath(customRoot, language, filename, normalized)
	if err != nil {
		return "", "", http.StatusBadRequest, err
	}
	if !pathExists(versionPath) {
		return "", "", http.StatusNotFound, errors.New("Prompt 自定义版本不存在")
	}
	raw, err := os.ReadFile(versionPath)
	if err != nil {
		return "", "", http.StatusInternalServerError, err
	}
	return string(raw), normalized, 0, nil
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
	validation := buildPromptValidation(payload.defaultText, content)
	if !validation.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": validation.Message})
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
	validation := buildPromptValidation(payload.defaultText, request.Content)
	if !validation.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": validation.Message})
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
