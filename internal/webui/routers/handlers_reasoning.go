package webuiapi

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 本文件对齐 ../MaiBot/src/webui/routers/reasoning_process.py 的只读日志浏览端点

const reasoningToolCallRecordWindowMS = 30 * 60 * 1000

func writeReasoningFailure(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

func reasoningQueryInt(r *http.Request, name string, fallback int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func (d Deps) handleReasoningStages(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	infos := listReasoningStageInfos(root)
	stages := make([]string, 0, len(infos))
	for _, info := range infos {
		stages = append(stages, info.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stages": stages, "stage_infos": infos})
}

func (d Deps) handleReasoningClearStage(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	stage, valid := resolveReasoningStageName(chiURLParam(r, "stage"))
	if !valid {
		writeReasoningFailure(w, http.StatusBadRequest, "阶段名称不合法")
		return
	}
	stageDir := filepath.Join(root, stage)
	rel, err := filepath.Rel(root, stageDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == "." {
		writeReasoningFailure(w, http.StatusBadRequest, "无效的推理过程类型")
		return
	}
	info, err := os.Stat(stageDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"stage": stage, "deleted_files": 0})
			return
		}
		writeReasoningFailure(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	if !info.IsDir() {
		writeReasoningFailure(w, http.StatusBadRequest, "推理过程路径不是目录")
		return
	}
	count := 0
	_ = filepath.Walk(stageDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.Mode().IsRegular() {
			count++
		}
		return nil
	})
	if err := os.RemoveAll(stageDir); err != nil {
		writeReasoningFailure(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stage": stage, "deleted_files": count})
}

func (d Deps) handleReasoningFiles(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	page, valid := reasoningQueryInt(r, "page", 1)
	if !valid || page < 1 {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "page 必须大于等于 1")
		return
	}
	pageSize, valid := reasoningQueryInt(r, "page_size", 50)
	if !valid || pageSize < 10 || pageSize > 200 {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "page_size 必须在 10 到 200 之间")
		return
	}
	stageInfos := listReasoningStageInfos(root)
	stages := make([]string, 0, len(stageInfos))
	for _, info := range stageInfos {
		stages = append(stages, info.Name)
	}
	stage, valid := resolveReasoningStageName(r.URL.Query().Get("stage"))
	if !valid {
		writeReasoningFailure(w, http.StatusBadRequest, "阶段名称不合法")
		return
	}
	sessions := listReasoningSessionNames(root, stage)
	session, valid := resolveReasoningSessionName(r.URL.Query().Get("session"), sessions)
	if !valid {
		writeReasoningFailure(w, http.StatusBadRequest, "会话名称不合法")
		return
	}
	infoMap := d.buildReasoningSessionInfos(sessions)
	sessionInfos := reasoningSessionInfosList(sessions, infoMap)
	records := collectReasoningRecords(root, stage, session, infoMap)
	action := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	targetStem := strings.TrimSpace(r.URL.Query().Get("target_stem"))

	var items []*reasoningPromptFile
	total := 0
	if action != "" || search != "" {
		items = hydrateReasoningRecords(root, records, true, false, false)
		if action != "" {
			filtered := make([]*reasoningPromptFile, 0, len(items))
			for _, item := range items {
				if matchesReasoningAction(item, action) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		if search != "" {
			filtered := make([]*reasoningPromptFile, 0, len(items))
			for _, item := range items {
				if matchesReasoningSearch(item, search) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		if stage == "replyer" {
			items = deduplicateReplyerRecords(items)
		}
		total = len(items)
		if page = reasoningTargetStemPage(items, targetStem, page, pageSize); page < 1 {
			page = 1
		}
		items = reasoningPageSlice(items, page, pageSize)
	} else if stage == "replyer" {
		items = hydrateReasoningRecords(root, records, false, false, true)
		items = deduplicateReplyerRecords(items)
		total = len(items)
		if page = reasoningTargetStemPage(items, targetStem, page, pageSize); page < 1 {
			page = 1
		}
		items = reasoningPageSlice(items, page, pageSize)
	} else {
		total = len(records)
		if targetStem != "" {
			for index, record := range records {
				if record.Stem == targetStem {
					page = index/pageSize + 1
					break
				}
			}
		}
		sliced := reasoningPageSlice(records, page, pageSize)
		items = hydrateReasoningRecords(root, sliced, false, true, true)
	}
	if items == nil {
		items = []*reasoningPromptFile{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": pageSize,
		"stages": stages, "stage_infos": stageInfos, "sessions": sessions,
		"session_infos": sessionInfos, "selected_session": session,
	})
}

func reasoningTargetStemPage(items []*reasoningPromptFile, targetStem string, page, pageSize int) int {
	if targetStem == "" {
		return page
	}
	for index, item := range items {
		if item.Stem == targetStem {
			return index/pageSize + 1
		}
	}
	return page
}

func reasoningPageSlice(items []*reasoningPromptFile, page, pageSize int) []*reasoningPromptFile {
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []*reasoningPromptFile{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func resolveReasoningLogRequestPath(root, raw string, allowed map[string]bool) (string, int, string) {
	clean := filepath.FromSlash(raw)
	if filepath.IsAbs(clean) || strings.Contains(raw, "..") {
		return "", http.StatusBadRequest, "路径不合法"
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", http.StatusBadRequest, "路径不合法"
	}
	if !allowed[strings.ToLower(filepath.Ext(full))] {
		return "", http.StatusBadRequest, "不支持的文件类型"
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", http.StatusNotFound, "文件不存在"
	}
	return full, 0, ""
}

func (d Deps) handleReasoningFile(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "path 不能为空")
		return
	}
	path, status, detail := resolveReasoningLogRequestPath(root, rawPath, map[string]bool{".txt": true, ".json": true})
	if status != 0 {
		writeReasoningFailure(w, status, detail)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeReasoningFailure(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		writeReasoningFailure(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	content := string(bytes)
	metadata := map[string]any{}
	messageAvatars := map[string]reasoningPromptMessageAvatar{}
	relPath := reasoningRelativePosix(root, path)
	if strings.ToLower(filepath.Ext(path)) == ".json" {
		payload, _ := readJSONMap(path)
		if payload == nil {
			payload = map[string]any{}
		}
		metadata = extractReasoningMetadataFromJSONPayload(payload)
		legacyEvents := strings.TrimSuffix(path, ".json") + ".events.jsonl"
		if stringValue(payload["record_type"]) == "jargon_learning_update" || fileExists(legacyEvents) {
			if pretty, err := json.MarshalIndent(payload, "", "  "); err == nil {
				content = string(pretty)
			}
		}
		messageAvatars = d.loadPromptMessageAvatarMap(relPath, content)
	} else {
		metadata = extractReasoningMetadataFromText(content)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": relPath, "content": strings.ToValidUTF8(content, "\uFFFD"),
		"size": info.Size(), "modified_at": float64(info.ModTime().UnixNano()) / 1e9,
		"model_name":        nullableReasoningString(metadata["model_name"]),
		"duration_ms":       nullableReasoningFloat(metadata["duration_ms"]),
		"prompt_tokens":     nullableReasoningInt(metadata["prompt_tokens"]),
		"completion_tokens": nullableReasoningInt(metadata["completion_tokens"]),
		"total_tokens":      nullableReasoningInt(metadata["total_tokens"]),
		"message_avatars":   messageAvatars,
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func nullableReasoningString(value any) any {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return nil
}

func nullableReasoningFloat(value any) any {
	if number, ok := value.(float64); ok {
		return number
	}
	return nil
}

func nullableReasoningInt(value any) any {
	if number, ok := value.(int64); ok {
		return number
	}
	return nil
}

func (d Deps) reasoningReplayImageRoots() (string, []string) {
	instanceDir := strings.TrimSpace(d.InstanceDir)
	if instanceDir == "" {
		return "", nil
	}
	root := filepath.Clean(instanceDir)
	return root, []string{
		filepath.Join(root, "data", "images"), filepath.Join(root, "data", "emoji"),
		filepath.Join(root, "data", "prompt_imgs"), filepath.Join(root, "data", "html_imgs"),
	}
}

func (d Deps) handleReasoningImage(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, roots := d.reasoningReplayImageRoots()
	if root == "" {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "path 不能为空")
		return
	}
	candidate := filepath.FromSlash(rawPath)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	resolved, err := filepath.Abs(candidate)
	if err != nil {
		writeReasoningFailure(w, http.StatusBadRequest, "图片引用路径不在允许目录内: "+rawPath)
		return
	}
	allowed := false
	for _, imageRoot := range roots {
		rel, err := filepath.Rel(imageRoot, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		writeReasoningFailure(w, http.StatusBadRequest, "图片引用路径不在允许目录内: "+rawPath)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil {
		writeReasoningFailure(w, http.StatusBadRequest, "图片引用文件不存在: "+rawPath)
		return
	}
	if !info.Mode().IsRegular() {
		writeReasoningFailure(w, http.StatusBadRequest, "图片引用不是文件: "+rawPath)
		return
	}
	mediaType := mime.TypeByExtension(filepath.Ext(resolved))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	http.ServeFile(w, r, resolved)
}

func (d Deps) handleReasoningHTML(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "path 不能为空")
		return
	}
	path, status, detail := resolveReasoningLogRequestPath(root, rawPath, map[string]bool{".html": true})
	if status != 0 {
		writeReasoningFailure(w, status, detail)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	http.ServeFile(w, r, path)
}

func reasoningToolCallRecord(callID, stage, session, stem string) map[string]any {
	return map[string]any{"call_id": callID, "stage": stage, "session": session, "stem": stem}
}

func findReasoningToolCallStems(sessionDir string, sourceTimestamp int64, pending map[string]bool) map[string]string {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return map[string]string{}
	}
	type candidate struct {
		path string
		stem int64
	}
	candidates := []candidate{}
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		stem, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), 10, 64)
		if err != nil || stem < sourceTimestamp || stem > sourceTimestamp+reasoningToolCallRecordWindowMS {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(sessionDir, entry.Name()), stem: stem})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].stem < candidates[j].stem })
	found := map[string]string{}
	for _, item := range candidates {
		file, err := os.Open(item.path)
		if err != nil {
			continue
		}
		buffer := make([]byte, 8192)
		n, _ := file.Read(buffer)
		_ = file.Close()
		head := string(buffer[:n])
		for callID := range pending {
			if _, exists := found[callID]; exists {
				continue
			}
			pattern := regexp.MustCompile("ID[:：]\\s*" + regexp.QuoteMeta(callID) + "([^A-Za-z0-9_-]|$)")
			if pattern.MatchString(head) {
				found[callID] = strconv.FormatInt(item.stem, 10)
			}
		}
		if len(found) == len(pending) {
			break
		}
	}
	return found
}

func (d Deps) handleReasoningToolCallRecords(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if !isSafeReasoningName(session) {
		writeReasoningFailure(w, http.StatusBadRequest, "会话名称不合法")
		return
	}
	sourceStage, valid := resolveReasoningStageName(r.URL.Query().Get("source_stage"))
	if !valid {
		writeReasoningFailure(w, http.StatusBadRequest, "阶段名称不合法")
		return
	}
	sourceStem := strings.TrimSpace(r.URL.Query().Get("source_stem"))
	pending := map[string]bool{}
	for _, callID := range r.URL.Query()["call_id"] {
		if trimmed := strings.TrimSpace(callID); trimmed != "" {
			pending[trimmed] = true
		}
	}
	sourceTimestamp, err := strconv.ParseInt(sourceStem, 10, 64)
	if len(pending) == 0 || err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"records": []any{}})
		return
	}
	records := []map[string]any{}
	for _, stage := range listReasoningStageNames(root) {
		if stage == sourceStage || len(pending) == 0 {
			continue
		}
		sessionDir := filepath.Join(root, stage, session)
		if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
			continue
		}
		found := findReasoningToolCallStems(sessionDir, sourceTimestamp, pending)
		for callID, stem := range found {
			records = append(records, reasoningToolCallRecord(callID, stage, session, stem))
			delete(pending, callID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

func reasoningPayloadOutputsToolCall(payload map[string]any, callID string) bool {
	items, _ := payload["output_items"].([]any)
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		if item == nil || stringValue(item["item_type"]) != "FunctionCallItem" {
			continue
		}
		if call, ok := item["tool_call"].(map[string]any); ok && stringValue(call["call_id"]) == callID {
			return true
		}
	}
	return false
}

func findReasoningToolCallSourceStem(sessionDir string, toolTimestamp int64, callID string) string {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		stem int64
	}
	candidates := []candidate{}
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		stem, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), 10, 64)
		if err != nil || stem < toolTimestamp-reasoningToolCallRecordWindowMS || stem > toolTimestamp {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(sessionDir, entry.Name()), stem: stem})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].stem > candidates[j].stem })
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	for _, item := range candidates {
		if payload, ok := readJSONMap(item.path); ok && reasoningPayloadOutputsToolCall(payload, callID) {
			return strconv.FormatInt(item.stem, 10)
		}
	}
	return ""
}

