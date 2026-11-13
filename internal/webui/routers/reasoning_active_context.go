package webuiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"maibot-go/internal/maisaka/display"
	maisaruntime "maibot-go/internal/maisaka/runtime"
)

func (d Deps) handleReasoningActiveContext(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeReasoningFailure(w, http.StatusUnprocessableEntity, "session_id 不能为空")
		return
	}
	if d.Agent == nil {
		writeReasoningFailure(w, http.StatusServiceUnavailable, "聊天流运行时未接线：需要 maisaruntime.Agent")
		return
	}
	active, err := d.Agent.ChatActiveContext(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, maisaruntime.ErrChatSessionNotFound) {
			writeReasoningFailure(w, http.StatusNotFound, "聊天流不存在")
			return
		}
		writeReasoningFailure(w, http.StatusInternalServerError, "读取活动上下文失败: "+err.Error())
		return
	}
	expressions, timestamp := []string{}, (*int64)(nil)
	if active.RuntimeActive {
		expressions, timestamp = d.latestReplyerExpressions(sessionID, active)
	}
	jargon := make([]map[string]any, 0, len(active.Jargon))
	for _, entry := range active.Jargon {
		jargon = append(jargon, map[string]any{"name": entry.Name, "meaning": entry.Meaning})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memory":            active.Memory,
		"expressions":       expressions,
		"jargon":            jargon,
		"tools":             active.Tools,
		"runtime_active":    active.RuntimeActive,
		"replyer_timestamp": timestamp,
	})
}

// latestReplyerExpressions 读取最近一次 Replyer 请求实际注入的表达习惯
func (d Deps) latestReplyerExpressions(sessionID string, active maisaruntime.ActiveChatContext) ([]string, *int64) {
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		return []string{}, nil
	}
	lookup := func(id string) (display.PreviewSessionInfo, bool) {
		if id != sessionID {
			return display.PreviewSessionInfo{}, false
		}
		return display.PreviewSessionInfo{
			Platform: active.Platform,
			IsGroup:  active.IsGroup,
			GroupID:  active.GroupID,
			UserID:   active.UserID,
		}, true
	}
	dir := filepath.Join(root, "replyer", display.BuildPreviewChatDirName(sessionID, lookup))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}, nil
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, err := strconv.ParseInt(stem, 10, 64); err != nil {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Slice(files, func(i, j int) bool {
		left, _ := strconv.ParseInt(strings.TrimSuffix(files[i], filepath.Ext(files[i])), 10, 64)
		right, _ := strconv.ParseInt(strings.TrimSuffix(files[j], filepath.Ext(files[j])), 10, 64)
		return left > right
	})
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}
		requestItems, ok := payload["request_items"].([]any)
		if !ok {
			continue
		}
		expressions := expressionHabitsFromRequestItems(requestItems)
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		timestamp, _ := strconv.ParseInt(stem, 10, 64)
		return expressions, &timestamp
	}
	return []string{}, nil
}

func expressionHabitsFromRequestItems(items []any) []string {
	expressions := make([]string, 0)
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := record["parts"].([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			partRecord, ok := part.(map[string]any)
			if !ok {
				continue
			}
			text, ok := partRecord["text"].(string)
			if !ok || !strings.HasPrefix(strings.TrimSpace(text), "【表达习惯参考") {
				continue
			}
			lines := strings.Split(strings.TrimSpace(text), "\n")
			for _, line := range lines[1:] {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				expressions = append(expressions, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			}
		}
	}
	return expressions
}
