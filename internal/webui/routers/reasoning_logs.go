package webuiapi

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 本文件对齐 ../MaiBot/src/webui/routers/reasoning_process.py 的日志目录、会话与记录索引逻辑

const (
	reasoningAllGroupSessions = "__all_group_chats__"
	reasoningCacheSeparator   = "\x00"
)

// reasoningStageInfo 推理过程类型概要
type reasoningStageInfo struct {
	Name             string  "json:\"name\""
	SessionCount     int     "json:\"session_count\""
	LatestModifiedAt float64 "json:\"latest_modified_at\""
}

// reasoningSessionInfo 日志目录对应的聊天流信息
type reasoningSessionInfo struct {
	Name                  string  "json:\"name\""
	Platform              string  "json:\"platform\""
	ChatType              string  "json:\"chat_type\""
	TargetID              string  "json:\"target_id\""
	ResolvedSessionID     *string "json:\"resolved_session_id\""
	DisplayName           string  "json:\"display_name\""
	AccountID             *string "json:\"account_id\""
	MatchedCurrentAccount bool    "json:\"matched_current_account\""
}

// reasoningSelectedExpression 一次回复选中的表达对
type reasoningSelectedExpression struct {
	Situation string "json:\"situation\""
	Style     string "json:\"style\""
}

// reasoningPromptFile 推理过程日志条目
type reasoningPromptFile struct {
	Stage               string                        "json:\"stage\""
	SessionID           string                        "json:\"session_id\""
	ResolvedSessionID   *string                       "json:\"resolved_session_id\""
	SessionDisplayName  *string                       "json:\"session_display_name\""
	Platform            *string                       "json:\"platform\""
	ChatType            *string                       "json:\"chat_type\""
	TargetID            *string                       "json:\"target_id\""
	Stem                string                        "json:\"stem\""
	Timestamp           *int64                        "json:\"timestamp\""
	TextPath            *string                       "json:\"text_path\""
	HTMLPath            *string                       "json:\"html_path\""
	JSONPath            *string                       "json:\"json_path\""
	OutputPreview       *string                       "json:\"output_preview\""
	SelectedExpressions []reasoningSelectedExpression "json:\"selected_expressions\""
	ActionPreview       *string                       "json:\"action_preview\""
	DisplayTitle        *string                       "json:\"display_title\""
	RelatedJSONPaths    []string                      "json:\"related_json_paths\""
	ModelName           *string                       "json:\"model_name\""
	DurationMS          *float64                      "json:\"duration_ms\""
	PromptTokens        *int64                        "json:\"prompt_tokens\""
	CompletionTokens    *int64                        "json:\"completion_tokens\""
	TotalTokens         *int64                        "json:\"total_tokens\""
	Size                int64                         "json:\"size\""
	ModifiedAt          float64                       "json:\"modified_at\""
}

func (d Deps) reasoningPromptLogRoot() (string, bool) {
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(filepath.Clean(dir), "logs", "maisaka_prompt"), true
}

func isSafeReasoningName(name string) bool {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

func reasoningRelativePosix(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func listReasoningStageNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return []string{}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && isSafeReasoningName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func listReasoningStageInfos(root string) []reasoningStageInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return []reasoningStageInfo{}
	}
	infos := make([]reasoningStageInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !isSafeReasoningName(entry.Name()) {
			continue
		}
		sessionEntries, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		count := 0
		var latest float64
		for _, sessionEntry := range sessionEntries {
			if !sessionEntry.IsDir() || !isSafeReasoningName(sessionEntry.Name()) {
				continue
			}
			count++
			info, err := sessionEntry.Info()
			if err != nil {
				continue
			}
			modified := float64(info.ModTime().UnixNano()) / 1e9
			if modified > latest {
				latest = modified
			}
		}
		infos = append(infos, reasoningStageInfo{Name: entry.Name(), SessionCount: count, LatestModifiedAt: latest})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func resolveReasoningStageName(stage string) (string, bool) {
	normalized := strings.TrimSpace(stage)
	if normalized == "" || normalized == "all" {
		return "planner", true
	}
	return normalized, isSafeReasoningName(normalized)
}

func listReasoningSessionNames(root, stage string) []string {
	entries, err := os.ReadDir(filepath.Join(root, stage))
	if err != nil {
		return []string{}
	}
	type sessionEntry struct {
		name    string
		modTime int64
	}
	sessions := make([]sessionEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !isSafeReasoningName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		sessions = append(sessions, sessionEntry{name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].modTime > sessions[j].modTime })
	names := make([]string, 0, len(sessions))
	for _, session := range sessions {
		names = append(names, session.name)
	}
	return names
}

func resolveReasoningSessionName(session string, sessions []string) (string, bool) {
	normalized := strings.TrimSpace(session)
	if normalized == reasoningAllGroupSessions {
		return reasoningAllGroupSessions, true
	}
	if normalized == "" || normalized == "all" || normalized == "auto" {
		if len(sessions) > 0 {
			return sessions[0], true
		}
		return "", true
	}
	if !isSafeReasoningName(normalized) {
		return "", false
	}
	for _, name := range sessions {
		if name == normalized {
			return normalized, true
		}
	}
	return "", true
}

func parseReasoningSessionDirectoryName(name string) (platform, chatType, targetID string, ok bool) {
	for _, candidate := range []string{"group", "private"} {
		marker := "_" + candidate + "_"
		index := strings.Index(name, marker)
		if index <= 0 {
			continue
		}
		platform = strings.TrimSpace(name[:index])
		targetID = strings.TrimSpace(name[index+len(marker):])
		if platform != "" && targetID != "" {
			return platform, candidate, targetID, true
		}
	}
	return "", "", "", false
}


func reasoningSessionInfosList(sessions []string, infoMap map[string]*reasoningSessionInfo) []*reasoningSessionInfo {
	result := make([]*reasoningSessionInfo, 0, len(sessions))
	for _, name := range sessions {
		if info, ok := infoMap[name]; ok {
			result = append(result, info)
		}
	}
	return result
}

func isGroupReasoningSession(name string, info *reasoningSessionInfo) bool {
	if info != nil {
		return info.ChatType == "group"
	}
	_, chatType, _, ok := parseReasoningSessionDirectoryName(name)
	return ok && chatType == "group"
}

func stringPointer(value string) *string { return &value }

func reasoningRecordTimestamp(record *reasoningPromptFile) int64 {
	if record.Timestamp != nil {
		return *record.Timestamp
	}
	return 0
}

func sortReasoningRecords(items []*reasoningPromptFile) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ModifiedAt != items[j].ModifiedAt {
			return items[i].ModifiedAt > items[j].ModifiedAt
		}
		return reasoningRecordTimestamp(items[i]) > reasoningRecordTimestamp(items[j])
	})
}