func (d Deps) handleReasoningToolCallSource(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if !isSafeReasoningName(session) {
		writeReasoningFailure(w, http.StatusBadRequest, "会话名称不合法")
		return
	}
	toolStage, valid := resolveReasoningStageName(r.URL.Query().Get("stage"))
	if !valid {
		writeReasoningFailure(w, http.StatusBadRequest, "阶段名称不合法")
		return
	}
	stem := strings.TrimSpace(r.URL.Query().Get("stem"))
	callID := strings.TrimSpace(r.URL.Query().Get("call_id"))
	toolTimestamp, err := strconv.ParseInt(stem, 10, 64)
	if callID == "" || err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"record": nil})
		return
	}
	stageNames := listReasoningStageNames(root)
	sort.SliceStable(stageNames, func(i, j int) bool { return stageNames[i] == "planner" && stageNames[j] != "planner" })
	for _, stage := range stageNames {
		if stage == toolStage {
			continue
		}
		sessionDir := filepath.Join(root, stage, session)
		if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
			continue
		}
		if sourceStem := findReasoningToolCallSourceStem(sessionDir, toolTimestamp, callID); sourceStem != "" {
			writeJSON(w, http.StatusOK, map[string]any{"record": reasoningToolCallRecord(callID, stage, session, sourceStem)})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": nil})
}
