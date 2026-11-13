package webuiapi

import (
	"encoding/json"
	"html"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gorm.io/gorm"

	memory "maibot-go/internal/common/database"
)

// 对齐 MaiBot reasoning_process.py 的 message_avatars：从 prompt JSON 抽 msg_id，再回查 mai_messages。

var (
	reasoningMessageTagPattern     = regexp.MustCompile(`(?i)<message\b(?P<attrs>[^>]*)>`)
	reasoningMessageTagAttrPattern = regexp.MustCompile(`([A-Za-z_][\w:-]*)\s*=\s*"([^"]*)"`)
)

// reasoningPromptMessageAvatar 对齐 ReasoningPromptMessageAvatar
type reasoningPromptMessageAvatar struct {
	MessageID   string  `json:"message_id"`
	Platform    string  `json:"platform"`
	UserID      string  `json:"user_id"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

func buildWebUIAvatarURL(platform, userID string) *string {
	normalizedPlatform := strings.ToLower(strings.TrimSpace(platform))
	normalizedUserID := strings.TrimSpace(userID)
	if normalizedPlatform == "" || normalizedUserID == "" {
		return nil
	}
	value := "/api/webui/avatar?platform=" + url.QueryEscape(normalizedPlatform) +
		"&user_id=" + url.QueryEscape(normalizedUserID)
	return &value
}

func parseReasoningMessageTagAttrs(text string) []map[string]string {
	attrsList := make([]map[string]string, 0)
	for _, tagMatch := range reasoningMessageTagPattern.FindAllStringSubmatch(text, -1) {
		if len(tagMatch) < 2 {
			continue
		}
		attrs := map[string]string{}
		for _, attrMatch := range reasoningMessageTagAttrPattern.FindAllStringSubmatch(tagMatch[1], -1) {
			if len(attrMatch) < 3 {
				continue
			}
			key := attrMatch[1]
			value := html.UnescapeString(attrMatch[2])
			if key != "" && value != "" {
				attrs[key] = value
			}
		}
		if len(attrs) > 0 {
			attrsList = append(attrsList, attrs)
		}
	}
	return attrsList
}

func appendReasoningMessageIDsFromText(messageIDs *[]string, seen map[string]struct{}, value any) {
	text, ok := value.(string)
	if !ok || !strings.Contains(text, "<message") {
		return
	}
	for _, attrs := range parseReasoningMessageTagAttrs(text) {
		messageID := strings.TrimSpace(attrs["msg_id"])
		if messageID == "" {
			continue
		}
		if _, exists := seen[messageID]; exists {
			continue
		}
		seen[messageID] = struct{}{}
		*messageIDs = append(*messageIDs, messageID)
	}
}

func appendReasoningMessageIDsFromValue(messageIDs *[]string, seen map[string]struct{}, value any) {
	switch typed := value.(type) {
	case string:
		appendReasoningMessageIDsFromText(messageIDs, seen, typed)
	case []any:
		for _, item := range typed {
			appendReasoningMessageIDsFromValue(messageIDs, seen, item)
		}
	case map[string]any:
		for _, item := range typed {
			appendReasoningMessageIDsFromValue(messageIDs, seen, item)
		}
	}
}

func extractMessageIDsFromPromptPayload(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	messageIDs := make([]string, 0)
	seen := map[string]struct{}{}

	appendReasoningMessageIDsFromValue(&messageIDs, seen, payload["request_items"])
	appendReasoningMessageIDsFromValue(&messageIDs, seen, payload["output_items"])

	// v1-v4 旧记录只在读取边界扫描
	if messages, ok := payload["messages"].([]any); ok {
		for _, message := range messages {
			if typed, ok := message.(map[string]any); ok {
				appendReasoningMessageIDsFromText(&messageIDs, seen, structuredPromptContentToText(typed["content"]))
			}
		}
	}
	if output, ok := payload["output"].(map[string]any); ok {
		appendReasoningMessageIDsFromText(&messageIDs, seen, structuredPromptContentToText(output["content"]))
	}
	if request, ok := payload["request"].(map[string]any); ok {
		appendReasoningMessageIDsFromText(&messageIDs, seen, request["selection_reason"])
	}
	return messageIDs
}

func messageAvatarFromDBRecord(row memory.MessageLog) *reasoningPromptMessageAvatar {
	messageID := strings.TrimSpace(row.ExternalMessageID)
	if messageID == "" {
		messageID = strconv.FormatInt(row.MessageID, 10)
	}
	platform := strings.TrimSpace(row.Platform)
	userID := strings.TrimSpace(row.ExternalUserID)
	if userID == "" && row.UserID != 0 {
		userID = strconv.FormatInt(row.UserID, 10)
	}
	if messageID == "" || platform == "" || userID == "" {
		return nil
	}
	displayName := strings.TrimSpace(row.UserCardname)
	if displayName == "" {
		displayName = strings.TrimSpace(row.UserNickname)
	}
	return &reasoningPromptMessageAvatar{
		MessageID:   messageID,
		Platform:    platform,
		UserID:      userID,
		DisplayName: displayName,
		AvatarURL:   buildWebUIAvatarURL(platform, userID),
	}
}

func loadMessageAvatarMap(db *gorm.DB, messageIDs []string, sessionInfo *reasoningSessionInfo) map[string]reasoningPromptMessageAvatar {
	avatars := map[string]reasoningPromptMessageAvatar{}
	if db == nil || len(messageIDs) == 0 {
		return avatars
	}
	sessionIDs := make([]string, 0, 2)
	if sessionInfo != nil {
		if name := strings.TrimSpace(sessionInfo.Name); name != "" {
			sessionIDs = append(sessionIDs, name)
		}
		if sessionInfo.ResolvedSessionID != nil {
			if resolved := strings.TrimSpace(*sessionInfo.ResolvedSessionID); resolved != "" {
				sessionIDs = append(sessionIDs, resolved)
			}
		}
	}
	query := db.Model(&memory.MessageLog{}).Where(
		"CAST(message_id AS TEXT) IN ? OR external_message_id IN ?",
		messageIDs, messageIDs,
	)
	if len(sessionIDs) > 0 {
		query = query.Where("session_id IN ?", sessionIDs)
	}
	var rows []memory.MessageLog
	if err := query.Find(&rows).Error; err != nil {
		return avatars
	}
	for _, row := range rows {
		avatar := messageAvatarFromDBRecord(row)
		if avatar == nil {
			continue
		}
		if _, exists := avatars[avatar.MessageID]; exists {
			continue
		}
		avatars[avatar.MessageID] = *avatar
	}
	return avatars
}

func (d Deps) resolveContentSessionInfo(relativePath string) *reasoningSessionInfo {
	parts := strings.Split(filepath.ToSlash(strings.TrimSpace(relativePath)), "/")
	if len(parts) < 3 {
		return nil
	}
	sessionName := parts[1]
	if !isSafeReasoningName(sessionName) {
		return nil
	}
	infos := d.buildReasoningSessionInfos([]string{sessionName})
	return infos[sessionName]
}

func (d Deps) loadPromptMessageAvatarMap(relativePath, content string) map[string]reasoningPromptMessageAvatar {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil || payload == nil {
		return map[string]reasoningPromptMessageAvatar{}
	}
	messageIDs := extractMessageIDsFromPromptPayload(payload)
	if len(messageIDs) == 0 {
		return map[string]reasoningPromptMessageAvatar{}
	}
	var db *gorm.DB
	if d.Memory != nil {
		db = d.Memory.GetDB()
	}
	return loadMessageAvatarMap(db, messageIDs, d.resolveContentSessionInfo(relativePath))
}