func collectReasoningRecordsForSession(root, stage, session string, infoMap map[string]*reasoningSessionInfo) []*reasoningPromptFile {
	sessionDir := filepath.Join(root, stage, session)
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil
	}
	records := make(map[string]*reasoningPromptFile)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		suffix := strings.ToLower(filepath.Ext(entry.Name()))
		if suffix != ".txt" && suffix != ".html" && suffix != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		key := stage + reasoningCacheSeparator + session + reasoningCacheSeparator + stem
		record, ok := records[key]
		if !ok {
			record = &reasoningPromptFile{Stage: stage, SessionID: session, Stem: stem, RelatedJSONPaths: []string{}}
			if sessionInfo, ok := infoMap[session]; ok {
				record.ResolvedSessionID = sessionInfo.ResolvedSessionID
				record.SessionDisplayName = stringPointer(sessionInfo.DisplayName)
				record.Platform = stringPointer(sessionInfo.Platform)
				record.ChatType = stringPointer(sessionInfo.ChatType)
				record.TargetID = stringPointer(sessionInfo.TargetID)
			}
			if timestamp, err := strconv.ParseInt(stem, 10, 64); err == nil {
				record.Timestamp = &timestamp
			}
			records[key] = record
		}
		record.Size += info.Size()
		if modified := float64(info.ModTime().UnixNano()) / 1e9; modified > record.ModifiedAt {
			record.ModifiedAt = modified
		}
		relative := reasoningRelativePosix(root, filepath.Join(sessionDir, entry.Name()))
		switch suffix {
		case ".txt":
			record.TextPath = stringPointer(relative)
		case ".html":
			record.HTMLPath = stringPointer(relative)
		case ".json":
			record.JSONPath = stringPointer(relative)
		}
	}
	items := make([]*reasoningPromptFile, 0, len(records))
	for _, record := range records {
		items = append(items, record)
	}
	sortReasoningRecords(items)
	return items
}

func collectReasoningRecords(root, stage, session string, infoMap map[string]*reasoningSessionInfo) []*reasoningPromptFile {
	if session != reasoningAllGroupSessions {
		return collectReasoningRecordsForSession(root, stage, session, infoMap)
	}
	var items []*reasoningPromptFile
	for name, info := range infoMap {
		if !isGroupReasoningSession(name, info) {
			continue
		}
		items = append(items, collectReasoningRecordsForSession(root, stage, name, infoMap)...)
	}
	sortReasoningRecords(items)
	return items
}

func resolveReasoningPromptLogPath(root, relative, suffix string) (string, bool) {
	clean := filepath.FromSlash(relative)
	if filepath.IsAbs(clean) || strings.Contains(relative, "..") {
		return "", false
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	if strings.ToLower(filepath.Ext(full)) != suffix {
		return "", false
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return full, true
}

func resolveReasoningRecordPath(root string, relative *string, suffix string) (string, bool) {
	if relative == nil || *relative == "" {
		return "", false
	}
	return resolveReasoningPromptLogPath(root, *relative, suffix)
}
